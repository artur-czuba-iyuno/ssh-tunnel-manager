package main

import (
	"context"
	"embed"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"ssh-tunnel-manager/internal/config"
	"ssh-tunnel-manager/internal/dns"
	"ssh-tunnel-manager/internal/prefs"
	"ssh-tunnel-manager/internal/updater"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	logWriter := io.Writer(os.Stderr)
	if logFile := openLogFile(); logFile != nil {
		defer logFile.Close()
		logWriter = io.MultiWriter(os.Stderr, logFile)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if handled, err := updater.HandleInstallHelper(os.Args[1:]); handled {
		if err != nil {
			slog.Error("privileged update install failed", "error", err)
			os.Exit(1)
		}
		return
	}

	// One-shot elevated Portless setup. Current macOS bundles use their separate
	// minimal helper; this main-binary path remains for older macOS plus the
	// Windows/Linux platform elevation wrappers.
	setupRequested := false
	setupRequirements := dns.SetupRequirements{}
	for _, arg := range os.Args[1:] {
		switch arg {
		case dns.SetupArg:
			setupRequested = true
		case dns.PrivilegedRedirectArg:
			setupRequirements.PrivilegedPortRedirect = true
		}
	}
	if setupRequested {
		if err := dns.RunSetup(setupRequirements); err != nil {
			slog.Error("portless system setup failed", "error", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	startHidden := false
	for _, arg := range os.Args[1:] {
		if arg == "--hidden" {
			startHidden = true
		}
	}

	store, err := config.NewStore()
	if err != nil {
		slog.Error("failed to initialize config store", "error", err)
		os.Exit(1)
	}

	prefsStore, err := prefs.NewStore()
	if err != nil {
		slog.Error("failed to initialize prefs store", "error", err)
		os.Exit(1)
	}

	app := NewApp(store, prefsStore, startHidden)

	p := prefsStore.Get()
	initWidth := p.WindowWidth
	initHeight := p.WindowHeight
	if initWidth < 400 {
		initWidth = 900
	}
	if initHeight < 300 {
		initHeight = 600
	}

	err = wails.Run(&options.App{
		Title:       "SSH Tunnel Manager",
		Width:       initWidth,
		Height:      initHeight,
		Frameless:   true,
		StartHidden: startHidden,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 10, G: 10, B: 10, A: 1},
		SingleInstanceLock: &options.SingleInstanceLock{
			// Stable and beta builds deliberately share one lock so switching
			// channels can never leave both variants running side by side.
			UniqueId: "com.wails.ssh-tunnel-manager",
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				app.showWindow()
			},
		},
		OnBeforeClose: func(ctx context.Context) bool {
			if app.forceQuit {
				return false // always allow quit from tray
			}
			if app.GetCloseToTray() {
				runtime.WindowHide(ctx)
				return true // cancel the close — hide instead
			}
			return false // allow quit
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		slog.Error("wails application error", "error", err)
		os.Exit(1)
	}
}

// openLogFile opens the persistent app log at
// ~/.config/ssh-tunnel-manager/app.log, appending across runs, so startup and
// Portless diagnostics survive even when the process is launched by a
// LaunchAgent/systemd unit that discards stderr. Logging still works (to
// stderr only) when this fails; nothing else depends on the file existing.
//
// The log can contain SSH destinations, ProxyCommand strings, and forwarded
// proxy stderr, so the directory and file are kept private (0700/0600) like
// the existing prefs/config stores — mode is enforced with an explicit
// chmod rather than trusted to MkdirAll/OpenFile, since neither changes the
// mode of a path that already existed with looser permissions.
func openLogFile() *os.File {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return openLogFileAt(filepath.Join(home, ".config", "ssh-tunnel-manager"))
}

// openLogFileAt does the actual work for openLogFile against an explicit
// directory, so it's testable without touching the real user's home.
func openLogFileAt(dir string) *os.File {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil
	}
	path := filepath.Join(dir, "app.log")
	rotateLogFileIfLarge(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil
	}
	return f
}

// maxLogFileSize bounds app.log so a tunnel stuck retrying forever (every
// attempt logged, see internal/ssh.Manager) can't grow it without limit.
const maxLogFileSize = 5 * 1024 * 1024 // 5 MiB

// rotateLogFileIfLarge renames an oversized log out of the way, keeping one
// previous generation, before a fresh one is opened at startup. Best-effort:
// any error here just means the next open appends to the existing file.
func rotateLogFileIfLarge(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxLogFileSize {
		return
	}
	_ = os.Rename(path, path+".old")
}
