package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const runtimeAuth = `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"SECRET_ID","access_token":"SECRET_ACCESS","refresh_token":"SECRET_REFRESH"}}`

// This fake represents a driver-owned container tmpfs, never a caller's host home.
type memoryRuntimeHome struct {
	auth, config []byte
	removed      bool
	fail         string
	captureKind  string
}

func TestRuntimeHomeRejectsUnsafeContainerCapture(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "outside", "extra", "permissions", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			s := testRuntimeStore(t)
			ctx := context.Background()
			s.StoreSession(ctx, []byte(runtimeAuth))
			m := &memoryRuntimeHome{captureKind: kind}
			l, err := s.Materialize(ctx, m)
			if err != nil {
				t.Fatal(err)
			}
			if err = l.Finish(ctx); err == nil {
				t.Fatal("unsafe container auth accepted")
			}
			if !m.removed {
				t.Fatal("unsafe capture not cleaned")
			}
		})
	}
}

func (m *memoryRuntimeHome) Prepare(ctx context.Context, archive io.Reader) error {
	if m.fail == "prepare" {
		return errors.New("SECRET_REFRESH")
	}
	r := tar.NewReader(archive)
	for _, name := range []string{"auth.json", "config.toml"} {
		h, err := r.Next()
		if err != nil || h.Name != name || h.Mode != 0600 || h.Typeflag != tar.TypeReg {
			return errors.New("invalid controlled archive")
		}
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		if name == "auth.json" {
			m.auth = data
		} else {
			m.config = data
		}
	}
	if _, err := r.Next(); err != io.EOF {
		return errors.New("extra materialized file")
	}
	return nil
}
func (m *memoryRuntimeHome) Capture(ctx context.Context) (io.ReadCloser, error) {
	if m.fail == "capture" {
		return nil, errors.New("SECRET_REFRESH")
	}
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	h := &tar.Header{Name: "auth.json", Mode: 0600, Size: int64(len(m.auth)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
	switch m.captureKind {
	case "symlink":
		h.Typeflag = tar.TypeSymlink
		h.Linkname = "/outside/auth.json"
		h.Size = 0
	case "hardlink":
		h.Typeflag = tar.TypeLink
		h.Linkname = "/outside/auth.json"
		h.Size = 0
	case "directory":
		h.Typeflag = tar.TypeDir
		h.Size = 0
	case "outside":
		h.Name = "../auth.json"
	case "permissions":
		h.Mode = 0644
	case "oversized":
		h.Size = runtimeAuthLimit + 1
	}
	w.WriteHeader(h)
	if h.Typeflag == tar.TypeReg {
		w.Write(m.auth)
	}
	if m.captureKind == "extra" {
		w.WriteHeader(&tar.Header{Name: "arbitrary", Mode: 0600, Typeflag: tar.TypeReg})
	}
	w.Close()
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}
func (m *memoryRuntimeHome) Destroy(ctx context.Context) error {
	if m.fail == "destroy" {
		return errors.New("SECRET_REFRESH")
	}
	m.removed = true
	clear(m.auth)
	return nil
}

func TestRuntimeHomeDestroyFailureRetainsExclusion(t *testing.T) {
	s := testRuntimeStore(t)
	ctx := context.Background()
	if err := s.StoreSession(ctx, []byte(runtimeAuth)); err != nil {
		t.Fatal(err)
	}
	m := &memoryRuntimeHome{fail: "destroy"}
	l, err := s.Materialize(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Finish(ctx); !errors.Is(err, ErrRuntimeHomeCleanup) {
		t.Fatal("cleanup error hidden")
	}
	if err = l.Abort(ctx); !errors.Is(err, ErrRuntimeHomeCleanup) {
		t.Fatal("cleanup failure hidden on repeated abort")
	}
	if _, err = s.Materialize(ctx, &memoryRuntimeHome{}); !errors.Is(err, ErrRuntimeAuthBusy) {
		t.Fatal("unsafe writer admitted")
	}
	if err = s.Close(); !errors.Is(err, ErrRuntimeAuthBusy) {
		t.Fatal("active home closed")
	}
	// Only the test releases its fake failed container. Production never clears a
	// retained writer lock automatically, even on process restart.
	m.fail = ""
	m.Destroy(ctx)
	s.release()
}

func TestRuntimeHomeCrossProcessWriter(t *testing.T) {
	if len(os.Args) > 2 && os.Args[len(os.Args)-2] == "runtime-writer-helper" {
		s, err := openRuntimeHome(os.Args[len(os.Args)-1])
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err = s.StoreSession(context.Background(), []byte(runtimeAuth)); !errors.Is(err, ErrRuntimeAuthBusy) {
			t.Fatal("cross-process writer admitted")
		}
		return
	}
	s := testRuntimeStore(t)
	ctx := context.Background()
	if err := s.StoreSession(ctx, []byte(runtimeAuth)); err != nil {
		t.Fatal(err)
	}
	l, err := s.Materialize(ctx, &memoryRuntimeHome{})
	if err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, os.Args[0], "-test.run=^TestRuntimeHomeCrossProcessWriter$", "--", "runtime-writer-helper", s.path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("writer process failed: %v (%s)", err, output)
	}
	if err = l.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.StoreSession(ctx, []byte(runtimeAuth)); err != nil {
		t.Fatal("writer did not resume")
	}
}

func TestRuntimeHomeRejectsUnsafePermissionsAndMissingSession(t *testing.T) {
	s := testRuntimeStore(t)
	ctx := context.Background()
	m := &memoryRuntimeHome{}
	if _, err := s.Materialize(ctx, m); err == nil || !m.removed {
		t.Fatal("missing session did not fail closed")
	}
	if err := s.StoreSession(ctx, []byte(runtimeAuth)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(s.path, "auth.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Materialize(ctx, &memoryRuntimeHome{}); err == nil {
		t.Fatal("public auth file accepted")
	}
	if err := s.StoreSession(ctx, []byte(runtimeAuth)); err == nil {
		t.Fatal("unsafe auth overwritten")
	}
	if err := os.Chmod(s.path, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := openRuntimeHome(s.path); err == nil {
		t.Fatal("public store accepted")
	}
}

func TestRuntimeHomeSafeLeaseDiagnosticsAndLifecycle(t *testing.T) {
	s := testRuntimeStore(t)
	ctx := context.Background()
	s.StoreSession(ctx, []byte(runtimeAuth))
	l, err := s.Materialize(ctx, &memoryRuntimeHome{})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, l), "SECRET") || strings.Contains(fmt.Sprintf(format, s), s.path) {
			t.Fatal("sensitive diagnostics")
		}
	}
	if err := s.Close(); !errors.Is(err, ErrRuntimeAuthBusy) {
		t.Fatal("close bypassed lease")
	}
	if err := l.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l.Finish(ctx); err == nil {
		t.Fatal("capture after cleanup")
	}
	if err := l.Abort(ctx); err != nil {
		t.Fatal("abort not idempotent")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreSession(ctx, []byte(runtimeAuth)); err == nil {
		t.Fatal("closed home accepted write")
	}
}

// Models the lease driver's stopped-writer boundary; transport ownership itself
// is exercised in infrastructure's TestCodexLeaseTransportClosePreservesContainer.
func TestRuntimeHomeCaptureBeforeFinalDestroy(t *testing.T) {
	for _, failure := range []string{"", "capture", "persistence"} {
		t.Run(failure, func(t *testing.T) {
			s := testRuntimeStore(t)
			ctx := context.Background()
			if s.StoreSession(ctx, []byte(runtimeAuth)) != nil {
				t.Fatal("store failed")
			}
			m := &memoryRuntimeHome{}
			lease, err := s.Materialize(ctx, m)
			if err != nil {
				t.Fatal("materialization failed")
			}
			if m.removed {
				t.Fatal("materialization destroyed before capture")
			}
			updated := strings.Replace(runtimeAuth, "SECRET_REFRESH", "SECRET_UPDATED", 1)
			m.auth = []byte(updated)
			if failure == "capture" {
				m.fail = "capture"
			}
			if failure == "persistence" {
				failRuntimePersistence(s, "replace")
			}
			err = lease.Finish(ctx)
			if (err != nil) != (failure != "") {
				t.Fatal("capture/persistence result hidden")
			}
			if !m.removed {
				t.Fatal("final destruction missing")
			}
			if lease.Abort(ctx) != nil || lease.Abort(ctx) != nil {
				t.Fatal("double abort failed")
			}
			persisted, err := s.read()
			defer clear(persisted)
			want := updated
			if failure != "" {
				want = runtimeAuth
			}
			if err != nil || string(persisted) != want {
				t.Fatal("authoritative session lost")
			}
		})
	}
}
func testRuntimeStore(t *testing.T) *CodexRuntimeHome {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := openRuntimeHome(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func failRuntimePersistence(s *CodexRuntimeHome, stage string) {
	failure := func() error { return errors.New("SECRET_REFRESH") }
	switch stage {
	case "write":
		s.ops.write = func(f *os.File, b []byte) error { f.Write(b[:len(b)/2]); return failure() }
	case "sync":
		s.ops.sync = func(*os.File) error { return failure() }
	case "close":
		s.ops.close = func(f *os.File) error { f.Close(); return failure() }
	case "replace":
		s.ops.replace = func(*os.Root, string, string) error { return failure() }
	}
}
func TestRuntimeHomeValidation(t *testing.T) {
	for _, material := range []string{runtimeAuth, "", "{", `{}`, strings.Replace(runtimeAuth, `"chatgpt"`, `"api_key"`, 1), strings.Replace(runtimeAuth, `null`, `"SECRET_KEY"`, 1), strings.Replace(runtimeAuth, `"auth_mode":"chatgpt",`, "", 1), strings.Replace(runtimeAuth, `"chatgpt"`, `"chatgptAuthTokens"`, 1), strings.Replace(runtimeAuth, `"tokens":`, `"external":{},"tokens":`, 1), strings.Replace(runtimeAuth, `"SECRET_ACCESS"`, `""`, 1), strings.Replace(runtimeAuth, `"tokens":`, `"auth_mode":"chatgpt","tokens":`, 1)} {
		t.Run(fmt.Sprint(len(material))+material[:min(10, len(material))], func(t *testing.T) {
			s := testRuntimeStore(t)
			err := s.StoreSession(context.Background(), []byte(material))
			if (err == nil) != (material == runtimeAuth) {
				t.Fatal("validation mismatch")
			}
			if strings.Contains(fmt.Sprintf("%+v %#v", err, s), "SECRET") {
				t.Fatal("secret in diagnostics")
			}
		})
	}
}
func TestRuntimeHomeLifecycleRestartAndSingleWriter(t *testing.T) {
	s := testRuntimeStore(t)
	ctx := context.Background()
	if err := s.StoreSession(ctx, []byte(runtimeAuth)); err != nil {
		t.Fatal(err)
	}
	other, err := openRuntimeHome(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	m := &memoryRuntimeHome{}
	lease, err := s.Materialize(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if string(m.auth) != runtimeAuth || string(m.config) != runtimeHomeConfig {
		t.Fatal("material/config mismatch")
	}
	if _, err := other.Materialize(ctx, &memoryRuntimeHome{}); err == nil {
		t.Fatal("second writer allowed")
	}
	if err := other.StoreSession(ctx, []byte(runtimeAuth)); err == nil {
		t.Fatal("session import bypassed writer")
	}
	updated := strings.Replace(runtimeAuth, "SECRET_REFRESH", "SECRET_UPDATED", 1)
	m.auth = []byte(updated)
	if err := lease.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if !m.removed {
		t.Fatal("cleanup missing")
	}
	restarted, err := openRuntimeHome(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	n := &memoryRuntimeHome{}
	next, err := restarted.Materialize(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if string(n.auth) != updated {
		t.Fatal("restart lost refresh")
	}
	if err := next.Finish(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestRuntimeHomeUnchangedDoesNotReplace(t *testing.T) {
	s := testRuntimeStore(t)
	ctx := context.Background()
	if err := s.StoreSession(ctx, []byte(runtimeAuth)); err != nil {
		t.Fatal(err)
	}
	failRuntimePersistence(s, "replace")
	m := &memoryRuntimeHome{}
	l, err := s.Materialize(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Finish(ctx); err != nil {
		t.Fatal("unchanged material rewritten")
	}
}
func TestRuntimeHomeAtomicFailuresPreservePrevious(t *testing.T) {
	for _, stage := range []string{"write", "sync", "close", "replace"} {
		t.Run(stage, func(t *testing.T) {
			s := testRuntimeStore(t)
			ctx := context.Background()
			if err := s.StoreSession(ctx, []byte(runtimeAuth)); err != nil {
				t.Fatal(err)
			}
			m := &memoryRuntimeHome{}
			l, err := s.Materialize(ctx, m)
			if err != nil {
				t.Fatal(err)
			}
			m.auth = []byte(strings.Replace(runtimeAuth, "SECRET_REFRESH", "SECRET_UPDATED", 1))
			failRuntimePersistence(s, stage)
			if err := l.Finish(ctx); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("persistence failure hidden/leaked")
			}
			if !m.removed {
				t.Fatal("failure cleanup missing")
			}
			persisted, err := os.ReadFile(filepath.Join(s.path, "auth.json"))
			if err != nil || string(persisted) != runtimeAuth {
				t.Fatal("previous auth lost")
			}
			s.ops = defaultRuntimePersistenceOps()
			next, err := s.Materialize(ctx, &memoryRuntimeHome{})
			if err != nil {
				t.Fatal("writer not released")
			}
			next.Abort(ctx)
		})
	}
}
func TestRuntimeHomeCleanupFailures(t *testing.T) {
	for _, stage := range []string{"prepare", "capture", "invalid", "cancel", "abort"} {
		t.Run(stage, func(t *testing.T) {
			s := testRuntimeStore(t)
			ctx := context.Background()
			s.StoreSession(ctx, []byte(runtimeAuth))
			m := &memoryRuntimeHome{fail: stage}
			l, err := s.Materialize(ctx, m)
			if stage == "prepare" {
				if err == nil || !m.removed {
					t.Fatal("partial prepare not cleaned")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if stage == "invalid" {
				m.auth = []byte("SECRET_INVALID")
			}
			if stage == "cancel" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			if stage == "abort" {
				err = l.Abort(ctx)
			} else {
				err = l.Finish(ctx)
			}
			if stage != "abort" && err == nil {
				t.Fatal("failure hidden")
			}
			if !m.removed {
				t.Fatal("cleanup missing")
			}
		})
	}
}
func TestRuntimeHomeRejectsSymlinkAndNonRegularMaterial(t *testing.T) {
	s := testRuntimeStore(t)
	ctx := context.Background()
	s.StoreSession(ctx, []byte(runtimeAuth))
	outside := filepath.Join(t.TempDir(), "auth.json")
	os.WriteFile(outside, []byte(runtimeAuth), 0600)
	os.Remove(filepath.Join(s.path, "auth.json"))
	if err := os.Symlink(outside, filepath.Join(s.path, "auth.json")); err != nil {
		t.Skip("symlink unavailable")
	}
	if _, err := s.Materialize(ctx, &memoryRuntimeHome{}); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := openRuntimeHome(filepath.Join(s.path, "auth.json")); err == nil {
		t.Fatal("arbitrary auth source accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	os.Symlink(s.path, alias)
	if _, err := openRuntimeHome(alias); err == nil {
		t.Fatal("store alias accepted")
	}
}
func TestRuntimeHomeHasNoCallerPathOrDesktopSource(t *testing.T) {
	// Public constructor has no path/source parameter. Material import is bytes only;
	// path strings are rejected as malformed auth, never opened or followed.
	var constructor func() (*CodexRuntimeHome, error) = NewCodexRuntimeHome
	_ = constructor
	s := testRuntimeStore(t)
	for _, path := range []string{`C:\Users\evert\.codex\auth.json`, "/workspace/auth.json", "/arbitrary/auth.json"} {
		if err := s.StoreSession(context.Background(), []byte(path)); err == nil {
			t.Fatal("path imported")
		}
	}
}
