package dns

import "testing"

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
