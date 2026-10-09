//go:build linux

package composition

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestInstanceLeaseProcess(t *testing.T) {
	dir := os.Getenv("INSTANCE_LEASE_TEST_DIR")
	if dir == "" {
		return
	}
	store, err := openAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	os.Stdout.WriteString("LEASE_READY\n")
	select {}
}

func crashInstance(t *testing.T, dir string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestInstanceLeaseProcess$", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), "INSTANCE_LEASE_TEST_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "LEASE_READY\n" {
			t.Fatalf("lease helper did not start: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lease helper startup timed out")
	}
	if other, err := openAudit(dir); !errors.Is(err, ErrStorage) {
		if other != nil {
			_ = other.close()
		}
		t.Fatal("active instance lease was bypassed", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
}

func TestInstanceLeaseCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	audit := []byte("[]")
	if err := os.WriteFile(filepath.Join(dir, "audit.json"), audit, 0600); err != nil {
		t.Fatal(err)
	}
	crashInstance(t, dir)
	store, err := openAudit(dir)
	if err != nil {
		t.Fatal("restart after proven orphaned lease failed", err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal("idempotent close", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.json"))
	if err != nil || !bytes.Equal(data, audit) {
		t.Fatal("recovery changed audit", err)
	}
	store, err = openAudit(dir)
	if err != nil {
		t.Fatal("clean restart failed", err)
	}
	defer store.close()
}

func TestInstanceLeaseUnknownOwnershipFailsClosed(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "instance.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	if store, err := openAudit(dir); !errors.Is(err, ErrStorage) {
		if store != nil {
			_ = store.close()
		}
		t.Fatal("legacy lock without ownership accepted", err)
	}
	if info, err := os.Stat(lock); err != nil || !info.IsDir() {
		t.Fatal("unknown lock removed", err)
	}
}

func TestInstanceLeaseRecoveryPreservesAuditFailClosed(t *testing.T) {
	for _, name := range []string{"audit.pending", "audit.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			crashInstance(t, dir)
			path := filepath.Join(dir, name)
			data := []byte("invalid")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if store, err := openAudit(dir); !errors.Is(err, ErrStorage) {
				if store != nil {
					_ = store.close()
				}
				t.Fatal("unsafe audit recovery accepted", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatal("unsafe audit changed", err)
			}
		})
	}
}

func TestInstanceLeaseRejectsUntrustedGuard(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "permissions", "replaced"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			guard := filepath.Join(dir, ".instance.lease")
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(target, guard); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(target, guard); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.WriteFile(guard, nil, 0644); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				crashInstance(t, dir)
				// Keep the original inode alive so replacement cannot reuse it.
				if err := os.Rename(guard, guard+".original"); err != nil {
					t.Fatal(err)
				}
			}
			if store, err := openAudit(dir); !errors.Is(err, ErrStorage) {
				if store != nil {
					_ = store.close()
				}
				t.Fatal("untrusted lease guard accepted", err)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "unchanged" {
				t.Fatal("target changed", err)
			}
			if kind == "replaced" {
				if _, err := os.Stat(filepath.Join(dir, "instance.lock", "owner")); err != nil {
					t.Fatal("unknown owner removed", err)
				}
			}
		})
	}
}

func TestInstanceLeaseClosePreservesChangedOwner(t *testing.T) {
	dir := t.TempDir()
	store, err := openAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(dir, "instance.lock", "owner")
	if err := os.WriteFile(owner, []byte("unknown-owner"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); !errors.Is(err, ErrStorage) {
		t.Fatal("changed ownership accepted", err)
	}
	data, err := os.ReadFile(owner)
	if err != nil || string(data) != "unknown-owner" {
		t.Fatal("changed owner removed", err)
	}
}
