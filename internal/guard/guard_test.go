package guard

import (
	"testing"

	"github.com/Golden-forest/claude-sentinel/internal/binding"
	"github.com/Golden-forest/claude-sentinel/internal/machine"
)

func openInc() *machine.Incident {
	return &machine.Incident{ID: "i1", SessionID: "s1", State: machine.StateVerifying, ResetAt: 100}
}

func allGood() Evidence {
	return Evidence{
		IncidentOpen:           true,
		BindingAlive:           true,
		PaneExists:             true,
		PaneInCopyMode:         false,
		ResetAtPassed:          true,
		NativeResumed:          false,
		NewerTranscriptActivity: false,
	}
}

func liveBinding() *binding.Binding {
	return &binding.Binding{SessionID: "s1", PaneID: "%1", Pid: 1, StartedAt: 1}
}

func TestCheck_AllGatesPass(t *testing.T) {
	if v := Check(openInc(), liveBinding(), allGood()); v != VerdictProceed {
		t.Fatalf("all gates pass → PROCEED, got %v", v)
	}
}

func TestCheck_NoBindingNeedsReview(t *testing.T) {
	if v := Check(openInc(), nil, allGood()); v != VerdictNeedsReview {
		t.Fatalf("nil binding → NEEDS_REVIEW, got %v", v)
	}
}

func TestCheck_BindingDeadNeedsReview(t *testing.T) {
	ev := allGood()
	ev.BindingAlive = false
	if v := Check(openInc(), liveBinding(), ev); v != VerdictNeedsReview {
		t.Fatalf("dead binding → NEEDS_REVIEW, got %v", v)
	}
}

func TestCheck_CopyModeNeedsReview(t *testing.T) {
	ev := allGood()
	ev.PaneInCopyMode = true
	if v := Check(openInc(), liveBinding(), ev); v != VerdictNeedsReview {
		t.Fatalf("copy-mode → NEEDS_REVIEW, got %v", v)
	}
}

func TestCheck_PaneMissingNeedsReview(t *testing.T) {
	ev := allGood()
	ev.PaneExists = false
	if v := Check(openInc(), liveBinding(), ev); v != VerdictNeedsReview {
		t.Fatalf("pane gone → NEEDS_REVIEW, got %v", v)
	}
}

func TestCheck_ResetNotPassedNeedsReview(t *testing.T) {
	ev := allGood()
	ev.ResetAtPassed = false
	if v := Check(openInc(), liveBinding(), ev); v != VerdictNeedsReview {
		t.Fatalf("reset not passed → NEEDS_REVIEW, got %v", v)
	}
}

func TestCheck_AlreadyResumed(t *testing.T) {
	for name, mut := range map[string]func(*Evidence){
		"native":   func(e *Evidence) { e.NativeResumed = true },
		"activity": func(e *Evidence) { e.NewerTranscriptActivity = true },
	} {
		ev := allGood()
		mut(&ev)
		if v := Check(openInc(), liveBinding(), ev); v != VerdictResolved {
			t.Fatalf("%s resumed → RESOLVED, got %v", name, v)
		}
	}
}

func TestCheck_CancelledIncident(t *testing.T) {
	inc := openInc()
	inc.State = machine.StateCancelled
	if v := Check(inc, liveBinding(), allGood()); v != VerdictCancelled {
		t.Fatalf("cancelled → CANCELLED, got %v", v)
	}
}

func TestCheck_ResolvedIncident(t *testing.T) {
	inc := openInc()
	inc.State = machine.StateResolved
	if v := Check(inc, liveBinding(), allGood()); v != VerdictResolved {
		t.Fatalf("resolved state → RESOLVED, got %v", v)
	}
}
