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
func openLogFile() *os.File {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".config", "ssh-tunnel-manager")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "app.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	return f
}
