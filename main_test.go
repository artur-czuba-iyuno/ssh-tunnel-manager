package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenLogFileAtCreatesPrivateDirAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ssh-tunnel-manager")
	f := openLogFileAt(dir)
	if f == nil {
		t.Fatal("openLogFileAt returned nil")
	}
	defer f.Close()

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("log dir mode = %o, want 0700", perm)
	}
	fileInfo, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("log file mode = %o, want 0600", perm)
	}
}

func TestOpenLogFileAtTightensExistingLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := openLogFileAt(dir)
	if f == nil {
		t.Fatal("openLogFileAt returned nil")
	}
	defer f.Close()

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("pre-existing log dir mode = %o, want tightened to 0700", perm)
	}
	fileInfo, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("pre-existing log file mode = %o, want tightened to 0600", perm)
	}
}

func TestRotateLogFileIfLargeRotatesOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, make([]byte, maxLogFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}

	rotateLogFileIfLarge(path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected the oversized log to be rotated away, stat err = %v", err)
	}
	if _, err := os.Stat(path + ".old"); err != nil {
		t.Fatalf("expected a rotated %s.old: %v", path, err)
	}
}

func TestRotateLogFileIfLargeLeavesSmallFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("small\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rotateLogFileIfLarge(path)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a small log should not be rotated away: %v", err)
	}
	if _, err := os.Stat(path + ".old"); !os.IsNotExist(err) {
		t.Fatal("no .old file should exist for a small log")
	}
}
