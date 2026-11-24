// Package resources reports how loaded this node is: the figures
// `docker stats` shows for its container (CPU, memory, network, processes),
// read from the same cgroup v2 counters, plus the node's simulation slots.
//
// Inside a container the cgroup covers the node process and every Python
// simulator it spawns; both cgroup v2 and v1 layouts are read. Outside one
// (a bare `go run`) there is no such boundary, so the sampler falls back to
// whole-machine figures from /proc and says so in Snapshot.Scope.
//
// HostID tells the GUI which nodes share a machine, so pool-wide CPU is not
// double counted: containers on one host see the same kernel boot id.
package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
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
	NodeID string `json:"node_id"`
	Scope  string `json:"scope"`
	// HostID is the same for every node on one machine (a hash of the
	// kernel boot id; it identifies nothing outside this pool view).
	HostID string `json:"host_id,omitempty"`
	// HostCores is the machine's CPU count, whatever the node's own limit.
	HostCores float64   `json:"host_cores"`
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

	cpuOK     bool
	cpuUsec   uint64  // container: cumulative CPU time
	cpuBusy   uint64  // host: cumulative busy ticks
	cpuAll    uint64  // host: cumulative total ticks
	cores     float64 // capacity available to this node
	hostCores float64 // CPUs on the machine

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
	ncpu   int    // CPUs this process may use (runtime.NumCPU)
	hostID string

	mu   sync.Mutex
	last *reading
	snap Snapshot
}

func NewSampler(nodeID string, slots SlotCounter) *Sampler {
	return newSampler(nodeID, slots, "/", runtime.NumCPU())
}

func newSampler(nodeID string, slots SlotCounter, root string, ncpu int) *Sampler {
	s := &Sampler{nodeID: nodeID, slots: slots, root: root, ncpu: ncpu}
	if data, err := os.ReadFile(filepath.Join(root, "proc/sys/kernel/random/boot_id")); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			sum := sha256.Sum256([]byte("p2p-sim host:" + id))
			s.hostID = hex.EncodeToString(sum[:6])
		}
	}
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
	snap := Snapshot{NodeID: s.nodeID, Scope: cur.scope, HostID: s.hostID, HostCores: cur.hostCores,
		Memory: cur.mem, PIDs: cur.pids, SampledAt: now.UTC()}
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
	hostCores := float64(s.machineCPUs())
	r := reading{at: now, scope: ScopeHost, cores: hostCores, hostCores: hostCores}
	hostTotal, hostAvail := s.meminfo()

	if cg, ok := s.cgroup(hostTotal); ok {
		r.scope, r.cpuOK, r.cpuUsec, r.mem, r.pids = ScopeContainer, true, cg.cpuUsec, cg.mem, cg.pids
		// A container may use at most its CPU quota and the CPUs it is pinned to.
		r.cores = math.Min(hostCores, float64(s.ncpu))
		if cg.quota > 0 && cg.quota < r.cores {
			r.cores = cg.quota
		}
	} else {
		r.cpuBusy, r.cpuAll, r.cpuOK = s.procStat()
		if hostTotal > 0 && hostAvail <= hostTotal {
			r.mem = &Memory{UsedBytes: hostTotal - hostAvail, LimitBytes: hostTotal}
		}
	}
	r.rx, r.tx, r.netOK = s.netDev(r.scope)
	return r
}

// cgroupReading is one look at the node's own container cgroup.
type cgroupReading struct {
	cpuUsec uint64
	mem     *Memory
	pids    *int
	quota   float64 // cores; 0 when unlimited
}

// cgroup reads the container's cgroup, v2 first, then v1. ok is false when
// the node is not in a container (the files then describe the whole host).
func (s *Sampler) cgroup(hostTotal uint64) (cgroupReading, bool) {
	var out cgroupReading
	capLimit := func(limit uint64, ok bool) uint64 {
		if !ok || (hostTotal > 0 && limit > hostTotal) {
			return hostTotal // "max" or a huge v1 sentinel: unlimited
		}
		return limit
	}
	setPIDs := func(path string) {
		if n, ok := number(path); ok {
			pids := int(n)
			out.pids = &pids
		}
	}
	cg := filepath.Join(s.root, "sys/fs/cgroup")

	// cgroup v2. The root cgroup has cpu.stat but no memory.current, so a
	// bare host never matches.
	if usec, ok := keyed(filepath.Join(cg, "cpu.stat"), "usage_usec"); ok {
		if current, ok := number(filepath.Join(cg, "memory.current")); ok {
			// docker stats does not count reclaimable page cache as used.
			if inactive, ok := keyed(filepath.Join(cg, "memory.stat"), "inactive_file"); ok && inactive <= current {
				current -= inactive
			}
			out.cpuUsec = usec
			out.mem = &Memory{UsedBytes: current, LimitBytes: capLimit(number(filepath.Join(cg, "memory.max")))}
			out.quota, _ = cpuQuota(filepath.Join(cg, "cpu.max"))
			setPIDs(filepath.Join(cg, "pids.current"))
			return out, true
		}
	}

	// cgroup v1. Its files exist at the root on a bare v1 host too, so also
	// require evidence of a container.
	if !s.inContainer() {
		return out, false
	}
	cpuDir := firstDir(cg, "cpu,cpuacct", "cpuacct,cpu", "cpuacct")
	nsec, ok := number(filepath.Join(cpuDir, "cpuacct.usage"))
	current, memOK := number(filepath.Join(cg, "memory/memory.usage_in_bytes"))
	if cpuDir == "" || !ok || !memOK {
		return out, false
	}
	if inactive, ok := keyed(filepath.Join(cg, "memory/memory.stat"), "total_inactive_file"); ok && inactive <= current {
		current -= inactive
	}
	out.cpuUsec = nsec / 1000
	out.mem = &Memory{UsedBytes: current, LimitBytes: capLimit(number(filepath.Join(cg, "memory/memory.limit_in_bytes")))}
	quotaDir := firstDir(cg, "cpu,cpuacct", "cpuacct,cpu", "cpu")
	q, qok := signedNumber(filepath.Join(quotaDir, "cpu.cfs_quota_us"))
	p, pok := signedNumber(filepath.Join(quotaDir, "cpu.cfs_period_us"))
	if qok && pok && q > 0 && p > 0 {
		out.quota = float64(q) / float64(p)
	}
	setPIDs(filepath.Join(cg, "pids/pids.current"))
	return out, true
}

// inContainer looks for the usual container markers.
func (s *Sampler) inContainer() bool {
	for _, marker := range []string{".dockerenv", "run/.containerenv"} {
		if _, err := os.Stat(filepath.Join(s.root, marker)); err == nil {
			return true
		}
	}
	data, err := os.ReadFile(filepath.Join(s.root, "proc/1/cgroup"))
	if err != nil {
		return false
	}
	text := string(data)
	for _, hint := range []string{"docker", "kubepods", "containerd", "libpod", "lxc"} {
		if strings.Contains(text, hint) {
			return true
		}
	}
	return false
}

// machineCPUs counts the machine's CPUs from /proc/stat, which lists every
// CPU even when this process is pinned to a few (runtime.NumCPU is not).
func (s *Sampler) machineCPUs() int {
	data, err := os.ReadFile(filepath.Join(s.root, "proc/stat"))
	if err == nil {
		n := 0
		for _, line := range strings.Split(string(data), "\n") {
			if len(line) > 3 && strings.HasPrefix(line, "cpu") && line[3] >= '0' && line[3] <= '9' {
				n++
			}
		}
		if n > 0 {
			return n
		}
	}
	return s.ncpu
}

func firstDir(base string, names ...string) string {
	for _, name := range names {
		dir := filepath.Join(base, name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
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

// virtualPrefixes are host interfaces that carry traffic already counted on a
// physical one (bridges, veth pairs, VPN tunnels).
var virtualPrefixes = []string{"veth", "docker", "br-", "virbr", "tailscale", "tun", "tap", "wg", "cni", "flannel", "cali", "vxlan", "zt"}

// netDev sums received and sent bytes over the node's interfaces. Inside a
// container that is everything but loopback; on a bare host, virtual
// interfaces are skipped so one packet is not counted two or three times.
func (s *Sampler) netDev(scope string) (rx, tx uint64, ok bool) {
	data, err := os.ReadFile(filepath.Join(s.root, "proc/net/dev"))
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, found := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !found || name == "lo" || (scope == ScopeHost && isVirtual(name)) {
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

func isVirtual(name string) bool {
	for _, prefix := range virtualPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// signedNumber reads a file holding one integer (v1 uses -1 for "no limit").
func signedNumber(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n, err == nil
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
