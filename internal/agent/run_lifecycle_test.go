package agent

import "testing"

func TestRunExecutionManagerRejectsDuplicateIdentities(t *testing.T) {
	runs := newRunExecutionManager()
	const runID = "run_lifecycle"
	if !runs.begin(runID) {
		t.Fatal("begin(new run) = false, want true")
	}
	if runs.begin(runID) {
		t.Fatal("begin(active run) = true, want false")
	}
	runs.abort(runID)
	if runs.begin(runID) {
		t.Fatal("begin(aborted run) = true, want false")
	}
}

func TestRunExecutionManagerRequiresExclusiveCloseClaim(t *testing.T) {
	runs := newRunExecutionManager()
	const runID = "run_close_claim"
	runs.begin(runID)
	if claimed, _ := runs.claimClose(runID); !claimed {
		t.Fatal("claimClose(active run) = false, want true")
	}
	if claimed, _ := runs.claimClose(runID); claimed {
		t.Fatal("claimClose(closing run) = true, want false")
	}
	runs.finish(runID)
	if claimed, _ := runs.claimClose(runID); claimed {
		t.Fatal("claimClose(completed run) = true, want false")
	}
}

func TestRunExecutionManagerMarksFailedClosurePending(t *testing.T) {
	runs := newRunExecutionManager()
	const runID = "run_close_retry"
	runs.begin(runID)
	if claimed, _ := runs.claimClose(runID); !claimed {
		t.Fatal("claimClose(active run) = false, want true")
	}
	if !runs.failClose(runID) {
		t.Fatal("failClose(closing run) = false, want true")
	}
	if claimed, _ := runs.claimClose(runID); !claimed {
		t.Fatal("claimClose(pending run) = false, want true")
	}
	runs.finish(runID)
	if runs.failClose(runID) {
		t.Fatal("failClose(completed run) = true, want false")
	}
}

func TestRunExecutionManagerFinishRequiresClosingClaim(t *testing.T) {
	runs := newRunExecutionManager()
	const runID = "run_finish_claim"
	runs.begin(runID)
	runs.finish(runID)
	if claimed, _ := runs.claimClose(runID); !claimed {
		t.Fatal("claimClose(run after invalid finish) = false, want lifecycle to remain active")
	}
	runs.finish(runID)
}
