package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"ssh-tunnel-manager/internal/config"
	"ssh-tunnel-manager/internal/dns"
	"ssh-tunnel-manager/internal/ssh"
)

type fakeSharedRegistry struct{}

func (*fakeSharedRegistry) Allocate(domain string, port int) (dns.Entry, error) {
	return dns.Entry{Domain: domain, IP: net.IPv4(127, 0, 1, 42), Port: port}, nil
}
func (*fakeSharedRegistry) Release(string) {}
func (*fakeSharedRegistry) Block(net.IP)   {}
func (*fakeSharedRegistry) Close() error   { return nil }

func TestHandlePortlessDNSStartFailureDegradesAddressConflict(t *testing.T) {
	app := &App{
		manager: ssh.NewManager(func(ssh.StatusEvent) {}, nil),
		logBuf:  make(map[string][]config.LogEntry),
	}
	cfg := config.TunnelConfig{ID: "bastion", Name: "bastion"}
	err := fmt.Errorf("binding UDP: %w", syscall.EADDRINUSE)

	if !app.handlePortlessDNSStartFailure(cfg, err) {
		t.Fatal("address conflict should degrade instead of remaining fatal")
	}
	if app.portlessFallback == nil {
		t.Fatal("address conflict should persist the fallback warning")
	}
	logs := app.logBuf[cfg.ID]
	if len(logs) != 1 || logs[0].Level != "warn" {
		t.Fatalf("fallback logs = %+v, want one warning", logs)
	}
	if !strings.Contains(logs[0].Message, "duplicate ports") {
		t.Fatalf("fallback log = %q", logs[0].Message)
	}
}

func TestHandlePortlessDNSStartFailureJoinsCompatibleOwner(t *testing.T) {
	shared := &fakeSharedRegistry{}
	app := &App{
		manager: ssh.NewManager(func(ssh.StatusEvent) {}, nil),
		logBuf:  make(map[string][]config.LogEntry),
		connectSharedDNS: func() (sharedPortlessRegistry, error) {
			return shared, nil
		},
	}
	cfg := config.TunnelConfig{ID: "bastion", Name: "bastion"}
	err := fmt.Errorf("binding TCP: %w", syscall.EADDRINUSE)

	if !app.handlePortlessDNSStartFailure(cfg, err) {
		t.Fatal("compatible DNS owner should handle the address conflict")
	}
	if app.sharedDNSRegistry != shared {
		t.Fatal("app did not retain the shared Portless registry")
	}
	if app.portlessFallback != nil {
		t.Fatal("compatible DNS owner must preserve Portless instead of degrading")
	}
	logs := app.logBuf[cfg.ID]
	if len(logs) != 1 || logs[0].Level != "info" {
		t.Fatalf("shared registry logs = %+v, want one info entry", logs)
	}
}

func TestHandlePortlessDNSStartFailureKeepsOtherErrorsFatal(t *testing.T) {
	app := &App{
		manager: ssh.NewManager(func(ssh.StatusEvent) {}, nil),
		logBuf:  make(map[string][]config.LogEntry),
	}
	cfg := config.TunnelConfig{ID: "bastion", Name: "bastion"}

	if app.handlePortlessDNSStartFailure(cfg, errors.New("unexpected DNS startup failure")) {
		t.Fatal("non-bind DNS error must remain fatal")
	}
	if app.portlessFallback != nil {
		t.Fatal("non-bind DNS error must not show the fallback warning")
	}
	if len(app.logBuf[cfg.ID]) != 0 {
		t.Fatalf("unexpected fallback logs: %+v", app.logBuf[cfg.ID])
	}
}

func TestHandlePortlessSetupPersistenceFailureDegradesWithRecommendation(t *testing.T) {
	app := &App{
		manager: ssh.NewManager(func(ssh.StatusEvent) {}, nil),
		logBuf:  make(map[string][]config.LogEntry),
	}
	cfg := config.TunnelConfig{ID: "bastion", Name: "bastion"}
	err := &dns.PersistenceError{
		Message:        "the helper service is approved, but its prerequisites never took effect",
		Recommendation: "reinstall the app",
		Command:        "sudo sfltool resetbtm",
	}

	if !app.handlePortlessSetupPersistenceFailure(cfg, err) {
		t.Fatal("a PersistenceError should degrade instead of remaining fatal")
	}
	if app.portlessFallback == nil {
		t.Fatal("PersistenceError should persist the fallback warning")
	}
	if app.portlessFallback.Recommendation != err.Recommendation || app.portlessFallback.Command != err.Command {
		t.Fatalf("fallback = %+v, want recommendation/command carried over from %+v", app.portlessFallback, err)
	}
	logs := app.logBuf[cfg.ID]
	if len(logs) != 1 || logs[0].Level != "warn" {
		t.Fatalf("fallback logs = %+v, want one warning", logs)
	}
}

func TestHandlePortlessSetupPersistenceFailureKeepsOtherErrorsFatal(t *testing.T) {
	app := &App{
		manager: ssh.NewManager(func(ssh.StatusEvent) {}, nil),
		logBuf:  make(map[string][]config.LogEntry),
	}
	cfg := config.TunnelConfig{ID: "bastion", Name: "bastion"}

	if app.handlePortlessSetupPersistenceFailure(cfg, errors.New("resolving current executable: no such file")) {
		t.Fatal("a plain error must remain fatal")
	}
	if app.portlessFallback != nil {
		t.Fatal("a plain error must not show the fallback warning")
	}
	if len(app.logBuf[cfg.ID]) != 0 {
		t.Fatalf("unexpected fallback logs: %+v", app.logBuf[cfg.ID])
	}
}
