//go:build linux

package infrastructure

import (
	"context"
	"crypto/rand"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Descriptors are opened component-by-component with NOFOLLOW. The model
// receives bytes only. No command execution or real-workspace writer exists.
type candidateWorkspace struct {
	mu            sync.Mutex
	root          *os.File
	cleanupParent *os.Root
	name          string
	rootStat      unix.Stat_t
	policy        domain.CandidatePolicy
	before        map[string]domain.CandidateFile
	base          string
	workspace     string
	closed        bool
	written       bool
	invalid       bool
	// A private failure seam used only by same-package deterministic tests.
	beforeWrite func(int) error
}

func candidateDeny() error { return domain.ErrCandidateDenied }
func candidateNewName() (string, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "candidate-" + hex.EncodeToString(random[:]), nil
}
func candidateDirectory(parent int, name string) (*os.File, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func candidateAbsoluteDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, candidateDeny()
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	cur := os.NewFile(uintptr(fd), "/")
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, err := candidateDirectory(int(cur.Fd()), part)
		cur.Close()
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}
func candidateGitAbsent(dir int) error {
	var st unix.Stat_t
	err := unix.Fstatat(dir, ".git", &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return candidateDeny()
}
func candidateParent(root int, target string, create, source bool) (*os.File, string, error) {
	cur, err := candidateDirectory(root, ".")
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(target, "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := candidateDirectory(int(cur.Fd()), part)
		if errors.Is(err, unix.ENOENT) && create {
			err = unix.Mkdirat(int(cur.Fd()), part, 0700)
			if err == nil {
				next, err = candidateDirectory(int(cur.Fd()), part)
			}
		}
		cur.Close()
		if err != nil {
			return nil, "", err
		}
		cur = next
		if source && candidateGitAbsent(int(cur.Fd())) != nil {
			cur.Close()
			return nil, "", candidateDeny()
		}
	}
	return cur, parts[len(parts)-1], nil
}
func candidateRead(ctx context.Context, root int, target string, limit int64, source bool) (domain.CandidateFile, error) {
	if err := ctx.Err(); err != nil {
		return domain.CandidateFile{}, err
	}
	parent, name, err := candidateParent(root, target, false, source)
	if err != nil {
		return domain.CandidateFile{}, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return domain.CandidateFile{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > limit {
		return domain.CandidateFile{}, candidateDeny()
	}
	mode := uint32(before.Mode & 07777)
	if mode != 0600 && mode != 0644 && mode != 0755 {
		return domain.CandidateFile{}, candidateDeny()
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(content)) > limit || unix.Fstat(fd, &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Nlink != after.Nlink || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return domain.CandidateFile{}, candidateDeny()
	}
	if err = ctx.Err(); err != nil {
		return domain.CandidateFile{}, err
	}
	return domain.CandidateFile{Target: target, Identity: domain.CandidateDigest(content), Mode: mode, Content: content}, nil
}
func candidateStateIdentity(policy domain.CandidatePolicy, files map[string]domain.CandidateFile) string {
	type entry struct {
		Target   string
		Identity string
		Mode     uint32
	}
	state := []entry{}
	for _, target := range policy.Targets() {
		f := files[target]
		state = append(state, entry{target, f.Identity, f.Mode})
	}
	data, _ := json.Marshal(struct {
		SchemaVersion int
		Files         []entry
	}{1, state})
	return domain.CandidateDigest(data)
}
func candidateSnapshot(ctx context.Context, root int, p domain.CandidatePolicy) (map[string]domain.CandidateFile, error) {
	required := map[string]bool{}
	for _, s := range p.InputTargets {
		required[s] = true
	}
	out := map[string]domain.CandidateFile{}
	var total int64
	for _, s := range p.Targets() {
		f, err := candidateRead(ctx, root, s, p.Limits.MaxFileBytes, true)
		if errors.Is(err, unix.ENOENT) && !required[s] {
			continue
		}
		if err != nil {
			return nil, err
		}
		total += int64(len(f.Content))
		if total > p.Limits.MaxTotalBytes {
			return nil, candidateDeny()
		}
		out[s] = f
	}
	return out, nil
}
func (f *candidateWorkspaceFactory) Prepare(ctx context.Context, source, identity string, p domain.CandidatePolicy) (ports.CandidateWorkspace, error) {
	if p.Validate() != nil || (domain.CandidateContext{ProjectID: "candidate", TaskID: "snapshot", CorrelationID: identity}).Validate() != nil {
		return nil, candidateDeny()
	}
	p = p.Clone()
	real, err := candidateAbsoluteDirectory(source)
	if err != nil {
		return nil, err
	}
	defer real.Close()
	var realStat unix.Stat_t
	if unix.Fstat(int(real.Fd()), &realStat) != nil {
		return nil, candidateDeny()
	}
	scratch, err := candidateAbsoluteDirectory(f.scratchRoot)
	if err != nil {
		return nil, err
	}
	defer scratch.Close()
	var scratchStat unix.Stat_t
	if unix.Fstat(int(scratch.Fd()), &scratchStat) != nil {
		return nil, candidateDeny()
	}
	// Reject either nesting direction as well as the identical physical root.
	if source == f.scratchRoot || strings.HasPrefix(f.scratchRoot, source+"/") || strings.HasPrefix(source, f.scratchRoot+"/") || realStat.Dev == scratchStat.Dev && realStat.Ino == scratchStat.Ino {
		return nil, candidateDeny()
	}
	before, err := candidateSnapshot(ctx, int(real.Fd()), p)
	if err != nil {
		return nil, err
	}
	again, err := candidateSnapshot(ctx, int(real.Fd()), p)
	if err != nil || candidateStateIdentity(p, before) != candidateStateIdentity(p, again) {
		return nil, candidateDeny()
	}
	// Pin cleanup parent and confirm it is the same no-follow directory.
	cleanupParent, err := os.OpenRoot(f.scratchRoot)
	if err != nil {
		return nil, err
	}
	parentFile, err := cleanupParent.Open(".")
	if err != nil {
		cleanupParent.Close()
		return nil, err
	}
	var pinned unix.Stat_t
	statErr := unix.Fstat(int(parentFile.Fd()), &pinned)
	parentFile.Close()
	if statErr != nil || pinned.Dev != scratchStat.Dev || pinned.Ino != scratchStat.Ino {
		cleanupParent.Close()
		return nil, candidateDeny()
	}
	// Random name and exclusive mkdir relative to the pinned parent.
	name, err := candidateNewName()
	if err != nil {
		cleanupParent.Close()
		return nil, err
	}
	if err = cleanupParent.Mkdir(name, 0700); err != nil {
		cleanupParent.Close()
		return nil, err
	}
	root, err := candidateDirectory(int(scratch.Fd()), name)
	if err != nil {
		cleanupParent.Close()
		// Ownership could not be established. Do not remove an unidentified root.
		return nil, errors.Join(ports.ErrCandidateCleanup, err)
	}
	var rootStat unix.Stat_t
	if unix.Fstat(int(root.Fd()), &rootStat) != nil {
		root.Close()
		cleanupParent.Close()
		return nil, errors.Join(ports.ErrCandidateCleanup, candidateDeny())
	}
	workspaceData, _ := json.Marshal(struct {
		Registry string
		Device   uint64
		Inode    uint64
	}{identity, uint64(realStat.Dev), realStat.Ino})
	w := &candidateWorkspace{root: root, cleanupParent: cleanupParent, name: name, rootStat: rootStat, policy: p, before: before, base: candidateStateIdentity(p, before), workspace: domain.CandidateDigest(workspaceData)}
	// Once returned, no source descriptor/path is retained by the copy writer.
	for _, target := range p.Targets() {
		file, ok := before[target]
		if !ok {
			continue
		}
		if err = w.put(ctx, file.Target, file.Content, file.Mode, false); err != nil {
			w.invalid = true
			return w, err
		}
	}
	return w, nil
}
func (w *candidateWorkspace) Inputs() []domain.CandidateFile {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []domain.CandidateFile{}
	for _, s := range w.policy.Targets() {
		if f, ok := w.before[s]; ok {
			out = append(out, f.Clone())
		}
	}
	return out
}
func (w *candidateWorkspace) put(ctx context.Context, target string, content []byte, mode uint32, replace bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, name, err := candidateParent(int(w.root.Fd()), target, true, false)
	if err != nil {
		return err
	}
	defer parent.Close()
	flags := unix.O_WRONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	if !replace {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, mode)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return candidateDeny()
	}
	if replace {
		old := w.before[target]
		if uint32(st.Mode&07777) != old.Mode {
			return candidateDeny()
		}
		current, err := candidateRead(ctx, int(w.root.Fd()), target, w.policy.Limits.MaxFileBytes, false)
		if err != nil || current.Identity != old.Identity {
			return candidateDeny()
		}
		if err = unix.Ftruncate(fd, 0); err != nil {
			return err
		}
	}
	if err = unix.Fchmod(fd, mode); err != nil {
		return err
	}
	if _, err = file.Write(content); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	return ctx.Err()
}
func (w *candidateWorkspace) Write(ctx context.Context, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.invalid || w.written {
		return candidateDeny()
	}
	proposal, err := domain.ValidateStructuredProposal(data, w.policy, w.before)
	if err != nil {
		w.invalid = true
		return err
	}
	physical, err := w.scan(ctx)
	if err != nil || candidateStateIdentity(w.policy, physical) != w.base {
		w.invalid = true
		return candidateDeny()
	}
	for i, e := range proposal.Edits {
		if w.beforeWrite != nil {
			if err = w.beforeWrite(i); err != nil {
				w.invalid = true
				return err
			}
		}
		content, _ := base64.StdEncoding.Strict().DecodeString(e.PostimageContent)
		mode := uint32(0644)
		if old, ok := w.before[e.Target]; ok {
			mode = old.Mode
		}
		if err = w.put(ctx, e.Target, content, mode, e.Operation == domain.WriteReplace); err != nil {
			w.invalid = true
			return err
		}
		physical, err := candidateRead(ctx, int(w.root.Fd()), e.Target, w.policy.Limits.MaxFileBytes, false)
		if err != nil || physical.Identity != e.PostimageIdentity || physical.Mode != mode {
			w.invalid = true
			return candidateDeny()
		}
	}
	w.written = true
	return nil
}
func (w *candidateWorkspace) scan(ctx context.Context) (map[string]domain.CandidateFile, error) {
	var rootStat unix.Stat_t
	if unix.Fstat(int(w.root.Fd()), &rootStat) != nil || rootStat.Mode&07777 != 0700 || rootStat.Dev != w.rootStat.Dev || rootStat.Ino != w.rootStat.Ino {
		return nil, candidateDeny()
	}
	targets := map[string]bool{}
	dirs := map[string]bool{}
	presentDirs := map[string]bool{}
	for _, s := range w.policy.Targets() {
		targets[s] = true
		parts := strings.Split(s, "/")
		for i := 1; i < len(parts); i++ {
			dirs[strings.Join(parts[:i], "/")] = true
		}
	}
	out := map[string]domain.CandidateFile{}
	var total int64
	var walk func(int, string) error
	walk = func(fd int, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := candidateDirectory(fd, ".")
		if err != nil {
			return err
		}
		defer dir.Close()
		entries, err := dir.ReadDir(len(targets) + len(dirs) + 1)
		if err != nil && err != io.EOF {
			return err
		}
		if len(entries) > len(targets)+len(dirs) {
			return candidateDeny()
		}
		for _, entry := range entries {
			target := prefix + entry.Name()
			var st unix.Stat_t
			if unix.Fstatat(fd, entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
				return candidateDeny()
			}
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				if !dirs[target] || st.Mode&07777 != 0700 {
					return candidateDeny()
				}
				presentDirs[target] = true
				child, err := candidateDirectory(fd, entry.Name())
				if err != nil {
					return err
				}
				err = walk(int(child.Fd()), target+"/")
				child.Close()
				if err != nil {
					return err
				}
			case unix.S_IFREG:
				if !targets[target] || w.policy.Excluded(target) {
					return candidateDeny()
				}
				f, err := candidateRead(ctx, int(w.root.Fd()), target, w.policy.Limits.MaxFileBytes, false)
				if err != nil {
					return err
				}
				total += int64(len(f.Content))
				if total > w.policy.Limits.MaxTotalBytes {
					return candidateDeny()
				}
				out[target] = f
			default:
				return candidateDeny()
			}
		}
		return nil
	}
	if err := walk(int(w.root.Fd()), ""); err != nil {
		return nil, err
	}
	for dir := range presentDirs {
		necessary := false
		for target := range out {
			if strings.HasPrefix(target, dir+"/") {
				necessary = true
				break
			}
		}
		if !necessary {
			return nil, candidateDeny()
		}
	}
	return out, nil
}
func (w *candidateWorkspace) Extract(ctx context.Context, c domain.CandidateContext) (domain.CandidateArtifact, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.invalid || !w.written || c.Validate() != nil {
		return domain.CandidateArtifact{}, candidateDeny()
	}
	after, err := w.scan(ctx)
	if err != nil {
		w.invalid = true
		return domain.CandidateArtifact{}, err
	}
	allowed := map[string]bool{}
	for _, s := range w.policy.WriteTargets {
		allowed[s] = true
	}
	manifest := domain.WriteManifest{SchemaVersion: 1}
	blobs := map[string][]byte{}
	for _, s := range w.policy.Targets() {
		old, existed := w.before[s]
		now, exists := after[s]
		if existed && (!exists || old.Mode != now.Mode) {
			w.invalid = true
			return domain.CandidateArtifact{}, candidateDeny()
		}
		if !existed && exists && now.Mode != 0644 {
			w.invalid = true
			return domain.CandidateArtifact{}, candidateDeny()
		}
		if !exists || existed && old.Identity == now.Identity {
			continue
		}
		if !allowed[s] {
			w.invalid = true
			return domain.CandidateArtifact{}, candidateDeny()
		}
		entry := domain.WriteManifestEntry{Target: s, Operation: domain.WriteCreate, FileType: "regular", Postimage: now.Identity, AfterMode: now.Mode}
		if existed {
			entry.Operation = domain.WriteReplace
			entry.Preimage = old.Identity
			entry.BeforeMode = old.Mode
		}
		manifest.Entries = append(manifest.Entries, entry)
		blobs[now.Identity] = now.Content
	}
	if len(manifest.Entries) == 0 || len(manifest.Entries) > w.policy.Limits.MaxOperations {
		return domain.CandidateArtifact{}, candidateDeny()
	}
	parameters := w.policy.Clone()
	sort.Strings(parameters.InputTargets)
	sort.Strings(parameters.WriteTargets)
	sort.Strings(parameters.ExcludedTargets)
	parametersData, _ := json.Marshal(parameters)
	candidate := domain.WriteCandidate{SchemaVersion: 1, ArtifactID: "pending", ProjectID: c.ProjectID, TaskID: c.TaskID, CorrelationID: c.CorrelationID, WorkspaceIdentity: w.workspace, BaseIdentity: w.base, ExpectedPostIdentity: candidateStateIdentity(w.policy, after), OperationParametersIdentity: domain.CandidateDigest(parametersData), Manifest: manifest}
	canonical, _ := json.Marshal(candidate)
	candidate.ArtifactID = domain.CandidateDigest(canonical)
	return domain.NewCandidateArtifact(candidate, blobs)
}
func (w *candidateWorkspace) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	info, err := w.cleanupParent.Lstat(w.name)
	if err != nil {
		return err
	}
	// os.FileInfo.Sys uses syscall.Stat_t; compare via the pinned child descriptor.
	child, err := w.cleanupParent.OpenFile(w.name, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	var actual unix.Stat_t
	err = unix.Fstat(int(child.Fd()), &actual)
	child.Close()
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || actual.Dev != w.rootStat.Dev || actual.Ino != w.rootStat.Ino {
		return candidateDeny()
	}
	if err = w.cleanupParent.RemoveAll(w.name); err != nil {
		return err
	}
	w.closed = true
	w.root.Close()
	return w.cleanupParent.Close()
}
