// Package resources reports how loaded this node is: the figures
// `docker stats` shows for its container (CPU, memory, network, processes),
// read from the same cgroup v2 counters, plus the node's simulation slots.
//
// Inside a container the cgroup covers the node process and every Python
// simulator it spawns. Outside one (a bare `go run`) there is no such
// boundary, so the sampler falls back to whole-machine figures from /proc
// and says so in Snapshot.Scope.
package resources

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ScopeContainer = "container"
	ScopeHost      = "host"
)

// Slots is the node's simulation concurrency limit and how much is taken.
type Slots struct {
	InUse int `json:"in_use"`
	Total int `json:"total"`
}

// CPU is the usage averaged over the last sampling interval.
type CPU struct {
	CoresUsed float64 `json:"cores_used"` // 1.5 = one and a half cores busy
	Cores     float64 `json:"cores"`      // cores available (cgroup quota or machine)
	Percent   float64 `json:"percent"`    // cores_used / cores · 100
}

type Memory struct {
	UsedBytes  uint64 `json:"used_bytes"`
	LimitBytes uint64 `json:"limit_bytes"`
}

// Net is the transfer rate over the last sampling interval, all interfaces
// except loopback.
type Net struct {
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
}

// Snapshot is the body of GET /resources. CPU and Net are rates, so they are
// null until two readings exist; PIDs is null in host scope.
type Snapshot struct {
	NodeID    string    `json:"node_id"`
	Scope     string    `json:"scope"`
	Slots     Slots     `json:"slots"`
	CPU       *CPU      `json:"cpu"`
	Memory    *Memory   `json:"memory"`
	PIDs      *int      `json:"pids"`
	Net       *Net      `json:"net"`
	SampledAt time.Time `json:"sampled_at"`
}

// SlotCounter reports slot occupancy. Implemented by worker.Executor.
type SlotCounter interface {
	SlotsInUse() int
	SlotsTotal() int
}

// reading is one look at the raw counters.
type reading struct {
	at    time.Time
	scope string

	cpuOK   bool
	cpuUsec uint64 // container: cumulative CPU time
	cpuBusy uint64 // host: cumulative busy ticks
	cpuAll  uint64 // host: cumulative total ticks
	cores   float64

	mem  *Memory
	pids *int

	netOK  bool
	rx, tx uint64
}

// Sampler keeps the latest usage figures. Start it with Run.
type Sampler struct {
	nodeID string
	slots  SlotCounter
	root   string // filesystem root, "/" outside tests
	ncpu   int

	mu   sync.Mutex
	last *reading
	snap Snapshot
}

func NewSampler(nodeID string, slots SlotCounter) *Sampler {
	return newSampler(nodeID, slots, "/", runtime.NumCPU())
}

func newSampler(nodeID string, slots SlotCounter, root string, ncpu int) *Sampler {
	s := &Sampler{nodeID: nodeID, slots: slots, root: root, ncpu: ncpu}
	s.sample(time.Now())
	return s
}

// Run samples every interval until ctx is done.
func (s *Sampler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.sample(now)
		}
	}
}

// Snapshot returns the latest sample with live slot occupancy.
func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	snap := s.snap
	s.mu.Unlock()
	snap.Slots = Slots{InUse: s.slots.SlotsInUse(), Total: s.slots.SlotsTotal()}
	return snap
}

func (s *Sampler) sample(now time.Time) {
	cur := s.read(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{NodeID: s.nodeID, Scope: cur.scope, Memory: cur.mem, PIDs: cur.pids, SampledAt: now.UTC()}
	if prev := s.last; prev != nil && prev.scope == cur.scope {
		elapsed := cur.at.Sub(prev.at).Seconds()
		if cur.cpuOK && prev.cpuOK && elapsed > 0 {
			if used, ok := coresUsed(prev, &cur, elapsed); ok {
				snap.CPU = &CPU{CoresUsed: round(used, 3), Cores: cur.cores, Percent: round(used/cur.cores*100, 1)}
			}
		}
		if cur.netOK && prev.netOK && elapsed > 0 && cur.rx >= prev.rx && cur.tx >= prev.tx {
			snap.Net = &Net{
				RxBytesPerSec: round(float64(cur.rx-prev.rx)/elapsed, 0),
				TxBytesPerSec: round(float64(cur.tx-prev.tx)/elapsed, 0),
			}
		}
	}
	s.last, s.snap = &cur, snap
}

func coresUsed(prev, cur *reading, elapsed float64) (float64, bool) {
	if cur.cores <= 0 {
		return 0, false
	}
	if cur.scope == ScopeContainer {
		if cur.cpuUsec < prev.cpuUsec {
			return 0, false
		}
		return float64(cur.cpuUsec-prev.cpuUsec) / 1e6 / elapsed, true
	}
	if cur.cpuAll <= prev.cpuAll || cur.cpuBusy < prev.cpuBusy {
		return 0, false
	}
	return float64(cur.cpuBusy-prev.cpuBusy) / float64(cur.cpuAll-prev.cpuAll) * cur.cores, true
}

func (s *Sampler) read(now time.Time) reading {
	r := reading{at: now, scope: ScopeHost, cores: float64(s.ncpu)}
	cg := filepath.Join(s.root, "sys/fs/cgroup")
	usec, cpuOK := keyed(filepath.Join(cg, "cpu.stat"), "usage_usec")
	current, memOK := number(filepath.Join(cg, "memory.current"))
	hostTotal, hostAvail := s.meminfo()

	if cpuOK && memOK {
		r.scope, r.cpuOK, r.cpuUsec = ScopeContainer, true, usec
		// docker stats does not count reclaimable page cache as used.
		if inactive, ok := keyed(filepath.Join(cg, "memory.stat"), "inactive_file"); ok && inactive <= current {
			current -= inactive
		}
		limit, ok := number(filepath.Join(cg, "memory.max")) // "max" when unlimited
		if !ok || (hostTotal > 0 && limit > hostTotal) {
			limit = hostTotal
		}
		r.mem = &Memory{UsedBytes: current, LimitBytes: limit}
		if quota, ok := cpuQuota(filepath.Join(cg, "cpu.max")); ok && quota < r.cores {
			r.cores = quota
		}
		if n, ok := number(filepath.Join(cg, "pids.current")); ok {
			pids := int(n)
			r.pids = &pids
		}
	} else {
		r.cpuBusy, r.cpuAll, r.cpuOK = s.procStat()
		if hostTotal > 0 && hostAvail <= hostTotal {
			r.mem = &Memory{UsedBytes: hostTotal - hostAvail, LimitBytes: hostTotal}
		}
	}
	r.rx, r.tx, r.netOK = s.netDev()
	return r
}

// meminfo returns MemTotal and MemAvailable in bytes (0 when unreadable).
func (s *Sampler) meminfo() (total, available uint64) {
	path := filepath.Join(s.root, "proc/meminfo")
	t, _ := keyed(path, "MemTotal:")
	a, _ := keyed(path, "MemAvailable:")
	return t * 1024, a * 1024
}

// procStat returns cumulative busy and total CPU ticks for the machine.
func (s *Sampler) procStat() (busy, all uint64, ok bool) {
	data, err := os.ReadFile(filepath.Join(s.root, "proc/stat"))
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	f := strings.Fields(line)
	if len(f) < 6 || f[0] != "cpu" {
		return 0, 0, false
	}
	// user nice system idle iowait irq softirq steal; guest time is already in user.
	for i, v := range f[1:] {
		if i >= 8 {
			break
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		all += n
		if i != 3 && i != 4 {
			busy += n
		}
	}
	return busy, all, true
}

// netDev sums received and sent bytes over every interface except loopback.
func (s *Sampler) netDev() (rx, tx uint64, ok bool) {
	data, err := os.ReadFile(filepath.Join(s.root, "proc/net/dev"))
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64)
		t, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx, tx, ok = rx+r, tx+t, true
	}
	return rx, tx, ok
}

// number reads a file holding one unsigned integer.
func number(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return n, err == nil
}

// keyed reads the integer after `key` in a "key value" per line file.
func keyed(path, key string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == key {
			n, err := strconv.ParseUint(f[1], 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// cpuQuota parses cpu.max ("200000 100000" = 2 cores, "max 100000" = no limit).
func cpuQuota(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(data))
	if len(f) != 2 || f[0] == "max" {
		return 0, false
	}
	quota, err1 := strconv.ParseFloat(f[0], 64)
	period, err2 := strconv.ParseFloat(f[1], 64)
	if err1 != nil || err2 != nil || quota <= 0 || period <= 0 {
		return 0, false
	}
	return quota / period, true
}

func round(v float64, places int) float64 {
	p := 1.0
	for range places {
		p *= 10
	}
	if v < 0 {
		return 0
	}
	return float64(int64(v*p+0.5)) / p
}
