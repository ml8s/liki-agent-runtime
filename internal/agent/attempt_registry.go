package agent

import "sync"

// attemptEntry is the single registration fact for one externally driven
// audit attempt. Every derived lookup index references entries created here.
type attemptEntry struct {
	scope     *runExecution
	contextID string
	runID     string
}

// attemptRegistry owns the mapping between externally driven audit attempts,
// their run identities, their protocol context, and the ADK invocation
// bridge. byAttempt is the source of truth; byRun, byContext, and
// byInvocation are derived lookup indexes maintained only by this type.
//
// Locking contract: registry.mu is never held while acquiring scope.mu (and
// vice versa). Entries are published only after their scope fields are
// immutable, so the registry never mutates a registered scope.
type attemptRegistry struct {
	mu           sync.Mutex
	byAttempt    map[string]*attemptEntry
	byRun        map[string]string              // runID -> attemptID
	byContext    map[string]map[string]struct{} // contextID -> attemptIDs
	byInvocation map[string]string              // sessionID\x00invocationID -> attemptID
}

func newAttemptRegistry() *attemptRegistry {
	return &attemptRegistry{
		byAttempt:    make(map[string]*attemptEntry),
		byRun:        make(map[string]string),
		byContext:    make(map[string]map[string]struct{}),
		byInvocation: make(map[string]string),
	}
}

// begin registers one attempt. It fails closed when either the attempt or the
// run identity is already active, so one execution aggregate can never gain a
// second audit scope.
func (r *attemptRegistry) begin(attemptID, runID, contextID string, scope *runExecution) bool {
	if attemptID == "" || runID == "" || scope == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byAttempt[attemptID]; exists {
		return false
	}
	if _, exists := r.byRun[runID]; exists && attemptID != runID {
		return false
	}
	r.byAttempt[attemptID] = &attemptEntry{scope: scope, contextID: contextID, runID: runID}
	r.byRun[runID] = attemptID
	if contextID != "" {
		attempts := r.byContext[contextID]
		if attempts == nil {
			attempts = make(map[string]struct{})
			r.byContext[contextID] = attempts
		}
		attempts[attemptID] = struct{}{}
	}
	return true
}

// lookup resolves a scope by attempt ID, then by run identity, then by a
// context that owns exactly one active attempt. Ambiguous contexts resolve to
// no scope instead of guessing.
func (r *attemptRegistry) lookup(key string) (*runExecution, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, exists := r.byAttempt[key]; exists {
		return entry.scope, true
	}
	if attemptID, exists := r.byRun[key]; exists {
		entry, ok := r.byAttempt[attemptID]
		return entry.scope, ok
	}
	attempts := r.byContext[key]
	if len(attempts) == 1 {
		for attemptID := range attempts {
			entry, ok := r.byAttempt[attemptID]
			return entry.scope, ok
		}
	}
	return nil, false
}

// bindInvocation bridges one ADK invocation to its owning attempt. The bridge
// is derived state: it disappears with the attempt.
func (r *attemptRegistry) bindInvocation(sessionID, invocationID, attemptID string) {
	if sessionID == "" || invocationID == "" || attemptID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byInvocation[invocationKey(sessionID, invocationID)] = attemptID
}

// attemptForInvocation resolves the attempt that owns an ADK invocation.
func (r *attemptRegistry) attemptForInvocation(sessionID, invocationID string) (string, bool) {
	if sessionID == "" || invocationID == "" {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	attemptID, exists := r.byInvocation[invocationKey(sessionID, invocationID)]
	return attemptID, exists && attemptID != ""
}

// unbindInvocations drops only the ADK invocation bridges of one attempt. The
// attempt registration survives: an A2A context may run further invocations,
// and its audit scope is released only by unbindAttempt/forgetContext.
func (r *attemptRegistry) unbindInvocations(attemptID string) {
	if attemptID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, bound := range r.byInvocation {
		if bound == attemptID {
			delete(r.byInvocation, key)
		}
	}
}

// unbindAttempt removes one attempt and every derived index entry, including
// invocation bridges registered for it.
func (r *attemptRegistry) unbindAttempt(attemptID string) {
	if attemptID == "" {
		return
	}
	r.mu.Lock()
	entry, exists := r.byAttempt[attemptID]
	if !exists {
		r.mu.Unlock()
		return
	}
	delete(r.byAttempt, attemptID)
	if current, exists := r.byRun[entry.runID]; exists && current == attemptID {
		delete(r.byRun, entry.runID)
	}
	if attempts := r.byContext[entry.contextID]; attempts != nil {
		delete(attempts, attemptID)
		if len(attempts) == 0 {
			delete(r.byContext, entry.contextID)
		}
	}
	for key, bound := range r.byInvocation {
		if bound == attemptID {
			delete(r.byInvocation, key)
		}
	}
	r.mu.Unlock()
}

// forgetContext removes every attempt owned by one protocol context. Contexts
// own attempts; the reverse removal never happens implicitly.
func (r *attemptRegistry) forgetContext(contextID string) {
	if contextID == "" {
		return
	}
	r.mu.Lock()
	attempts := r.byContext[contextID]
	delete(r.byContext, contextID)
	r.mu.Unlock()
	for attemptID := range attempts {
		r.unbindAttempt(attemptID)
	}
}

func invocationKey(sessionID, invocationID string) string {
	return sessionID + "\x00" + invocationID
}
