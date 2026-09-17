package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"ssh-tunnel-manager/internal/autostart"
	"ssh-tunnel-manager/internal/config"
	"ssh-tunnel-manager/internal/dns"
	"ssh-tunnel-manager/internal/keychain"
	"ssh-tunnel-manager/internal/netcap"
	"ssh-tunnel-manager/internal/portless"
	"ssh-tunnel-manager/internal/prefs"
	"ssh-tunnel-manager/internal/ssh"
	"ssh-tunnel-manager/internal/sshconfig"
	"ssh-tunnel-manager/internal/sysstats"
	"ssh-tunnel-manager/internal/tray"
	"ssh-tunnel-manager/internal/updater"
)

// App is the Wails application struct, bridging Go backend and frontend.
type App struct {
	ctx         context.Context
	store       *config.Store
	prefs       *prefs.Store
	manager     *ssh.Manager
	termMgr     *ssh.TerminalManager
	sftpMgr     *ssh.SFTPManager
	tray        *tray.Tray
	startHidden bool
	forceQuit   bool

	// Passphrase prompt coordination
	passphraseMu   sync.Mutex
	passphraseChan chan string

	// Pending update info (nil if no update available)
	updateMu         sync.Mutex
	updateNotifyMu   sync.Mutex // serializes state changes with UI/tray notifications
	updateChannel    updater.Channel
	updateGeneration uint64
	pendingUpdate    *updater.UpdateInfo
	updateChecker    func(context.Context, string, string, updater.Channel) (*updater.UpdateInfo, error)

	// Per-tunnel log buffer (ring buffer, max 300 entries per tunnel)
	logMu  sync.Mutex
	logBuf map[string][]config.LogEntry

	// Per-tunnel previous CPU sample, so GetServerStats can compute CPU usage
	// percentage from the delta between successive polls.
	statsMu sync.Mutex
	lastCPU map[string]sysstats.CPUSample

	// Active SFTP transfers, keyed by transferID — supports multiple
	// concurrent transfers per session.
	sftpTransferMu sync.Mutex
	sftpTransfers  map[string]context.CancelFunc
	sftpNextXferID int

	// Portless DNS: registry of domain → loopback IP, and the embedded DNS
	// server. The server is started lazily on the first portless connect so
	// users who never opt in pay zero cost.
	dnsMu             sync.Mutex
	dnsRegistry       *dns.Registry
	dnsServer         *dns.Server
	sharedDNSRegistry sharedPortlessRegistry
	connectSharedDNS  func() (sharedPortlessRegistry, error)
	// portlessFallback persists the current degradation so the frontend can
	// recover it even when auto-connect emitted the first event before mount.
	portlessFallback *PortlessFallbackStatus
	// portlessSetupErr caches a confirmed *dns.PersistenceError for this
	// process's lifetime, so auto-connecting several portless tunnels only
	// repeats the elevated setup dance (and any admin prompt) once instead
	// of once per tunnel — EnsureSystemConfigured has no cheaper way to
	// tell "still broken" from "never tried" than actually attempting it.
	portlessSetupErr *dns.PersistenceError
}

type sharedPortlessRegistry interface {
	dns.ForwardRegistry
	Close() error
}

// PortlessFallbackStatus describes the active localhost fallback used when
// Portless is degraded — either another process owns the embedded DNS
// address, or the machine-wide setup never took effect. Recommendation and
// Command are only populated for the latter, where the cause is specific
// enough to hand the user an actual fix instead of just "it's broken".
type PortlessFallbackStatus struct {
	TunnelID       string `json:"tunnelId"`
	Message        string `json:"message"`
	Recommendation string `json:"recommendation,omitempty"`
	Command        string `json:"command,omitempty"`
}

// PortlessServiceStatus describes the machine-wide macOS helper without
// exposing platform-specific implementation details to the frontend.
type PortlessServiceStatus struct {
	Available        bool   `json:"available"`
	Installed        bool   `json:"installed"`
	ApprovalRequired bool   `json:"approvalRequired"`
	Current          bool   `json:"current"`
	State            string `json:"state"`
	Message          string `json:"message"`
}

// NewApp creates a new App instance.
func NewApp(store *config.Store, prefsStore *prefs.Store, startHidden bool) *App {
	registry := dns.NewRegistry()
	updateChannel := updater.ChannelStable
	if configured, err := updater.ParseChannel(prefsStore.Get().UpdateChannel); err == nil {
		updateChannel = configured
	}
	app := &App{
		store:         store,
		prefs:         prefsStore,
		startHidden:   startHidden,
		updateChannel: updateChannel,
		updateChecker: updater.Check,
		logBuf:        make(map[string][]config.LogEntry),
		lastCPU:       make(map[string]sysstats.CPUSample),
		sftpTransfers: make(map[string]context.CancelFunc),
		dnsRegistry:   registry,
		dnsServer:     dns.NewServer(registry),
		connectSharedDNS: func() (sharedPortlessRegistry, error) {
			return dns.ConnectRemoteRegistry()
		},
	}
	app.termMgr = ssh.NewTerminalManager(app.getPassphrase)
	app.sftpMgr = ssh.NewSFTPManager(app.getPassphrase)
	app.manager = ssh.NewManager(func(event ssh.StatusEvent) {
		app.emitStatus(event)
		app.tray.HandleStatusEvent(event)
	}, app.getPassphrase)
	app.manager.WithDNSRegistry(registry)
	app.manager.WithLogEmitter(func(tunnelID, level, msg string) {
		app.appendTunnelLog(tunnelID, level, msg)
	})
	app.manager.WithBindErrorEmitter(func(e ssh.PortlessBindError) {
		// A log line alone isn't enough — desktop users don't read journalctl.
		// Surface it both as an in-app banner and a desktop notification.
		if app.ctx != nil {
			runtime.EventsEmit(app.ctx, "portless:bind-failed", e)
		}
		title := "Portless forward failed"
		body := e.Message
		if e.NeedsCapability {
			title = "Portless needs permission"
			body = e.Message + " Open the app to authorize."
		}
		app.tray.Notify(title, body)
	})
	app.manager.WithForwardErrorEmitter(func(e ssh.PortForwardError) {
		if app.ctx != nil {
			runtime.EventsEmit(app.ctx, "port-forward:failed", e)
		}
		app.tray.Notify("SSH port forwarding blocked", e.Message)
	})
	app.tray = tray.New(tray.Callbacks{
		ShowWindow: func() { app.showWindow() },
		Quit:       func() { app.quit() },
		Connect:    func(id string) error { return app.ConnectTunnel(id) },
		Disconnect: func(id string) error { return app.DisconnectTunnel(id) },
		CopyToClip: func(text string) error { return app.copyToClipboard(text) },
		GetTunnels: func() []config.TunnelConfig { return store.GetTunnels() },
		OnUpdate:   func() { app.installUpdateFromTray() },
	})
	return app
}

func (a *App) appendTunnelLog(tunnelID, level, msg string) {
	entry := config.LogEntry{Timestamp: time.Now(), Level: level, Message: msg}
	a.logMu.Lock()
	buf := a.logBuf[tunnelID]
	if len(buf) >= 300 {
		buf = buf[1:]
	}
	a.logBuf[tunnelID] = append(buf, entry)
	a.logMu.Unlock()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "tunnel:log", map[string]any{
			"tunnelId": tunnelID,
			"entry":    entry,
		})
	}
}

// startup is called by Wails when the application starts.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.tray.Start()

	// Refresh the executable path recorded by the platform's autostart
	// mechanism. This keeps start-on-login working when an update renames or
	// relocates the application bundle.
	if enabled, err := autostart.IsEnabled(); err != nil {
		slog.Warn("checking autostart configuration failed", "error", err)
	} else if enabled {
		if err := autostart.Enable(a.prefs.Get().StartMinimized); err != nil {
			slog.Warn("refreshing autostart configuration failed", "error", err)
		}
	}

	if a.startHidden {
		// Wails v2 activates the macOS application even when StartHidden is
		// set. Hide the application itself so login startup cannot steal focus.
		runtime.Hide(a.ctx)
	} else {
		runtime.WindowShow(a.ctx)
	}

	go a.autoConnectTunnels()

	go func() {
		info, err := a.checkForUpdate(ctx)
		if err != nil {
			slog.Warn("update check failed", "error", err)
			return
		}
		if info == nil {
			return
		}
		slog.Info("update available", "version", info.LatestVersion)
	}()
}

// autoConnectTunnels fires off a Connect for every tunnel marked
// AutoConnect=true. ConnectTunnel spawns a goroutine per tunnel and returns
// immediately, so iterating sequentially is fine — the dials run in parallel
// behind the manager. Portless tunnels share the embedded DNS server; the
// first one in the loop pays the EnsureSystemConfigured cost, the rest hit
// the idempotent fast path.
func (a *App) autoConnectTunnels() {
	for _, t := range a.store.GetTunnels() {
		if !t.AutoConnect {
			continue
		}
		slog.Info("auto-connecting tunnel", "id", t.ID, "name", t.Name)
		if err := a.ConnectTunnel(t.ID); err != nil {
			slog.Warn("auto-connect failed", "id", t.ID, "error", err)
		}
	}
}

// shutdown is called by Wails when the application is closing.
func (a *App) shutdown(ctx context.Context) {
	slog.Info("shutting down, disconnecting all tunnels")
	a.saveWindowSize()
	a.tray.Stop()
	a.termMgr.CloseAll()
	a.sftpMgr.CloseAll()
	a.manager.DisconnectAll()
	a.dnsMu.Lock()
	srv := a.dnsServer
	a.dnsMu.Unlock()
	if srv != nil {
		srv.Stop()
	}
	a.dnsMu.Lock()
	sharedRegistry := a.sharedDNSRegistry
	a.sharedDNSRegistry = nil
	a.dnsMu.Unlock()
	if sharedRegistry != nil {
		if err := sharedRegistry.Close(); err != nil {
			slog.Debug("closing shared Portless registry failed", "error", err)
		}
	}
}

// GetTunnels returns all configured tunnels.
func (a *App) GetTunnels() []config.TunnelConfig {
	return a.store.GetTunnels()
}

// GetConfigFiles returns the list of SSH config source files (main + included),
// with paths collapsed to use ~/ where possible.
func (a *App) GetConfigFiles() []string {
	files := a.store.GetConfigFiles()
	for i, f := range files {
		files[i] = sshconfig.CollapseTildePath(f)
	}
	return files
}

// GetIncludedConfigFiles lists the files that can be written to when Include
// directives are present in the main config.
func (a *App) GetIncludedConfigFiles() []config.ConfigFileInfo {
	files, err := a.store.GetIncludedConfigFiles()
	if err != nil {
		slog.Warn("failed to list included config files", "error", err)
		return nil
	}
	return files
}

// AddTunnel persists a new tunnel configuration. ID is set to Name.
func (a *App) AddTunnel(t config.TunnelConfig) error {
	t.ID = t.Name
	if err := a.store.AddTunnel(t); err != nil {
		return err
	}
	a.tray.RefreshMenu()
	return nil
}

// UpdateTunnel updates an existing tunnel configuration. If the tunnel is
// currently active, it is disconnected and reconnected with the new config so
// edits (port forwards, host, portless settings, etc.) take effect without
// the user having to toggle the tunnel manually.
func (a *App) UpdateTunnel(t config.TunnelConfig) error {
	wasActive := a.isTunnelActive(t.ID)
	slog.Info("UpdateTunnel", "id", t.ID, "wasActive", wasActive)
	if wasActive {
		slog.Info("UpdateTunnel: disconnecting before save", "id", t.ID)
		_ = a.manager.DisconnectAndWait(t.ID)
		slog.Info("UpdateTunnel: disconnect complete", "id", t.ID)
	}

	if err := a.store.UpdateTunnel(t); err != nil {
		if wasActive {
			// Best-effort restore on save failure
			if cfg, ok := a.store.GetTunnel(t.ID); ok {
				if err := a.manager.Connect(cfg); err != nil {
					slog.Warn("could not restore tunnel after failed update", "id", t.ID, "error", err)
				}
			}
		}
		return err
	}
	a.tray.RefreshMenu()

	if wasActive {
		newID := t.Name // store.UpdateTunnel sets ID = Name on rename
		slog.Info("UpdateTunnel: reconnecting", "id", newID)
		if err := a.ConnectTunnel(newID); err != nil {
			slog.Warn("reconnect after update failed", "id", newID, "error", err)
			return fmt.Errorf("saved config, but reconnect failed: %w", err)
		}
		slog.Info("UpdateTunnel: reconnect initiated", "id", newID)
	}
	return nil
}

func (a *App) isTunnelActive(id string) bool {
	statuses := a.manager.GetStatuses()
	s, ok := statuses[id]
	if !ok {
		return false
	}
	switch s.Status {
	case config.StatusConnected, config.StatusConnecting, config.StatusReconnecting:
		return true
	}
	return false
}

// DeleteTunnel removes a tunnel by ID. Disconnects it first if running.
func (a *App) DeleteTunnel(id string) error {
	_ = a.manager.Disconnect(id) // ignore error if not connected
	if err := a.store.DeleteTunnel(id); err != nil {
		return err
	}
	a.tray.RefreshMenu()
	return nil
}

// SetTunnelPinned toggles the pinned state of a tunnel and persists it.
func (a *App) SetTunnelPinned(id string, pinned bool) error {
	t, ok := a.store.GetTunnel(id)
	if !ok {
		return &tunnelNotFoundError{id}
	}
	t.Pinned = pinned
	if err := a.store.UpdateTunnel(t); err != nil {
		return err
	}
	a.tray.RefreshMenu()
	return nil
}

// ConnectTunnel starts an SSH connection for the given tunnel ID.
func (a *App) ConnectTunnel(id string) error {
	cfg, ok := a.store.GetTunnel(id)
	if !ok {
		return &tunnelNotFoundError{id}
	}
	if tunnelHasPortless(cfg) {
		if err := a.ensurePortlessReady(cfg); err != nil {
			return err
		}
	}
	return a.manager.Connect(cfg)
}

// AuthorizePrivilegedBind grants this binary CAP_NET_BIND_SERVICE (Linux only)
// via a one-time PolicyKit prompt, so portless forwards can bind privileged
// ports like :80.
//
// It deliberately does NOT reconnect afterwards: Linux applies file
// capabilities only at execve(), so the already-running process cannot use the
// freshly granted capability. The caller must restart the app (RestartApp) for
// it to take effect — the frontend prompts for this on success.
func (a *App) AuthorizePrivilegedBind() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating current executable: %w", err)
	}
	if err := netcap.Authorize(a.ctx, exe); err != nil {
		return err
	}
	slog.Info("portless: CAP_NET_BIND_SERVICE granted; restart required to apply", "exe", exe)
	return nil
}

// RestartApp relaunches a fresh instance of the app and quits the current one.
// Used after granting CAP_NET_BIND_SERVICE so the new process inherits the
// file capability at execve() time.
func (a *App) RestartApp() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating current executable: %w", err)
	}
	cmd := exec.Command(exe)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relaunching app: %w", err)
	}
	slog.Info("relaunching app", "exe", exe, "pid", cmd.Process.Pid)
	a.quit()
	return nil
}

func tunnelHasPortless(cfg config.TunnelConfig) bool {
	for _, pf := range cfg.PortForwards {
		if pf.Portless {
			return true
		}
	}
	return false
}

func tunnelRequiresPrivilegedPortRedirect(cfg config.TunnelConfig) bool {
	for _, pf := range cfg.PortForwards {
		if !pf.Portless {
			continue
		}
		publicPort := pf.RemotePort
		if pf.ExposePort > 0 {
			publicPort = pf.ExposePort
		}
		if portless.RequiresPrivilegedPortRedirect(publicPort) {
			return true
		}
	}
	return false
}

// ensurePortlessReady performs the one-time per-OS admin setup (if not already
// done) and starts the embedded DNS server. Safe to call repeatedly; each
// step is idempotent.
func (a *App) ensurePortlessReady(cfg config.TunnelConfig) error {
	a.dnsMu.Lock()
	defer a.dnsMu.Unlock()
	if a.sharedDNSRegistry != nil {
		a.manager.WithDNSRegistry(a.sharedDNSRegistry)
		return nil
	}

	requirements := dns.SetupRequirements{
		PrivilegedPortRedirect: tunnelRequiresPrivilegedPortRedirect(cfg),
	}
	if !dns.IsSystemConfigured(requirements) {
		if a.portlessSetupErr != nil {
			// Already confirmed this run that setup won't persist for this
			// machine — go straight to the fallback banner instead of
			// repeating the elevated dance (and any admin prompt) for
			// every tunnel that auto-connects.
			a.handlePortlessSetupPersistenceFailure(cfg, a.portlessSetupErr)
			return nil
		}
		slog.Info("portless: system not configured, prompting for admin setup")
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "portless:setup-started")
		}
		err := dns.EnsureSystemConfigured(a.ctx, requirements)
		if a.ctx != nil {
			payload := map[string]any{"ok": err == nil}
			if err != nil {
				payload["error"] = err.Error()
			}
			runtime.EventsEmit(a.ctx, "portless:setup-finished", payload)
		}
		if err != nil {
			var persistErr *dns.PersistenceError
			if errors.As(err, &persistErr) {
				a.portlessSetupErr = persistErr
			}
			if a.handlePortlessSetupPersistenceFailure(cfg, err) {
				return nil
			}
			return fmt.Errorf("portless setup: %w", err)
		}
	}
	if !a.dnsServer.Running() {
		if err := a.dnsServer.Start(); err != nil {
			if a.handlePortlessDNSStartFailure(cfg, err) {
				return nil
			}
			return fmt.Errorf("starting portless DNS server: %w", err)
		}
	}
	a.manager.WithDNSRegistry(a.dnsRegistry)
	if a.portlessFallback != nil {
		a.portlessFallback = nil
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "portless:dns-restored")
		}
	}
	return nil
}

// handlePortlessDNSStartFailure turns only a listen-address conflict into a
// non-fatal fallback. Setup failures and all other DNS startup errors remain
// fatal so administrator configuration problems are not hidden.
func (a *App) handlePortlessDNSStartFailure(cfg config.TunnelConfig, err error) bool {
	if !dns.IsAddressInUse(err) {
		return false
	}
	if a.connectSharedDNS != nil {
		sharedRegistry, sharedErr := a.connectSharedDNS()
		if sharedErr == nil && sharedRegistry != nil {
			a.sharedDNSRegistry = sharedRegistry
			a.manager.WithDNSRegistry(sharedRegistry)
			msg := fmt.Sprintf("Portless is sharing the DNS registry on %s:%d with another app instance; domains retain distinct 127.0.1.x addresses.", dns.BindIP, dns.ListenPort)
			a.appendTunnelLog(cfg.ID, "info", msg)
			slog.Info("portless joined shared DNS registry", "tunnel", cfg.Name)
			if a.portlessFallback != nil {
				a.portlessFallback = nil
				if a.ctx != nil {
					runtime.EventsEmit(a.ctx, "portless:dns-restored")
				}
			}
			return true
		}
		if sharedErr == nil {
			sharedErr = fmt.Errorf("compatible owner returned no registry")
		}
		slog.Warn("portless DNS owner does not expose a compatible shared registry",
			"tunnel", cfg.Name, "error", sharedErr)
	}

	a.manager.WithDNSRegistry(nil)
	addr := fmt.Sprintf("%s:%d", dns.BindIP, dns.ListenPort)
	msg := fmt.Sprintf("Portless unavailable: %s is held by a process without a compatible shared registry. Domains are disabled; forwards will try configured 127.0.0.1 local ports, where duplicate ports cannot all be exposed.", addr)
	a.appendTunnelLog(cfg.ID, "warn", msg)
	slog.Warn("portless DNS unavailable; continuing with local-port fallback",
		"tunnel", cfg.Name, "addr", addr, "error", err)
	if a.portlessFallback == nil {
		a.portlessFallback = &PortlessFallbackStatus{TunnelID: cfg.ID, Message: msg}
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "portless:dns-unavailable", a.portlessFallback)
		}
		if a.tray != nil {
			a.tray.Notify("Portless unavailable", msg)
		}
	}
	return true
}

// handlePortlessSetupPersistenceFailure reports a *dns.PersistenceError
// through the same sticky fallback banner used for a contended DNS registry,
// so a broken machine-wide setup degrades instead of silently failing every
// portless connection attempt with an opaque error. Returns false for any
// other error, leaving it to the caller.
func (a *App) handlePortlessSetupPersistenceFailure(cfg config.TunnelConfig, err error) bool {
	var persistErr *dns.PersistenceError
	if !errors.As(err, &persistErr) {
		return false
	}
	a.manager.WithDNSRegistry(nil)
	a.appendTunnelLog(cfg.ID, "warn", persistErr.Message)
	slog.Warn("portless system setup did not persist; continuing with local-port fallback",
		"tunnel", cfg.Name, "error", persistErr.Message, "recommendation", persistErr.Recommendation)
	// Replace a generic fallback (e.g. left over from a contended DNS
	// registry, which never carries a recommendation) once this specific,
	// actionable diagnosis is available — otherwise the "only fill when
	// nil" guard would silently keep showing the earlier, less useful
	// message for the rest of the session.
	upgrade := a.portlessFallback == nil ||
		(persistErr.Recommendation != "" && a.portlessFallback.Recommendation == "")
	if upgrade {
		a.portlessFallback = &PortlessFallbackStatus{
			TunnelID:       cfg.ID,
			Message:        persistErr.Message,
			Recommendation: persistErr.Recommendation,
			Command:        persistErr.Command,
		}
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "portless:dns-unavailable", a.portlessFallback)
		}
		if a.tray != nil {
			a.tray.Notify("Portless unavailable", persistErr.Message)
		}
	}
	return true
}

// GetPortlessFallback returns the active degradation warning, if any. The
// frontend calls it on mount so a hidden auto-connect cannot lose the event.
func (a *App) GetPortlessFallback() *PortlessFallbackStatus {
	a.dnsMu.Lock()
	defer a.dnsMu.Unlock()
	if a.portlessFallback == nil {
		return nil
	}
	status := *a.portlessFallback
	return &status
}

// GetPortlessServiceStatus returns the current machine-wide helper state. On
// non-macOS platforms Available is false, so the frontend hides the section.
func (a *App) GetPortlessServiceStatus() PortlessServiceStatus {
	status := dns.GetSystemServiceStatus()
	return PortlessServiceStatus{
		Available:        status.Available,
		Installed:        status.Installed,
		ApprovalRequired: status.ApprovalRequired,
		Current:          status.Current,
		State:            status.State,
		Message:          status.Message,
	}
}

// InstallPortlessService lets a user explicitly prepare Portless before
// creating a tunnel. First Portless use calls the same idempotent path.
func (a *App) InstallPortlessService() error {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return dns.InstallSystemService(ctx)
}

// UninstallPortlessService is an explicit user action. It removes the system
// configuration with one final authorization and then unregisters the daemon.
func (a *App) UninstallPortlessService() error {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return dns.UninstallSystemService(ctx)
}

func (a *App) OpenPortlessServiceSettings() error {
	return dns.OpenSystemServiceSettings()
}

// DisconnectTunnel stops the SSH connection for the given tunnel ID.
func (a *App) DisconnectTunnel(id string) error {
	return a.manager.Disconnect(id)
}

// GetTunnelStatuses returns the current status of all tunnels.
func (a *App) GetTunnelStatuses() map[string]ssh.StatusEvent {
	return a.manager.GetStatuses()
}

// OpenTerminal opens an interactive SSH terminal session for the given tunnel.
func (a *App) OpenTerminal(tunnelID string) (string, error) {
	cfg, ok := a.store.GetTunnel(tunnelID)
	if !ok {
		return "", &tunnelNotFoundError{tunnelID}
	}

	sessionID, err := a.termMgr.OpenSession(cfg,
		func(sessionID, data string) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "terminal:output", map[string]any{
					"sessionId": sessionID,
					"data":      data,
				})
			}
		},
		func(sessionID string) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "terminal:closed", map[string]any{
					"sessionId": sessionID,
				})
			}
		},
	)
	if err != nil {
		return "", fmt.Errorf("opening terminal for %s: %w", cfg.Name, err)
	}
	return sessionID, nil
}

// TerminalWrite sends input data to a terminal session.
func (a *App) TerminalWrite(sessionID, data string) error {
	ts, ok := a.termMgr.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("terminal session %s not found", sessionID)
	}
	return ts.Write(data)
}

// CloseTerminal closes a terminal session.
func (a *App) CloseTerminal(sessionID string) error {
	a.termMgr.CloseSession(sessionID)
	return nil
}

// ResizeTerminal resizes the PTY of a terminal session.
func (a *App) ResizeTerminal(sessionID string, cols, rows int) error {
	ts, ok := a.termMgr.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("terminal session %s not found", sessionID)
	}
	return ts.Resize(cols, rows)
}

// SFTPOpenResult is returned from OpenSFTP with the session ID and the
// initial directory (remote home) to start browsing from.
type SFTPOpenResult struct {
	SessionID string `json:"sessionId"`
	Home      string `json:"home"`
}

// OpenSFTP starts a new SFTP session for the given tunnel.
func (a *App) OpenSFTP(tunnelID string) (*SFTPOpenResult, error) {
	cfg, ok := a.store.GetTunnel(tunnelID)
	if !ok {
		return nil, &tunnelNotFoundError{tunnelID}
	}
	sessionID, home, err := a.sftpMgr.OpenSession(cfg)
	if err != nil {
		return nil, fmt.Errorf("opening SFTP for %s: %w", cfg.Name, err)
	}
	return &SFTPOpenResult{SessionID: sessionID, Home: home}, nil
}

// CloseSFTP closes an SFTP session.
func (a *App) CloseSFTP(sessionID string) error {
	a.sftpMgr.CloseSession(sessionID)
	return nil
}

// SFTPListDir returns the contents of a remote directory.
func (a *App) SFTPListDir(sessionID, remotePath string) ([]ssh.FileEntry, error) {
	s, ok := a.sftpMgr.GetSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("SFTP session %s not found", sessionID)
	}
	return s.List(remotePath)
}

// SFTPDownload prompts the user for a local save location and downloads the
// remote file. Returns the local path written, or empty string if cancelled.
func (a *App) SFTPDownload(sessionID, remotePath string) (string, error) {
	s, ok := a.sftpMgr.GetSession(sessionID)
	if !ok {
		return "", fmt.Errorf("SFTP session %s not found", sessionID)
	}
	defaultName := filepath.Base(remotePath)
	localPath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Save file as",
		DefaultFilename: defaultName,
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if localPath == "" {
		return "", nil
	}
	xferID, ctx := a.startSFTPTransfer()
	defer a.endSFTPTransfer(xferID)
	progress := a.sftpProgressFn(xferID, sessionID, "download", defaultName, 1, 1)
	if err := s.Download(ctx, remotePath, localPath, progress); err != nil {
		a.emitSFTPProgress(xferID, sessionID, "download", defaultName, 1, 1, 0, 0, err.Error(), true)
		return "", err
	}
	a.emitSFTPProgress(xferID, sessionID, "download", defaultName, 1, 1, 0, 0, "", true)
	return localPath, nil
}

// SFTPDownloadDir prompts the user for a local save location and downloads
// the remote directory as a ZIP archive. Returns the local path written or
// empty string if cancelled.
func (a *App) SFTPDownloadDir(sessionID, remotePath string) (string, error) {
	s, ok := a.sftpMgr.GetSession(sessionID)
	if !ok {
		return "", fmt.Errorf("SFTP session %s not found", sessionID)
	}
	defaultName := filepath.Base(remotePath) + ".zip"
	localPath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Save directory as ZIP",
		DefaultFilename: defaultName,
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if localPath == "" {
		return "", nil
	}
	xferID, ctx := a.startSFTPTransfer()
	defer a.endSFTPTransfer(xferID)
	progress := a.sftpProgressFn(xferID, sessionID, "download", defaultName, 1, 1)
	if err := s.DownloadDirZip(ctx, remotePath, localPath, progress); err != nil {
		a.emitSFTPProgress(xferID, sessionID, "download", defaultName, 1, 1, 0, 0, err.Error(), true)
		return "", err
	}
	a.emitSFTPProgress(xferID, sessionID, "download", defaultName, 1, 1, 0, 0, "", true)
	return localPath, nil
}

// SFTPUploadPick is returned from SFTPPickUploadFiles. Paths lists the local
// files the user selected; Conflicts lists the base names among those that
// already exist in the target remote directory.
type SFTPUploadPick struct {
	Paths     []string `json:"paths"`
	Conflicts []string `json:"conflicts"`
}

// SFTPPickUploadFiles opens the file picker and returns the chosen local
// paths plus any name conflicts in remoteDir. No upload happens yet.
func (a *App) SFTPPickUploadFiles(sessionID, remoteDir string) (*SFTPUploadPick, error) {
	s, ok := a.sftpMgr.GetSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("SFTP session %s not found", sessionID)
	}
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select files to upload",
		Filters: []runtime.FileFilter{
			{DisplayName: "All Files", Pattern: "*"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open dialog: %w", err)
	}
	var conflicts []string
	for _, p := range paths {
		name := filepath.Base(p)
		if _, err := s.StatRemote(remoteDir, name); err == nil {
			conflicts = append(conflicts, name)
		}
	}
	return &SFTPUploadPick{Paths: paths, Conflicts: conflicts}, nil
}

// SFTPUploadFiles uploads the given local paths into remoteDir. Caller is
// responsible for having confirmed any overwrites first.
func (a *App) SFTPUploadFiles(sessionID string, paths []string, remoteDir string) (int, error) {
	s, ok := a.sftpMgr.GetSession(sessionID)
	if !ok {
		return 0, fmt.Errorf("SFTP session %s not found", sessionID)
	}
	xferID, ctx := a.startSFTPTransfer()
	defer a.endSFTPTransfer(xferID)
	var uploaded int
	for i, p := range paths {
		if ctx.Err() != nil {
			break
		}
		name := filepath.Base(p)
		progress := a.sftpProgressFn(xferID, sessionID, "upload", name, i+1, len(paths))
		if _, err := s.Upload(ctx, p, remoteDir, progress); err != nil {
			a.emitSFTPProgress(xferID, sessionID, "upload", name, i+1, len(paths), 0, 0, err.Error(), true)
			return uploaded, err
		}
		uploaded++
	}
	a.emitSFTPProgress(xferID, sessionID, "upload", "", uploaded, len(paths), 0, 0, "", true)
	return uploaded, nil
}

// CancelSFTPTransfer cancels the in-flight transfer with the given ID.
// Returns true if it was running, false if no such transfer exists.
func (a *App) CancelSFTPTransfer(transferID string) bool {
	a.sftpTransferMu.Lock()
	cancel, ok := a.sftpTransfers[transferID]
	a.sftpTransferMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func (a *App) startSFTPTransfer() (string, context.Context) {
	a.sftpTransferMu.Lock()
	a.sftpNextXferID++
	id := fmt.Sprintf("xfer-%d", a.sftpNextXferID)
	ctx, cancel := context.WithCancel(a.ctx)
	a.sftpTransfers[id] = cancel
	a.sftpTransferMu.Unlock()
	return id, ctx
}

func (a *App) endSFTPTransfer(id string) {
	a.sftpTransferMu.Lock()
	delete(a.sftpTransfers, id)
	a.sftpTransferMu.Unlock()
}

func (a *App) sftpProgressFn(transferID, sessionID, kind, name string, index, total int) ssh.ProgressFunc {
	return func(transferred, fileSize int64) {
		a.emitSFTPProgress(transferID, sessionID, kind, name, index, total, transferred, fileSize, "", false)
	}
}

func (a *App) emitSFTPProgress(transferID, sessionID, kind, name string, index, total int, transferred, size int64, errMsg string, done bool) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "sftp:progress", map[string]any{
		"transferId":  transferID,
		"sessionId":   sessionID,
		"kind":        kind,
		"name":        name,
		"fileIndex":   index,
		"fileTotal":   total,
		"transferred": transferred,
		"size":        size,
		"error":       errMsg,
		"done":        done,
	})
}

// SFTPMkdir creates a new remote directory.
func (a *App) SFTPMkdir(sessionID, remotePath string) error {
	s, ok := a.sftpMgr.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("SFTP session %s not found", sessionID)
	}
	return s.Mkdir(remotePath)
}

// SFTPDelete removes a remote file or directory (recursively).
func (a *App) SFTPDelete(sessionID, remotePath string) error {
	s, ok := a.sftpMgr.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("SFTP session %s not found", sessionID)
	}
	return s.Remove(remotePath)
}

// SFTPRename renames or moves a remote entry.
func (a *App) SFTPRename(sessionID, oldPath, newPath string) error {
	s, ok := a.sftpMgr.GetSession(sessionID)
	if !ok {
		return fmt.Errorf("SFTP session %s not found", sessionID)
	}
	return s.Rename(oldPath, newPath)
}

// SFTPReadText loads a remote file as text for in-app editing. When force is
// false the file is not returned if it looks binary or exceeds the edit size
// limit; the result's Binary/TooLarge flags say which, so the UI can offer to
// open it anyway.
func (a *App) SFTPReadText(sessionID, remotePath string, force bool) (*ssh.TextFileResult, error) {
	s, ok := a.sftpMgr.GetSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("SFTP session %s not found", sessionID)
	}
	return s.ReadText(remotePath, force)
}

// SFTPWriteResult reports the outcome of SFTPWriteText. Conflict is true when
// the remote file changed since it was opened and nothing was written.
type SFTPWriteResult struct {
	Conflict bool      `json:"conflict"`
	ModTime  time.Time `json:"modTime"`
}

// SFTPWriteText writes edited content back to a remote file. expectModTimeMs
// is the modification time (Unix milliseconds) observed when the file was
// opened; pass 0 to skip the check and overwrite unconditionally. If the
// remote file has changed since, no write happens and Conflict is true.
func (a *App) SFTPWriteText(sessionID, remotePath, content string, expectModTimeMs int64) (*SFTPWriteResult, error) {
	s, ok := a.sftpMgr.GetSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("SFTP session %s not found", sessionID)
	}
	var expect time.Time
	if expectModTimeMs != 0 {
		expect = time.UnixMilli(expectModTimeMs)
	}
	changed, modTime, err := s.WriteText(remotePath, content, expect)
	if err != nil {
		return nil, err
	}
	return &SFTPWriteResult{Conflict: changed, ModTime: modTime}, nil
}

func (a *App) showWindow() {
	if a.ctx != nil {
		// A hidden macOS application must be unhidden before its window can
		// become key. This is harmless on Windows and Linux.
		runtime.Show(a.ctx)
		runtime.WindowShow(a.ctx)
	}
}

func (a *App) quit() {
	if a.ctx != nil {
		a.forceQuit = true
		runtime.Quit(a.ctx)
	}
}

func (a *App) saveWindowSize() {
	if a.ctx == nil {
		return
	}
	w, h := runtime.WindowGetSize(a.ctx)
	if w < 400 || h < 300 {
		return
	}
	p := a.prefs.Get()
	p.WindowWidth = w
	p.WindowHeight = h
	if err := a.prefs.Set(p); err != nil {
		slog.Warn("failed to save window size", "error", err)
	}
}

func (a *App) copyToClipboard(text string) error {
	if a.ctx == nil {
		return nil
	}
	return runtime.ClipboardSetText(a.ctx, text)
}

// CopyToClipboard exposes the OS clipboard to the frontend so tunnel-card
// elements (port forward tags, portless addresses) can copy their value on
// click.
func (a *App) CopyToClipboard(text string) error {
	return a.copyToClipboard(text)
}

func (a *App) emitStatus(event ssh.StatusEvent) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "tunnel:status-changed", event)
	if event.Error != "" {
		runtime.EventsEmit(a.ctx, "tunnel:error", event)
	}
}

// getPassphrase is called by the SSH manager when an encrypted key needs
// a passphrase. It first checks the OS keychain, then prompts the user
// via the frontend.
func (a *App) getPassphrase(keyPath string) (string, error) {
	// Try keychain first
	stored, err := keychain.GetPassphrase(keyPath)
	if err != nil {
		slog.Warn("keychain lookup failed", "keyPath", keyPath, "error", err)
	}
	if stored != "" {
		return stored, nil
	}

	// Ask frontend for passphrase
	a.passphraseMu.Lock()
	a.passphraseChan = make(chan string, 1)
	a.passphraseMu.Unlock()

	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "passphrase:request", keyPath)
	}

	// Wait for the frontend to call SubmitPassphrase
	passphrase := <-a.passphraseChan

	a.passphraseMu.Lock()
	a.passphraseChan = nil
	a.passphraseMu.Unlock()

	if passphrase == "" {
		return "", fmt.Errorf("passphrase entry cancelled")
	}

	// Store in keychain for next time
	if err := keychain.SetPassphrase(keyPath, passphrase); err != nil {
		slog.Warn("failed to store passphrase in keychain", "keyPath", keyPath, "error", err)
	}

	return passphrase, nil
}

// SubmitPassphrase is called by the frontend to provide a passphrase
// for an encrypted SSH key.
func (a *App) SubmitPassphrase(passphrase string) {
	a.passphraseMu.Lock()
	ch := a.passphraseChan
	a.passphraseMu.Unlock()

	if ch != nil {
		ch <- passphrase
	}
}

// GetAutostart returns whether the app is set to start on login.
func (a *App) GetAutostart() bool {
	enabled, _ := autostart.IsEnabled()
	return enabled
}

// SetAutostart enables or disables starting the app on login.
func (a *App) SetAutostart(enabled bool) error {
	if enabled {
		return autostart.Enable(a.prefs.Get().StartMinimized)
	}
	return autostart.Disable()
}

// GetStartMinimized returns whether login startup should keep the app hidden.
func (a *App) GetStartMinimized() bool {
	return a.prefs.Get().StartMinimized
}

// SetStartMinimized configures login startup visibility and persists it.
func (a *App) SetStartMinimized(enabled bool) error {
	p := a.prefs.Get()
	p.StartMinimized = enabled
	if err := a.prefs.Set(p); err != nil {
		return err
	}

	autostartEnabled, err := autostart.IsEnabled()
	if err != nil {
		return err
	}
	if autostartEnabled {
		return autostart.Enable(enabled)
	}
	return nil
}

// GetCurrentVersion returns the compiled-in application version.
func (a *App) GetCurrentVersion() string {
	return Version
}

// GetUpdateChannel returns the persisted update channel.
func (a *App) GetUpdateChannel() string {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return string(a.updateChannel)
}

// SetUpdateChannel validates and persists the update channel. Any update found
// for the previous channel is invalidated immediately; the frontend triggers a
// fresh check after this method succeeds.
func (a *App) SetUpdateChannel(value string) error {
	channel, err := updater.ParseChannel(value)
	if err != nil {
		return err
	}

	a.updateNotifyMu.Lock()
	defer a.updateNotifyMu.Unlock()

	a.updateMu.Lock()
	if a.updateChannel == channel {
		a.updateMu.Unlock()
		return nil
	}

	p := a.prefs.Get()
	p.UpdateChannel = string(channel)
	if err := a.prefs.Set(p); err != nil {
		a.updateMu.Unlock()
		return err
	}

	a.updateChannel = channel
	a.updateGeneration++
	a.pendingUpdate = nil
	a.updateMu.Unlock()

	a.emitUpdateState(nil)
	return nil
}

// CheckForUpdate contacts GitHub for a newer release. Returns nil if already
// up to date. Stores result in pendingUpdate and emits the update-available event.
func (a *App) CheckForUpdate() (*updater.UpdateInfo, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.checkForUpdate(ctx)
}

func (a *App) checkForUpdate(ctx context.Context) (*updater.UpdateInfo, error) {
	channel, generation := a.beginUpdateCheck()
	checker := a.updateChecker
	if checker == nil {
		checker = updater.Check
	}
	info, err := checker(ctx, Version, "JustCodePL/ssh-tunnel-manager", channel)
	if err != nil {
		return nil, err
	}

	a.updateNotifyMu.Lock()
	defer a.updateNotifyMu.Unlock()
	if !a.commitUpdateResult(generation, info) {
		// The channel changed while the request was in flight. Discard the stale
		// response instead of surfacing an update from the previous channel.
		return nil, nil
	}
	a.emitUpdateState(info)
	return info, nil
}

func (a *App) beginUpdateCheck() (updater.Channel, uint64) {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return a.updateChannel, a.updateGeneration
}

func (a *App) commitUpdateResult(generation uint64, info *updater.UpdateInfo) bool {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	if generation != a.updateGeneration {
		return false
	}
	a.pendingUpdate = info
	return true
}

func (a *App) emitUpdateState(info *updater.UpdateInfo) {
	if info == nil {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "updater:update-cleared")
		}
		if a.tray != nil {
			a.tray.SetUpdateAvailable("")
		}
		return
	}

	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "updater:update-available", info)
	}
	if a.tray != nil {
		a.tray.SetUpdateAvailable(info.LatestVersion)
	}
}

// InstallUpdate downloads and runs the pending installer, then quits the app.
func (a *App) InstallUpdate() error {
	a.updateMu.Lock()
	info := a.pendingUpdate
	a.updateMu.Unlock()

	if info == nil {
		return fmt.Errorf("no pending update")
	}

	if err := updater.Install(a.ctx, info); err != nil {
		return err
	}
	a.quit()
	return nil
}

func (a *App) installUpdateFromTray() {
	if err := a.InstallUpdate(); err != nil {
		slog.Error("tray: install update failed", "error", err)
	}
}

// SelectFile opens a native file dialog for picking an SSH config file.
func (a *App) SelectFile() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select SSH Config File",
		Filters: []runtime.FileFilter{
			{DisplayName: "All Files", Pattern: "*"},
		},
	})
}

// SelectSaveFile opens a native save dialog.
func (a *App) SelectSaveFile(defaultName string) (string, error) {
	return runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export Tunnels",
		DefaultFilename: defaultName,
	})
}

// ImportPreview parses an SSH config file and returns the host entries found
// without importing them. The frontend uses this to show a preview.
func (a *App) ImportPreview(path string) ([]config.TunnelConfig, error) {
	entries, err := sshconfig.ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	var tunnels []config.TunnelConfig
	for _, e := range entries {
		tunnels = append(tunnels, entryToTunnel(e))
	}
	return tunnels, nil
}

// ImportTunnels imports the selected tunnels (by name) from the given file.
func (a *App) ImportTunnels(path string, names []string) (int, error) {
	entries, err := sshconfig.ParseFile(path)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", path, err)
	}

	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}

	var imported int
	for _, e := range entries {
		if !nameSet[e.Alias] {
			continue
		}
		tc := entryToTunnel(e)
		tc.ID = tc.Name
		if err := a.store.AddTunnel(tc); err != nil {
			slog.Warn("import: skipping duplicate tunnel", "name", tc.Name, "error", err)
			continue
		}
		imported++
	}

	if imported > 0 {
		runtime.EventsEmit(a.ctx, "tunnels:changed")
		a.tray.RefreshMenu()
	}
	return imported, nil
}

// ExportTunnels writes selected tunnels (by ID) to an SSH config file.
func (a *App) ExportTunnels(path string, ids []string) error {
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}

	tunnels := a.store.GetTunnels()
	var blocks []string
	for _, t := range tunnels {
		if !idSet[t.ID] {
			continue
		}
		blocks = append(blocks, sshconfig.RenderHostBlock(tunnelToEntry(t)))
	}

	content := strings.Join(blocks, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing export file: %w", err)
	}
	return nil
}

func entryToTunnel(e sshconfig.HostEntry) config.TunnelConfig {
	t := config.TunnelConfig{
		ID:              e.Alias,
		Name:            e.Alias,
		Host:            e.HostName,
		Port:            e.Port,
		User:            e.User,
		KeyPath:         e.IdentityFile,
		ProxyCommand:    e.ProxyCommand,
		ProxyJump:       e.ProxyJump,
		Color:           e.Color,
		Group:           e.Group,
		AutoConnect:     e.AutoConnect,
		Pinned:          e.Pinned,
		SourceFile:      e.SourceFile,
		SourceFileLabel: sshconfig.CollapseTildePath(e.SourceFile),
	}
	for _, pf := range e.PortForwards {
		t.PortForwards = append(t.PortForwards, config.PortForward{
			LocalPort:   pf.LocalPort,
			RemoteHost:  pf.RemoteHost,
			RemotePort:  pf.RemotePort,
			Description: pf.Description,
			Portless:    pf.Portless,
			Domain:      pf.Domain,
			ExposePort:  pf.ExposePort,
		})
	}
	return t
}

func tunnelToEntry(t config.TunnelConfig) sshconfig.HostEntry {
	e := sshconfig.HostEntry{
		Alias:        t.Name,
		HostName:     t.Host,
		Port:         t.Port,
		User:         t.User,
		IdentityFile: t.KeyPath,
		ProxyCommand: t.ProxyCommand,
		ProxyJump:    t.ProxyJump,
		Color:        t.Color,
		Group:        t.Group,
		AutoConnect:  t.AutoConnect,
		Pinned:       t.Pinned,
		SourceFile:   t.SourceFile,
	}
	for _, pf := range t.PortForwards {
		e.PortForwards = append(e.PortForwards, sshconfig.PortForwardEntry{
			LocalPort:   pf.LocalPort,
			RemoteHost:  pf.RemoteHost,
			RemotePort:  pf.RemotePort,
			Description: pf.Description,
			Portless:    pf.Portless,
			Domain:      pf.Domain,
			ExposePort:  pf.ExposePort,
		})
	}
	return e
}

// GetTunnelLogs returns the buffered log entries for a given tunnel ID.
func (a *App) GetTunnelLogs(tunnelID string) []config.LogEntry {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	buf := a.logBuf[tunnelID]
	if len(buf) == 0 {
		return []config.LogEntry{}
	}
	out := make([]config.LogEntry, len(buf))
	copy(out, buf)
	return out
}

// ClearTunnelLogs removes all buffered log entries for a given tunnel ID.
func (a *App) ClearTunnelLogs(tunnelID string) {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	delete(a.logBuf, tunnelID)
}

// ConnectGroup connects all tunnels belonging to the given group.
func (a *App) ConnectGroup(group string) error {
	tunnels := a.store.GetTunnels()
	var firstErr error
	for _, t := range tunnels {
		if t.Group != group {
			continue
		}
		if err := a.ConnectTunnel(t.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DisconnectGroup disconnects all tunnels belonging to the given group.
func (a *App) DisconnectGroup(group string) error {
	tunnels := a.store.GetTunnels()
	var firstErr error
	for _, t := range tunnels {
		if t.Group != group {
			continue
		}
		if err := a.manager.Disconnect(t.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RenameGroup renames a group across all tunnels that belong to it.
func (a *App) RenameGroup(oldName, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("new group name cannot be empty")
	}
	if oldName == newName {
		return nil
	}
	tunnels := a.store.GetTunnels()
	for _, t := range tunnels {
		if t.Group != oldName {
			continue
		}
		t.Group = newName
		if err := a.store.UpdateTunnel(t); err != nil {
			return fmt.Errorf("renaming group for %s: %w", t.Name, err)
		}
	}
	a.tray.RefreshMenu()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "tunnels:changed")
	}
	return nil
}

type tunnelNotFoundError struct {
	id string
}

func (e *tunnelNotFoundError) Error() string {
	return "tunnel not found: " + e.id
}

// statsCommandTimeout bounds the per-call SSH command run for monitoring so a
// slow remote can't stall a UI poll.
const statsCommandTimeout = 8 * time.Second

// dockerCommandTimeout is much more generous than statsCommandTimeout: `docker
// ps`, `inspect` and `stats` talk to the docker daemon, which can be very slow
// to respond on small/loaded VPSes (cold daemon, swapping) and easily exceeds
// the 8s monitoring budget.
const dockerCommandTimeout = 45 * time.Second

// runRemote runs a shell command on the live SSH client of a connected tunnel.
func (a *App) runRemote(tunnelID, cmd string) (string, error) {
	return a.runRemoteTimeout(tunnelID, cmd, statsCommandTimeout)
}

// runRemoteTimeout is runRemote with a caller-supplied command timeout.
func (a *App) runRemoteTimeout(tunnelID, cmd string, timeout time.Duration) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return a.manager.RunCommand(ctx, tunnelID, cmd)
}

// GetServerStats returns a CPU/RAM snapshot for a connected tunnel's remote
// host. CPU percentage is derived from the delta against the previous poll, so
// the first call after connecting reports HasCPU=false.
func (a *App) GetServerStats(tunnelID string) (sysstats.ServerStats, error) {
	out, err := a.runRemote(tunnelID, "cat /proc/stat; echo ---; cat /proc/meminfo")
	if err != nil {
		return sysstats.ServerStats{}, err
	}

	parts := strings.SplitN(out, "---", 2)
	var statPart, memPart string
	statPart = parts[0]
	if len(parts) == 2 {
		memPart = parts[1]
	}

	cur := sysstats.ParseCPUSample(statPart)
	totalKB, availKB := sysstats.ParseMeminfo(memPart)

	a.statsMu.Lock()
	prev := a.lastCPU[tunnelID]
	a.lastCPU[tunnelID] = cur
	a.statsMu.Unlock()

	stats := sysstats.ServerStats{
		MemTotal: totalKB * 1024,
		MemUsed:  (totalKB - availKB) * 1024,
	}
	if prev.Valid() && cur.Valid() {
		stats.CPUPercent = sysstats.CPUPercent(prev, cur)
		stats.HasCPU = true
	}
	return stats, nil
}

// GetProcessStats returns an htop-like snapshot (per-core CPU, memory/swap,
// load, uptime, top processes) for a connected tunnel's host. CPU percentages
// come from two /proc/stat reads taken 0.6s apart within the same command, so
// the values are accurate on the first call.
func (a *App) GetProcessStats(tunnelID string) (sysstats.ProcessStats, error) {
	const cmd = `cat /proc/stat; echo @@@; sleep 0.6; cat /proc/stat; echo @@@; ` +
		`cat /proc/meminfo; echo @@@; cat /proc/loadavg; echo @@@; cat /proc/uptime; echo @@@; ` +
		`LANG=C ps -eo pid,user:32,pcpu,pmem,comm --sort=-pcpu 2>/dev/null | head -n 41`
	out, err := a.runRemote(tunnelID, cmd)
	if err != nil {
		return sysstats.ProcessStats{}, err
	}
	parts := strings.Split(out, "@@@")
	if len(parts) < 6 {
		return sysstats.ProcessStats{}, fmt.Errorf("unexpected monitor output (got %d sections)", len(parts))
	}
	return sysstats.BuildProcessStats(parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]), nil
}

// GetServerCapabilities detects which optional tools (docker, htop) are present
// on the remote host and its OS, and persists the result so the buttons can be
// shown immediately on future connections. The frontend calls this on every
// connect so tools installed (or removed) since last time are detected.
func (a *App) GetServerCapabilities(tunnelID string) (sysstats.Capabilities, error) {
	const cmd = `command -v docker >/dev/null 2>&1 && echo docker; ` +
		`command -v htop >/dev/null 2>&1 && echo htop; uname -s`
	out, err := a.runRemote(tunnelID, cmd)
	if err != nil {
		return sysstats.Capabilities{}, err
	}
	c := sysstats.ParseCapabilities(out)
	if err := a.saveCapabilities(tunnelID, c); err != nil {
		slog.Warn("failed to persist host capabilities", "tunnel", tunnelID, "error", err)
	}
	return c, nil
}

// GetSavedCapabilities returns the persisted per-tunnel tool capabilities so
// the frontend can render the monitoring buttons immediately at startup.
func (a *App) GetSavedCapabilities() map[string]sysstats.Capabilities {
	p := a.prefs.Get()
	out := make(map[string]sysstats.Capabilities, len(p.HostTools))
	for id, t := range p.HostTools {
		out[id] = sysstats.Capabilities{Docker: t.Docker, Htop: t.Htop, OS: t.OS}
	}
	return out
}

// VerifyTool re-checks whether a tool (docker/htop) is still present on the
// remote host with a `command -v` probe, and updates the persisted record.
//
// The returned bool reflects presence ONLY when error is nil. A non-nil error
// means the probe itself could not run (connection lost, etc.) and the caller
// must NOT interpret that as the tool being absent.
func (a *App) VerifyTool(tunnelID, tool string) (bool, error) {
	switch tool {
	case "docker", "htop":
	default:
		return false, fmt.Errorf("unknown tool %q", tool)
	}
	cmd := fmt.Sprintf("command -v %s >/dev/null 2>&1 && echo yes || echo no", tool)
	out, err := a.runRemote(tunnelID, cmd)
	if err != nil {
		return false, err
	}
	present := strings.Contains(out, "yes")
	a.updateSavedTool(tunnelID, tool, present)
	return present, nil
}

// saveCapabilities persists a full capability record for a tunnel.
func (a *App) saveCapabilities(tunnelID string, c sysstats.Capabilities) error {
	p := a.prefs.Get()
	p.HostTools = cloneHostTools(p.HostTools)
	p.HostTools[tunnelID] = prefs.HostTools{Docker: c.Docker, Htop: c.Htop, OS: c.OS}
	return a.prefs.Set(p)
}

// updateSavedTool flips a single tool's presence in the persisted record.
func (a *App) updateSavedTool(tunnelID, tool string, present bool) {
	p := a.prefs.Get()
	p.HostTools = cloneHostTools(p.HostTools)
	rec := p.HostTools[tunnelID]
	switch tool {
	case "docker":
		rec.Docker = present
	case "htop":
		rec.Htop = present
	}
	p.HostTools[tunnelID] = rec
	if err := a.prefs.Set(p); err != nil {
		slog.Warn("failed to persist tool verification", "tunnel", tunnelID, "tool", tool, "error", err)
	}
}

// cloneHostTools returns a shallow copy so we never mutate the map shared with
// the prefs store (prefs.Get returns the map header by value).
func cloneHostTools(m map[string]prefs.HostTools) map[string]prefs.HostTools {
	nm := make(map[string]prefs.HostTools, len(m)+1)
	for k, v := range m {
		nm[k] = v
	}
	return nm
}

// GetDiskUsage returns all real filesystem mounts on a connected tunnel's host.
func (a *App) GetDiskUsage(tunnelID string) ([]sysstats.DiskMount, error) {
	out, err := a.runRemote(tunnelID, "df -P -B1")
	if err != nil {
		return nil, err
	}
	mounts := sysstats.ParseDf(out)
	if mounts == nil {
		mounts = []sysstats.DiskMount{}
	}
	return mounts, nil
}

// ListDockerContainers returns all docker containers on a connected tunnel's
// host. If docker is present but not usable (daemon down, permission denied),
// the error carries docker's own message for display.
//
// When withStats is true the rows are enriched with live CPU/RAM usage via
// `docker stats`, which is comparatively slow; callers should request it only
// when needed (e.g. sorting by CPU/RAM) so the plain listing stays fast.
func (a *App) ListDockerContainers(tunnelID string, withStats bool) ([]sysstats.DockerContainer, error) {
	out, err := a.runRemoteTimeout(tunnelID, "docker ps -a --format '{{json .}}'", dockerCommandTimeout)
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, err
	}
	containers := sysstats.ParseDockerPs(out)
	if containers == nil {
		containers = []sysstats.DockerContainer{}
	}

	// Live usage is best-effort: a stats failure (or no running containers)
	// just leaves the usage fields empty.
	if withStats {
		if statsOut, statsErr := a.runRemoteTimeout(tunnelID,
			"docker stats --no-stream --format '{{json .}}'", dockerCommandTimeout); statsErr == nil {
			sysstats.MergeDockerStats(containers, statsOut)
		}
	}

	return containers, nil
}

// GetDockerContainerDetails returns the expanded view of a single container:
// inspect metadata (ports, mounts, networks, command) plus live usage from
// `docker stats` when the container is running. containerID may be a container
// ID or name.
func (a *App) GetDockerContainerDetails(tunnelID, containerID string) (sysstats.DockerContainerDetails, error) {
	id := shellSingleQuote(containerID)
	out, err := a.runRemoteTimeout(tunnelID, "docker inspect --format '{{json .}}' "+id, dockerCommandTimeout)
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg != "" {
			return sysstats.DockerContainerDetails{}, fmt.Errorf("%s", msg)
		}
		return sysstats.DockerContainerDetails{}, err
	}

	det, ok := sysstats.ParseDockerInspect(out)
	if !ok {
		return sysstats.DockerContainerDetails{}, fmt.Errorf("could not parse container details")
	}

	if det.State == "running" {
		// Usage is best-effort; a stats failure shouldn't hide the metadata.
		if statsOut, statsErr := a.runRemoteTimeout(tunnelID,
			"docker stats --no-stream --format '{{json .}}' "+id, dockerCommandTimeout); statsErr == nil {
			sysstats.ParseDockerStats(statsOut, &det)
		}
	}

	return det, nil
}

// shellSingleQuote wraps s in single quotes for safe interpolation into a remote
// shell command, escaping any embedded single quotes.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// OpenCommandTerminal opens an interactive PTY running a single command (e.g.
// htop) for the given tunnel, reusing the terminal event plumbing.
func (a *App) OpenCommandTerminal(tunnelID, command string) (string, error) {
	cfg, ok := a.store.GetTunnel(tunnelID)
	if !ok {
		return "", &tunnelNotFoundError{tunnelID}
	}
	sessionID, err := a.termMgr.OpenCommandSession(cfg, command,
		func(sessionID, data string) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "terminal:output", map[string]any{
					"sessionId": sessionID,
					"data":      data,
				})
			}
		},
		func(sessionID string) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "terminal:closed", map[string]any{
					"sessionId": sessionID,
				})
			}
		},
	)
	if err != nil {
		return "", fmt.Errorf("opening %q for %s: %w", command, cfg.Name, err)
	}
	return sessionID, nil
}

// GetShowResourceStats reports whether the inline CPU/RAM widget is enabled.
func (a *App) GetShowResourceStats() bool {
	return a.prefs.Get().ShowResourceStats
}

// SetShowResourceStats enables or disables the inline CPU/RAM widget.
func (a *App) SetShowResourceStats(enabled bool) error {
	p := a.prefs.Get()
	p.ShowResourceStats = enabled
	return a.prefs.Set(p)
}

// GetCloseToTray returns whether the window close button hides to tray (true)
// or quits the application (false).
func (a *App) GetCloseToTray() bool {
	return a.prefs.Get().CloseToTray
}

// SetCloseToTray configures the close button behaviour and persists the setting.
func (a *App) SetCloseToTray(enabled bool) error {
	p := a.prefs.Get()
	p.CloseToTray = enabled
	return a.prefs.Set(p)
}
