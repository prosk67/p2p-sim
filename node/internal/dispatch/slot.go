package dispatch

import "sync"

// CoordinatorSlot admits one coordinated batch per node at a time, whatever
// its kind (M/M/1 or network), so concurrent batches do not distort each
// other's timing measurements. The zero value is a free slot.
type CoordinatorSlot struct {
	mu      sync.Mutex
	running string
}

// TryAcquire claims the slot for batchID. If another batch holds it, it
// returns that batch's id and false.
func (s *CoordinatorSlot) TryAcquire(batchID string) (holder string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running != "" {
		return s.running, false
	}
	s.running = batchID
	return batchID, true
}

// Release frees the slot if batchID holds it.
func (s *CoordinatorSlot) Release(batchID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == batchID {
		s.running = ""
	}
}
