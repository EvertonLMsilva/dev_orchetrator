package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"dev-orchestrator/internal/infrastructure"
)

const runtimeHomeConfig = "cli_auth_credentials_store = \"file\"\nforced_login_method = \"chatgpt\"\n[features]\nplugins = false\n"
const runtimeAuthLimit = 1024 * 1024
const runtimeStorePath = "/var/lib/dev-orchestrator/runtime-auth"

var (
	ErrRuntimeAuthUnavailable = errors.New("runtime auth: unavailable")
	ErrRuntimeAuthInvalid     = errors.New("runtime auth: invalid material")
	ErrRuntimeAuthBusy        = errors.New("runtime auth: writer busy")
	ErrRuntimeAuthPersistence = errors.New("runtime auth: persistence failed")
	ErrRuntimeHomePreparation = errors.New("runtime auth: preparation failed")
	ErrRuntimeHomeCapture     = errors.New("runtime auth: capture failed")
	ErrRuntimeHomeCleanup     = errors.New("runtime auth: cleanup failed")
)

// RuntimeHomeContainer is a trusted infrastructure driver owning one isolated
// Linux container tmpfs at /run/codex-auth (0700). Prepare writes only auth.json
// (0600) and config.toml (0600) from the supplied controlled archive, without
// argv/env or host staging. Capture runs only after the app-server has stopped,
// archives only auth.json with its actual filesystem metadata, and never follows
// symlinks/reparse aliases. RuntimeHome checks the archive before importing auth.
// Neither operation accepts host paths, imports other files or logs payloads.
// Destroy destroys the owned tmpfs even after partial preparation. Methods must
// honor context and must not retain buffers. Destroy stops/removes the owned
// container resources; Finish must precede external container removal.
// The device-login harness composes the infrastructure-owned Docker driver.
type RuntimeHomeContainer interface {
	Prepare(context.Context, io.Reader) error
	Capture(context.Context) (io.ReadCloser, error)
	Destroy(context.Context) error
}

// CodexRuntimeHome is independent of bootstrap and desktop AuthorizedCodexHome.
// The public constructor has no configurable path, auth source or environment
// fallback. It never inspects CODEX_HOME or imports a caller-selected host file.
type CodexRuntimeHome struct {
	mu     sync.Mutex
	path   string
	root   *os.Root
	active bool
	ops    runtimePersistenceOps
}
type runtimePersistenceOps struct {
	write   func(*os.File, []byte) error
	sync    func(*os.File) error
	close   func(*os.File) error
	replace func(*os.Root, string, string) error
}

func defaultRuntimePersistenceOps() runtimePersistenceOps {
	return runtimePersistenceOps{
		write: func(f *os.File, b []byte) error {
			n, e := f.Write(b)
			if e == nil && n != len(b) {
				e = io.ErrShortWrite
			}
			return e
		},
		sync:    func(f *os.File) error { return f.Sync() },
		close:   func(f *os.File) error { return f.Close() },
		replace: func(r *os.Root, a, b string) error { return r.Rename(a, b) },
	}
}

// NewCodexRuntimeHome uses a fixed, private Linux service location. Unsupported
// host platforms fail closed; Linux/Docker is the runtime/validation authority.
func NewCodexRuntimeHome() (*CodexRuntimeHome, error) {
	if runtime.GOOS != "linux" {
		return nil, ErrRuntimeAuthUnavailable
	}
	parent, err := os.OpenRoot("/var/lib")
	if err != nil {
		return nil, ErrRuntimeAuthUnavailable
	}
	defer parent.Close()
	if err = privateDirectory(parent, "dev-orchestrator"); err != nil {
		return nil, err
	}
	service, err := parent.OpenRoot("dev-orchestrator")
	if err != nil {
		return nil, ErrRuntimeAuthUnavailable
	}
	defer service.Close()
	if err = privateDirectory(service, "runtime-auth"); err != nil {
		return nil, err
	}
	return openRuntimeHome(runtimeStorePath)
}
func privateDirectory(root *os.Root, name string) error {
	err := root.Mkdir(name, 0700)
	created := err == nil
	if err != nil && !errors.Is(err, os.ErrExist) {
		return ErrRuntimeAuthUnavailable
	}
	info, err := root.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return ErrRuntimeAuthUnavailable
	}
	if created && syncRuntimeDirectory(root) != nil {
		return ErrRuntimeAuthUnavailable
	}
	return nil
}

func syncRuntimeDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return ErrRuntimeAuthPersistence
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil || closeErr != nil {
		return ErrRuntimeAuthPersistence
	}
	return nil
}

// Private constructor is also the filesystem test seam. No exported API accepts
// desktop/workspace/arbitrary paths. The service location is never a workspace.
func openRuntimeHome(path string) (*CodexRuntimeHome, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrRuntimeAuthUnavailable
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Clean(path) {
		return nil, ErrRuntimeAuthUnavailable
	}
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode().Perm()&0077 != 0 {
		return nil, ErrRuntimeAuthUnavailable
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrRuntimeAuthUnavailable
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, ErrRuntimeAuthUnavailable
	}
	return &CodexRuntimeHome{path: path, root: root, ops: defaultRuntimePersistenceOps()}, nil
}
func (*CodexRuntimeHome) Format(s fmt.State, _ rune) { io.WriteString(s, "CodexRuntimeHome[redacted]") }
func (s *CodexRuntimeHome) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return ErrRuntimeAuthBusy
	}
	if s.root == nil {
		return nil
	}
	err := s.root.Close()
	s.root = nil
	if err != nil {
		return ErrRuntimeHomeCleanup
	}
	return nil
}

// mkdir is the inter-process exclusion primitive, not just a Go mutex. A crash
// leaves the writer lock in place and fails closed. There is no automatic stale
// lock deletion or destructive recovery. Hold it through capture AND cleanup.
func (s *CodexRuntimeHome) acquire(ctx context.Context) error {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ErrRuntimeAuthUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return ErrRuntimeAuthUnavailable
	}
	if s.active {
		return ErrRuntimeAuthBusy
	}
	info, err := os.Lstat(s.path)
	opened, openErr := s.root.Stat(".")
	if err != nil || openErr != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !os.SameFile(info, opened) {
		return ErrRuntimeAuthUnavailable
	}
	if err = s.root.Mkdir("writer.lock", 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrRuntimeAuthBusy
		}
		return ErrRuntimeAuthUnavailable
	}
	s.active = true
	return nil
}
func (s *CodexRuntimeHome) release() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root.Remove("writer.lock") != nil {
		return ErrRuntimeHomeCleanup
	}
	s.active = false
	return nil
}

// StoreSession accepts only validated runtime bootstrap material, never a path.
// Callers are trusted infrastructure bootstrap code; secrets remain here. It
// takes the same writer lock as execution and never triggers login or fallback.
func (s *CodexRuntimeHome) StoreSession(ctx context.Context, material []byte) (err error) {
	if !infrastructure.ValidRuntimeCodexAuth(material) {
		return ErrRuntimeAuthInvalid
	}
	if err = s.acquire(ctx); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.release()) }()
	return s.persist(ctx, material)
}
func (s *CodexRuntimeHome) read() ([]byte, error) {
	info, err := s.root.Lstat("auth.json")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > runtimeAuthLimit {
		return nil, ErrRuntimeAuthUnavailable
	}
	f, err := s.root.Open("auth.json")
	if err != nil {
		return nil, ErrRuntimeAuthUnavailable
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		f.Close()
		return nil, ErrRuntimeAuthUnavailable
	}
	data, readErr := io.ReadAll(io.LimitReader(f, runtimeAuthLimit+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || !infrastructure.ValidRuntimeCodexAuth(data) {
		clear(data)
		return nil, ErrRuntimeAuthInvalid
	}
	return data, nil
}
func (s *CodexRuntimeHome) persist(ctx context.Context, material []byte) (resultErr error) {
	if ctx == nil || ctx.Err() != nil {
		return ErrRuntimeAuthPersistence
	}
	// Never replace a symlink, directory or unexpectedly accessible auth file.
	if info, err := s.root.Lstat("auth.json"); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return ErrRuntimeAuthPersistence
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrRuntimeAuthPersistence
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrRuntimeAuthPersistence
	}
	name := fmt.Sprintf(".auth-%x.tmp", nonce)
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrRuntimeAuthPersistence
	}
	closed := false
	defer func() {
		if !closed {
			f.Close()
		}
		if err := s.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, ErrRuntimeHomeCleanup)
		}
	}()
	if s.ops.write(f, material) != nil || s.ops.sync(f) != nil {
		return ErrRuntimeAuthPersistence
	}
	if s.ops.close(f) != nil {
		return ErrRuntimeAuthPersistence
	}
	closed = true
	if ctx.Err() != nil || s.ops.replace(s.root, name, "auth.json") != nil {
		return ErrRuntimeAuthPersistence
	}
	// A successful result means rename was durably committed, not merely visible.
	return syncRuntimeDirectory(s.root)
}

type RuntimeHomeLease struct {
	mu         sync.Mutex
	store      *CodexRuntimeHome
	container  RuntimeHomeContainer
	original   []byte
	done       bool
	cleanupErr error
}

func (*RuntimeHomeLease) Format(s fmt.State, _ rune) { io.WriteString(s, "RuntimeHomeLease[redacted]") }

// Bootstrap creates a config-only materialization for an explicitly initiated
// fresh runtime login. Existing authoritative sessions are never overwritten.
// The lease holds exclusion through validation, persistence and final cleanup.
func (s *CodexRuntimeHome) Bootstrap(ctx context.Context, c RuntimeHomeContainer) (*RuntimeHomeLease, error) {
	if c == nil {
		return nil, ErrRuntimeHomePreparation
	}
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	l := &RuntimeHomeLease{store: s, container: c}
	if _, err := s.root.Lstat("auth.json"); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.Join(ErrRuntimeAuthUnavailable, l.Abort(context.Background()))
	}
	delivery, err := runtimeMaterialArchive(nil)
	defer clear(delivery)
	if err != nil || c.Prepare(ctx, bytes.NewReader(delivery)) != nil || ctx.Err() != nil {
		return nil, errors.Join(ErrRuntimeHomePreparation, l.Abort(context.Background()))
	}
	return l, nil
}
func (s *CodexRuntimeHome) Materialize(ctx context.Context, c RuntimeHomeContainer) (*RuntimeHomeLease, error) {
	if c == nil {
		return nil, ErrRuntimeHomePreparation
	}
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	l := &RuntimeHomeLease{store: s, container: c}
	material, err := s.read()
	if err != nil {
		return nil, errors.Join(err, l.Abort(context.Background()))
	}
	l.original = material
	// Archive names and permissions are runtime-controlled. No caller can choose
	// a destination or add desktop config, and the driver never sees argv secrets.
	delivery, archiveErr := runtimeMaterialArchive(material)
	defer clear(delivery)
	if archiveErr != nil || c.Prepare(ctx, bytes.NewReader(delivery)) != nil || ctx.Err() != nil {
		return nil, errors.Join(ErrRuntimeHomePreparation, l.Abort(context.Background()))
	}
	return l, nil
}

// Finish must run after the workload has stopped. It captures only expected
// auth, validates before comparison, and propagates refresh persistence failures.
// The caller MUST treat any error as an uncommitted execution result.
func (l *RuntimeHomeLease) Finish(ctx context.Context) (err error) {
	if l == nil {
		return ErrRuntimeHomeCapture
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return ErrRuntimeHomeCapture
	}
	defer func() { err = errors.Join(err, l.cleanup()) }()
	if ctx == nil || ctx.Err() != nil {
		return ErrRuntimeHomeCapture
	}
	stream, captureErr := l.container.Capture(ctx)
	if captureErr != nil || stream == nil {
		if stream != nil {
			stream.Close()
		}
		return ErrRuntimeHomeCapture
	}
	data, readErr := captureRuntimeAuth(stream)
	closeErr := stream.Close()
	defer clear(data)
	if readErr != nil || closeErr != nil || ctx.Err() != nil || !infrastructure.ValidRuntimeCodexAuth(data) {
		return ErrRuntimeHomeCapture
	}
	if subtle.ConstantTimeCompare(l.original, data) == 1 {
		return nil
	}
	return l.store.persist(ctx, data)
}
func (l *RuntimeHomeLease) Abort(_ context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return l.cleanupErr
	}
	return l.cleanup()
}
func (l *RuntimeHomeLease) cleanup() (err error) {
	defer func() { l.cleanupErr = err }()
	l.done = true
	clear(l.original)
	l.original = nil
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if l.container.Destroy(ctx) != nil {
		// If destruction failed, retain exclusion: another writer must not run while
		// an old container might still hold this refresh token. Fail closed.
		return ErrRuntimeHomeCleanup
	}
	return l.store.release()
}

func runtimeMaterialArchive(auth []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, file := range []struct {
		name string
		data []byte
	}{{"auth.json", auth}, {"config.toml", []byte(runtimeHomeConfig)}} {
		if file.name == "auth.json" && auth == nil {
			continue
		}
		if w.WriteHeader(&tar.Header{Name: file.name, Mode: 0600, Size: int64(len(file.data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}) != nil {
			clear(buf.Bytes())
			return nil, ErrRuntimeHomePreparation
		}
		if _, err := w.Write(file.data); err != nil {
			clear(buf.Bytes())
			return nil, ErrRuntimeHomePreparation
		}
	}
	if w.Close() != nil {
		clear(buf.Bytes())
		return nil, ErrRuntimeHomePreparation
	}
	return buf.Bytes(), nil
}

func captureRuntimeAuth(stream io.Reader) ([]byte, error) {
	// Bound the entire archive, including hostile extended headers. Only one
	// regular expected filename is accepted. No path is extracted to the host.
	bounded := &io.LimitedReader{R: stream, N: runtimeAuthLimit + 8192}
	r := tar.NewReader(bounded)
	h, err := r.Next()
	if err != nil || h.Name != "auth.json" || h.Typeflag != tar.TypeReg || h.Linkname != "" ||
		h.Mode != 0600 || h.Size <= 0 || h.Size > runtimeAuthLimit || len(h.PAXRecords) != 0 {
		return nil, ErrRuntimeHomeCapture
	}
	data, err := io.ReadAll(r)
	if err != nil || int64(len(data)) != h.Size {
		clear(data)
		return nil, ErrRuntimeHomeCapture
	}
	if _, err = r.Next(); err != io.EOF {
		clear(data)
		return nil, ErrRuntimeHomeCapture
	}
	// tar EOF may precede concatenated/junk archives. Only zero padding is valid.
	padding, err := io.ReadAll(bounded)
	valid := err == nil && bounded.N > 0
	for _, b := range padding {
		valid = valid && b == 0
	}
	clear(padding)
	if !valid {
		clear(data)
		return nil, ErrRuntimeHomeCapture
	}
	return data, nil
}
