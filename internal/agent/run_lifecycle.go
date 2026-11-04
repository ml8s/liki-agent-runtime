package agent

import "sync"

// runLifecycleState is the process-local execution state. It does not replace
// audit facts; it prevents a lifecycle identity from being reused or closed by
// two call paths at the same time.
type runLifecycleState int

const (
	runLifecycleActive runLifecycleState = iota
	runLifecycleClosing
	runLifecyclePendingClose
	runLifecycleCompleted
)

// runExecutionManager is the single state machine for a RunExecution identity.
// Transition contract:
//
//	active   -> closing    claimClose
//	closing  -> pending    failClose    (terminal append failed)
//	pending  -> closing    claimClose   (retry terminal append)
//	closing  -> completed  finish       (terminal evidence is durable)
//	active   -> completed  abort        (identity rejected before started evidence)
//
// The completed ring is retained only to reject duplicate RunIDs after a
// bounded process restart window; durable reservations own cross-restart
// exclusivity in production.
type runExecutionManager struct {
	mu         sync.Mutex
	states     map[string]runLifecycleState
	completed  map[string]struct{}
	order      []string
	orderStart int
}

func newRunExecutionManager() *runExecutionManager {
	return &runExecutionManager{
		states:    make(map[string]runLifecycleState),
		completed: make(map[string]struct{}),
	}
}

// begin reserves a new active lifecycle. It fails for any existing state.
func (m *runExecutionManager) begin(runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.states[runID]; exists {
		return false
	}
	if _, exists := m.completed[runID]; exists {
		return false
	}
	m.states[runID] = runLifecycleActive
	return true
}

// claimClose claims terminal closure and reports whether the identity was
// previously pending, so callers can adjust the pending gauge exactly once.
// Pending runs may retry; a false result means another caller owns the
// closing transition.
func (m *runExecutionManager) claimClose(runID string) (claimed, wasPending bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.states[runID] {
	case runLifecycleActive, runLifecyclePendingClose:
		wasPending = m.states[runID] == runLifecyclePendingClose
		return m.transitionLocked(runID, runLifecycleClosing), wasPending
	default:
		return false, false
	}
}

// abort retires an identity that was rejected before a started fact became
// durable. The completed set keeps it non-reusable for this process; production
// also owns a durable reservation.
func (m *runExecutionManager) abort(runID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.states[runID] != runLifecycleActive {
		return
	}
	if !m.transitionLocked(runID, runLifecycleCompleted) {
		return
	}
	m.completed[runID] = struct{}{}
	m.order = append(m.order, runID)
	for len(m.order)-m.orderStart > maxCompletedRunIDs {
		delete(m.completed, m.order[m.orderStart])
		m.order[m.orderStart] = ""
		m.orderStart++
	}
	if m.orderStart == len(m.order) {
		m.order = m.order[:0]
		m.orderStart = 0
	}
}

// failClose marks a failed terminal transaction as pending retry. The bool
// result lets callers increment a pending gauge exactly once per identity.
func (m *runExecutionManager) failClose(runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.states[runID] != runLifecycleClosing {
		return false
	}
	return m.transitionLocked(runID, runLifecyclePendingClose)
}

// finish marks terminal evidence complete. A closing claim is required so
// process state cannot be released concurrently with another terminal path.
func (m *runExecutionManager) finish(runID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.states[runID] != runLifecycleClosing {
		return
	}
	if !m.transitionLocked(runID, runLifecycleCompleted) {
		return
	}
	m.completed[runID] = struct{}{}
	m.order = append(m.order, runID)
	// Ring eviction keeps duplicate-run detection bounded without repeatedly
	// reslicing the backing array, which would grow forever.
	for len(m.order)-m.orderStart > maxCompletedRunIDs {
		delete(m.completed, m.order[m.orderStart])
		m.order[m.orderStart] = ""
		m.orderStart++
	}
	if m.orderStart == len(m.order) {
		m.order = m.order[:0]
		m.orderStart = 0
	}
}

// transitionLocked owns every state change. Ring bookkeeping for completed
// runs is intentionally handled by abort and finish, because eviction must
// not remove a newly completed identity.
func (m *runExecutionManager) transitionLocked(runID string, next runLifecycleState) bool {
	if _, exists := m.states[runID]; !exists {
		return false
	}
	m.states[runID] = next
	return true
}
