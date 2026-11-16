package resources

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeSlots struct{ inUse, total int }

func (f fakeSlots) SlotsInUse() int { return f.inUse }
func (f fakeSlots) SlotsTotal() int { return f.total }

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const netDevHeader = "Inter-|   Receive                                                |  Transmit\n" +
	" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"

func netDev(rx, tx string) string {
	return netDevHeader +
		"    lo: 999999 10 0 0 0 0 0 0 999999 10 0 0 0 0 0 0\n" +
		"  eth0: " + rx + " 10 0 0 0 0 0 0 " + tx + " 10 0 0 0 0 0 0\n"
}

func TestContainerScope(t *testing.T) {
	root := t.TempDir()
	write(t, root, "proc/meminfo", "MemTotal:       16000000 kB\nMemAvailable:    8000000 kB\n")
	write(t, root, "sys/fs/cgroup/cpu.stat", "usage_usec 1000000\nuser_usec 1\n")
	write(t, root, "sys/fs/cgroup/cpu.max", "200000 100000\n")
	write(t, root, "sys/fs/cgroup/memory.current", "300000000\n")
	write(t, root, "sys/fs/cgroup/memory.stat", "anon 1\ninactive_file 100000000\n")
	write(t, root, "sys/fs/cgroup/memory.max", "536870912\n")
	write(t, root, "sys/fs/cgroup/pids.current", "7\n")
	write(t, root, "proc/net/dev", netDev("1000", "5000"))

	start := time.Now()
	s := newSampler("node-1", fakeSlots{1, 2}, root, 8)
	first := s.Snapshot()
	if first.Scope != ScopeContainer || first.CPU != nil || first.Net != nil {
		t.Fatalf("first snapshot should have no rates yet: %+v", first)
	}
	if first.Slots != (Slots{InUse: 1, Total: 2}) {
		t.Fatalf("slots %+v", first.Slots)
	}

	// 3 CPU-seconds over 2 wall seconds = 1.5 cores of a 2-core quota.
	write(t, root, "sys/fs/cgroup/cpu.stat", "usage_usec 4000000\n")
	write(t, root, "proc/net/dev", netDev("3000", "9000"))
	s.sample(start.Add(2 * time.Second))
	got := s.Snapshot()

	if got.CPU == nil || got.CPU.Cores != 2 || got.CPU.CoresUsed < 1.4 || got.CPU.CoresUsed > 1.6 {
		t.Fatalf("cpu %+v", got.CPU)
	}
	if got.CPU.Percent < 70 || got.CPU.Percent > 80 {
		t.Fatalf("cpu percent %v", got.CPU.Percent)
	}
	if got.Memory == nil || got.Memory.UsedBytes != 200000000 || got.Memory.LimitBytes != 536870912 {
		t.Fatalf("memory %+v", got.Memory)
	}
	if got.PIDs == nil || *got.PIDs != 7 {
		t.Fatalf("pids %v", got.PIDs)
	}
	// loopback is excluded: 2000 B and 4000 B over ~2 s.
	if got.Net == nil || got.Net.RxBytesPerSec < 900 || got.Net.RxBytesPerSec > 1100 ||
		got.Net.TxBytesPerSec < 1800 || got.Net.TxBytesPerSec > 2200 {
		t.Fatalf("net %+v", got.Net)
	}
}

func TestContainerWithoutLimitsUsesMachineSize(t *testing.T) {
	root := t.TempDir()
	write(t, root, "proc/meminfo", "MemTotal:       1000 kB\nMemAvailable:    500 kB\n")
	write(t, root, "sys/fs/cgroup/cpu.stat", "usage_usec 0\n")
	write(t, root, "sys/fs/cgroup/cpu.max", "max 100000\n")
	write(t, root, "sys/fs/cgroup/memory.current", "4096\n")
	write(t, root, "sys/fs/cgroup/memory.max", "max\n")

	got := newSampler("n", fakeSlots{0, 2}, root, 4).read(time.Now())
	if got.scope != ScopeContainer || got.cores != 4 || got.mem.LimitBytes != 1000*1024 {
		t.Fatalf("%+v mem %+v", got, got.mem)
	}
}

func TestHostScope(t *testing.T) {
	root := t.TempDir()
	write(t, root, "proc/meminfo", "MemTotal:       16000 kB\nMemFree: 1 kB\nMemAvailable:    4000 kB\n")
	write(t, root, "proc/stat", "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 1 1 1 1\n")

	start := time.Now()
	s := newSampler("node-1", fakeSlots{0, 2}, root, 4)
	// 500 of the next 1000 ticks are busy = half of 4 cores.
	write(t, root, "proc/stat", "cpu  400 0 300 1200 100 0 0 0 0 0\n")
	s.sample(start.Add(2 * time.Second))
	got := s.Snapshot()

	if got.Scope != ScopeHost || got.PIDs != nil || got.Net != nil {
		t.Fatalf("%+v", got)
	}
	if got.CPU == nil || got.CPU.CoresUsed != 2 || got.CPU.Percent != 50 || got.CPU.Cores != 4 {
		t.Fatalf("cpu %+v", got.CPU)
	}
	if got.Memory == nil || got.Memory.UsedBytes != 12000*1024 || got.Memory.LimitBytes != 16000*1024 {
		t.Fatalf("memory %+v", got.Memory)
	}
}

func TestNothingReadable(t *testing.T) {
	got := newSampler("n", fakeSlots{0, 1}, t.TempDir(), 2).Snapshot()
	if got.CPU != nil || got.Memory != nil || got.Net != nil || got.Slots.Total != 1 {
		t.Fatalf("%+v", got)
	}
}
