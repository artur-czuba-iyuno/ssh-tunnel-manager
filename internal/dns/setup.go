package dns

import (
	"context"
	"fmt"
	"log/slog"
	"os"
)

// SetupArg is the CLI flag main.go inspects to determine that the process was
// relaunched with elevated privileges purely to perform Portless system setup.
const SetupArg = "--setup-dns"

// CleanupArg selects the explicit, one-shot privileged removal path used when
// the user uninstalls the macOS Portless system service from Settings.
const CleanupArg = "--cleanup-dns"

// PrivilegedRedirectArg tells the elevated macOS helper to install the narrow
// PF redirect used by Portless public ports below 1024.
const PrivilegedRedirectArg = "--setup-privileged-port-redirect"

// SetupRequirements describes which machine-wide Portless prerequisites a
// tunnel needs before its listeners can start.
type SetupRequirements struct {
	PrivilegedPortRedirect bool
}

// IsSystemConfigured reports whether the OS-level resolver and all requested
// platform prerequisites are ready. Platforms differ in how they record this
// state (files on macOS/Linux, registry/markers on Windows).
func IsSystemConfigured(requirements SetupRequirements) bool {
	return isSystemConfigured() &&
		isSetupPersistenceConfigured() &&
		(!requirements.PrivilegedPortRedirect || isPrivilegedPortRedirectConfigured())
}

// EnsureSystemConfigured runs the platform-specific setup, prompting the user
// for admin privileges (UAC / sudo / pkexec) if necessary. Returns nil if all
// requested prerequisites are already configured or setup completed.
//
// Windows, Linux, and legacy macOS builds relaunch the current executable via
// their elevation wrapper. Current signed macOS bundles instead register the
// minimal bundled LaunchDaemon through SMAppService.
func EnsureSystemConfigured(ctx context.Context, requirements SetupRequirements) error {
	if IsSystemConfigured(requirements) {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving current executable: %w", err)
	}
	args := []string{SetupArg}
	if requirements.PrivilegedPortRedirect {
		args = append(args, PrivilegedRedirectArg)
	}
	slog.Info("portless: launching elevated system setup", "exe", exe,
		"privilegedPortRedirect", requirements.PrivilegedPortRedirect)
	if err := runElevatedSetup(ctx, exe, args); err != nil {
		return err
	}
	if !IsSystemConfigured(requirements) {
		message, recommendation, command := classifyPersistenceFailure(GetSystemServiceStatus())
		return &PersistenceError{Message: message, Recommendation: recommendation, Command: command}
	}
	return nil
}

// PersistenceError indicates that an elevated setup attempt returned no Go
// error, yet the requested prerequisites still aren't observably in effect.
// Recommendation and Command are populated when classifyPersistenceFailure
// can point at a specific, actionable cause instead of a generic cancelled
// admin prompt.
type PersistenceError struct {
	Message        string
	Recommendation string
	Command        string
}

func (e *PersistenceError) Error() string { return e.Message }

// classifyPersistenceFailure explains why the requested prerequisites are
// still missing after a setup attempt that itself reported no error.
//
// When the OS already considers the helper service registered and approved,
// the missing effect is almost never a cancelled admin prompt — approval
// already happened. On macOS this combination is the signature of
// Background Task Management losing track of the app bundle after an
// in-place reinstall: it keeps reporting the service "enabled" while
// launchd silently never loads the job, so the network prerequisites
// (resolver, loopback pool) never get applied no matter how many times
// setup is retried.
func classifyPersistenceFailure(status SystemServiceStatus) (message, recommendation, command string) {
	if status.Available && status.Installed && status.State == "enabled" {
		return "The Portless helper service is approved, but its network prerequisites never took effect — the OS lost track of how to actually load it.",
			"Quit the app, delete /Applications/SSH Tunnel Manager.app, reboot, then reinstall a fresh copy — this usually restores the OS's link to the app bundle. If it doesn't, resetting background task approvals works but wipes every background-item approval on this Mac, requiring you to re-approve each one.",
			"sudo sfltool resetbtm"
	}
	return "Portless system setup did not persist — admin prompt was likely cancelled.", "", ""
}

// RunSetup performs the actual privileged setup work and is intended to be
// called from main() when the --setup-dns flag is present. The process exits
// after RunSetup returns.
func RunSetup(requirements SetupRequirements) error {
	return doSetup(requirements)
}

// RunCleanup removes machine-wide Portless state. It is intentionally exposed
// only through the explicit service-uninstall flow; normal app startup never
// invokes it.
func RunCleanup() error {
	return doCleanup()
}
