package dns

import (
	"errors"
	"testing"
)

func TestClassifyPersistenceFailureDistinguishesApprovedService(t *testing.T) {
	message, recommendation, command := classifyPersistenceFailure(SystemServiceStatus{
		Available: true,
		Installed: true,
		State:     "enabled",
	})
	if recommendation == "" || command == "" {
		t.Fatalf("an approved-but-ineffective service should return an actionable recommendation and command, got message=%q recommendation=%q command=%q", message, recommendation, command)
	}
}

func TestClassifyPersistenceFailureFallsBackWhenNotApproved(t *testing.T) {
	for _, status := range []SystemServiceStatus{
		{}, // unsupported platform / never registered
		{Available: true, Installed: true, State: "approval-required"},
		{Available: true, Installed: false, State: "not-installed"},
	} {
		_, recommendation, command := classifyPersistenceFailure(status)
		if recommendation != "" || command != "" {
			t.Fatalf("status %+v should not get the approved-service recommendation, got recommendation=%q command=%q", status, recommendation, command)
		}
	}
}

func TestClassifySetupOutcomePrefersPersistenceErrorWhenApproved(t *testing.T) {
	// Reproduces ensurePortlessService's own timeout path (service_darwin.go):
	// it returns a plain, non-nil error while the service is still enabled.
	// classifySetupOutcome must reclassify that error rather than pass it
	// through, or the stale-registration case this package exists to
	// diagnose never reaches the fallback banner.
	setupErr := errors.New("Portless system service is approved but did not finish setup")
	status := SystemServiceStatus{Available: true, Installed: true, State: "enabled"}

	err := classifySetupOutcome(setupErr, status)

	var persistErr *PersistenceError
	if !errors.As(err, &persistErr) {
		t.Fatalf("classifySetupOutcome() = %v, want a *PersistenceError even though setupErr was non-nil", err)
	}
	if persistErr.Recommendation == "" || persistErr.Command == "" {
		t.Fatalf("PersistenceError = %+v, want a populated recommendation and command", persistErr)
	}
}

func TestClassifySetupOutcomePreservesSetupErrWhenNotApproved(t *testing.T) {
	setupErr := errors.New("Portless system service needs approval in System Settings")
	status := SystemServiceStatus{Available: true, Installed: true, State: "approval-required"}

	err := classifySetupOutcome(setupErr, status)

	if !errors.Is(err, setupErr) {
		t.Fatalf("classifySetupOutcome() = %v, want the original setupErr preserved unchanged", err)
	}
}

func TestClassifySetupOutcomeFallsBackToGenericMessageWhenSilent(t *testing.T) {
	err := classifySetupOutcome(nil, SystemServiceStatus{})

	var persistErr *PersistenceError
	if errors.As(err, &persistErr) {
		t.Fatal("an unregistered/unsupported status must not produce an actionable PersistenceError")
	}
	if err == nil {
		t.Fatal("expected a non-nil error when the system is still unconfigured")
	}
}
