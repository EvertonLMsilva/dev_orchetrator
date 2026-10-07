//go:build linux

package infrastructure

import (
	"bytes"
	"context"
	"crypto/sha1"
	"dev-orchestrator/internal/domain"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const managedGitConfig = "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n"
const gitZeroOID = "0000000000000000000000000000000000000000"

type gitTreeFile struct {
	Mode string
	OID  string
}
type gitLimitedOutput struct {
	mu     sync.Mutex
	b      bytes.Buffer
	cancel context.CancelFunc
}

func (w *gitLimitedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.b.Len()+len(p) > 64<<20 {
		w.cancel()
		return 0, ErrManagedWriteBlocked
	}
	return w.b.Write(p)
}

// Private fixed plumbing only. No model-owned executable, argv, cwd, env,
// config or shell is accepted. Content is stdin or index-info, never code.
func managedGit(ctx context.Context, root, repo *os.File, index string, input []byte, actor, committer domain.GitActor, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	flags := []string{"--no-pager", "--literal-pathspecs", "--git-dir=/proc/self/fd/4", "--work-tree=/proc/self/fd/3", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.bare=true", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "commit.gpgSign=false", "-c", "core.logAllRefUpdates=false", "-c", "credential.helper=", "-c", "i18n.commitEncoding=UTF-8"}
	if args[0] == "init" {
		flags = append(flags[:3], flags[4:]...)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/git", append(flags, args...)...)
	cmd.ExtraFiles = []*os.File{root, repo}
	cmd.Dir = "/"
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "GIT_EDITOR=/bin/false", "GIT_CONFIG_SYSTEM=/dev/null"}
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE=/proc/self/fd/4/"+index)
	}
	if actor.Validate() {
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME="+actor.Name, "GIT_AUTHOR_EMAIL="+actor.Email, "GIT_AUTHOR_DATE=@"+strconv.FormatInt(actor.UnixSeconds, 10)+" +0000", "GIT_COMMITTER_NAME="+committer.Name, "GIT_COMMITTER_EMAIL="+committer.Email, "GIT_COMMITTER_DATE=@"+strconv.FormatInt(committer.UnixSeconds, 10)+" +0000")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.WaitDelay = time.Second
	out := &gitLimitedOutput{cancel: cancel}
	stderr := &gitLimitedOutput{cancel: cancel}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("trusted Git %s failed: %w", args[0], errors.Join(ErrManagedWriteBlocked, ctx.Err()))
	}
	return out.b.Bytes(), nil
}
func gitObjectID(kind string, data []byte) string {
	hash := sha1.New()
	fmt.Fprintf(hash, "%s %d\x00", kind, len(data))
	hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}
func gitCommitBytes(tree, parent, message string, a, c domain.GitActor) []byte {
	header := "tree " + tree + "\n"
	if parent != "" {
		header += "parent " + parent + "\n"
	}
	actor := func(v domain.GitActor) string { return fmt.Sprintf("%s <%s> %d +0000", v.Name, v.Email, v.UnixSeconds) }
	return []byte(header + "author " + actor(a) + "\ncommitter " + actor(c) + "\n\n" + message)
}
func gitExpectedTree(files map[string]gitTreeFile) (string, error) {
	paths := []domain.WriteManifestEntry{}
	for path := range files {
		paths = append(paths, domain.WriteManifestEntry{Target: path, Operation: domain.WriteCreate, FileType: "regular", Postimage: domain.CandidateDigest(nil), AfterMode: 0644})
	}
	if len(paths) > 0 {
		if _, err := (domain.WriteManifest{SchemaVersion: 1, Entries: paths}).Identity(); err != nil {
			return "", err
		}
	}
	type node struct {
		files map[string]gitTreeFile
		dirs  map[string]*node
	}
	root := &node{map[string]gitTreeFile{}, map[string]*node{}}
	for path, f := range files {
		if !domain.GitOID(f.OID) || (f.Mode != "100644" && f.Mode != "100755") {
			return "", domain.ErrWriteDenied
		}
		parts := strings.Split(path, "/")
		n := root
		for _, part := range parts[:len(parts)-1] {
			if _, ok := n.files[part]; ok {
				return "", domain.ErrWriteDenied
			}
			if n.dirs[part] == nil {
				n.dirs[part] = &node{map[string]gitTreeFile{}, map[string]*node{}}
			}
			n = n.dirs[part]
		}
		name := parts[len(parts)-1]
		if n.dirs[name] != nil {
			return "", domain.ErrWriteDenied
		}
		n.files[name] = f
	}
	var encode func(*node) (string, error)
	encode = func(n *node) (string, error) {
		entries := map[string]gitTreeFile{}
		keys := []string{}
		for name, f := range n.files {
			entries[name] = f
			keys = append(keys, name)
		}
		for name, dir := range n.dirs {
			id, err := encode(dir)
			if err != nil {
				return "", err
			}
			entries[name+"/"] = gitTreeFile{"40000", id}
			keys = append(keys, name+"/")
		}
		sort.Strings(keys)
		var b bytes.Buffer
		for _, key := range keys {
			f := entries[key]
			raw, _ := hex.DecodeString(f.OID)
			fmt.Fprintf(&b, "%s %s\x00", f.Mode, strings.TrimSuffix(key, "/"))
			b.Write(raw)
		}
		return gitObjectID("tree", b.Bytes()), nil
	}
	return encode(root)
}
func managedGitTree(ctx context.Context, root, repo *os.File, commit string) (map[string]gitTreeFile, error) {
	if !domain.GitOID(commit) {
		return nil, domain.ErrWriteDenied
	}
	commitBytes, err := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "cat-file", "commit", commit)
	if err != nil || gitObjectID("commit", commitBytes) != commit {
		return nil, ErrManagedWriteBlocked
	}
	header, _, ok := strings.Cut(string(commitBytes), "\n")
	if !ok || !strings.HasPrefix(header, "tree ") || !domain.GitOID(strings.TrimPrefix(header, "tree ")) {
		return nil, ErrManagedWriteBlocked
	}
	out, err := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	files := map[string]gitTreeFile{}
	entries := []domain.WriteManifestEntry{}
	for len(out) > 0 {
		end := bytes.IndexByte(out, 0)
		if end < 0 || len(files) >= 1024 {
			return nil, ErrManagedWriteBlocked
		}
		header, path, ok := strings.Cut(string(out[:end]), "\t")
		out = out[end+1:]
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || fields[1] != "blob" || !domain.GitOID(fields[2]) || (fields[0] != "100644" && fields[0] != "100755") {
			return nil, ErrManagedWriteBlocked
		}
		if _, ok := files[path]; ok {
			return nil, ErrManagedWriteBlocked
		}
		files[path] = gitTreeFile{fields[0], fields[2]}
		mode := uint32(0644)
		if fields[0] == "100755" {
			mode = 0755
		}
		entries = append(entries, domain.WriteManifestEntry{Target: path, Operation: domain.WriteCreate, FileType: "regular", Postimage: domain.CandidateDigest(nil), AfterMode: mode})
	}
	if len(entries) > 0 {
		if _, err := (domain.WriteManifest{SchemaVersion: 1, Entries: entries}).Identity(); err != nil {
			return nil, err
		}
	}
	expected, err := gitExpectedTree(files)
	if err != nil || expected != strings.TrimPrefix(header, "tree ") {
		return nil, ErrManagedWriteBlocked
	}
	var total int
	for _, f := range files {
		data, err := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "cat-file", "blob", f.OID)
		total += len(data)
		if err != nil || len(data) > 16<<20 || total > 64<<20 || gitObjectID("blob", data) != f.OID {
			return nil, ErrManagedWriteBlocked
		}
	}
	return files, nil
}
func managedGitRead(ctx context.Context, root *os.File, path string) ([]byte, error) {
	f, err := candidateRead(ctx, int(root.Fd()), path, 64<<20, false)
	if err != nil {
		return nil, err
	}
	return f.Content, nil
}

// No metadata symlink, hardlink, alternate storage, common dir, graft or
// replacement refs is accepted. Local config must be the provisioner's exact
// inert config. Unknown/malicious config is denied rather than interpreted.
func managedGitMetadata(repo *os.File, requireConfig bool) error {
	for _, path := range []string{"commondir", "objects/info/alternates", "objects/info/http-alternates", "info/grafts", "refs/replace", "index.lock"} {
		parent, name, parentErr := candidateParent(int(repo.Fd()), path, false, false)
		if errors.Is(parentErr, unix.ENOENT) {
			continue
		}
		if parentErr != nil {
			return ErrManagedWriteBlocked
		}
		var st unix.Stat_t
		err := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
		parent.Close()
		if !errors.Is(err, unix.ENOENT) {
			return ErrManagedWriteBlocked
		}
	}
	count := 0
	var walk func(int, int) error
	walk = func(fd, depth int) error {
		if depth > 16 {
			return ErrManagedWriteBlocked
		}
		d, err := candidateDirectory(fd, ".")
		if err != nil {
			return err
		}
		defer d.Close()
		entries, err := d.ReadDir(8193)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			count++
			if count > 8192 {
				return ErrManagedWriteBlocked
			}
			var st unix.Stat_t
			if unix.Fstatat(fd, entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Uid != uint32(os.Geteuid()) {
				return ErrManagedWriteBlocked
			}
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				child, err := candidateDirectory(fd, entry.Name())
				if err != nil {
					return err
				}
				err = walk(int(child.Fd()), depth+1)
				child.Close()
				if err != nil {
					return err
				}
			case unix.S_IFREG:
				if st.Nlink != 1 || st.Mode&0022 != 0 {
					return ErrManagedWriteBlocked
				}
			default:
				return ErrManagedWriteBlocked
			}
		}
		return nil
	}
	if err := walk(int(repo.Fd()), 0); err != nil {
		return err
	}
	if requireConfig {
		b, err := managedGitRead(context.Background(), repo, "config")
		if err != nil || string(b) != managedGitConfig {
			return ErrManagedWriteBlocked
		}
	}
	return nil
}
func (s *ManagedWorkspaceStore) openRepository(db *managedDatabase, w ManagedWorkspace, r domain.ManagedRepositoryIdentity) (*os.File, error) {
	registered, ok := db.Repositories[w.WorkspaceID]
	if !ok || registered != r || r.Validate() != nil || r.WorkspaceID != w.WorkspaceID || r.WorkspaceGeneration != w.Generation {
		return nil, domain.ErrWriteDenied
	}
	repo, err := candidateDirectory(int(s.control.Fd()), r.RepositoryID)
	if err != nil {
		return nil, ErrManagedWriteBlocked
	}
	var st unix.Stat_t
	if unix.Fstat(int(repo.Fd()), &st) != nil || managedRootIdentity(st) != r.GitDirIdentity || st.Mode&07777 != 0700 {
		repo.Close()
		return nil, ErrManagedWriteBlocked
	}
	if err = managedGitMetadata(repo, true); err != nil {
		repo.Close()
		return nil, err
	}
	return repo, nil
}
func gitReadOID(ctx context.Context, root, repo *os.File, ref string) (string, error) {
	out, err := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "rev-parse", "--verify", ref+"^{commit}")
	id := strings.TrimSpace(string(out))
	if err != nil || !domain.GitOID(id) {
		return "", ErrManagedWriteBlocked
	}
	return id, nil
}
func gitRef(ctx context.Context, root, repo *os.File, branch string) (string, error) {
	if !domain.GitBranchName(branch) {
		return "", domain.ErrWriteDenied
	}
	// Distinguish absence from errors by inspecting the fixed local ref path.
	parent, name, err := candidateParent(int(repo.Fd()), "refs/heads/"+branch, false, false)
	if errors.Is(err, unix.ENOENT) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer parent.Close()
	var st unix.Stat_t
	err = unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	raw, err := managedGitRead(ctx, repo, "refs/heads/"+branch)
	if err != nil || !domain.GitOID(strings.TrimSpace(string(raw))) {
		return "", ErrManagedWriteBlocked
	}
	return gitReadOID(ctx, root, repo, "refs/heads/"+branch)
}
func gitMode(mode uint32) string {
	if mode == 0755 {
		return "100755"
	}
	return "100644"
}
func gitBuildTree(ctx context.Context, root, repo *os.File, index, parent string, files map[string]domain.CandidateFile) (string, error) {
	args := []string{"read-tree", "--empty"}
	if parent != "" {
		args = []string{"read-tree", parent}
	}
	if _, err := managedGit(ctx, root, repo, index, nil, domain.GitActor{}, domain.GitActor{}, args...); err != nil {
		return "", err
	}
	keys := []string{}
	for path := range files {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	var entries bytes.Buffer
	for _, path := range keys {
		file := files[path]
		out, err := managedGit(ctx, root, repo, index, file.Content, domain.GitActor{}, domain.GitActor{}, "hash-object", "-w", "--stdin", "--no-filters")
		id := strings.TrimSpace(string(out))
		if err != nil || id != gitObjectID("blob", file.Content) {
			return "", ErrManagedWriteBlocked
		}
		stored, err := managedGit(ctx, root, repo, index, nil, domain.GitActor{}, domain.GitActor{}, "cat-file", "blob", id)
		if err != nil || !bytes.Equal(stored, file.Content) {
			return "", ErrManagedWriteBlocked
		}
		fmt.Fprintf(&entries, "%s %s\t%s\x00", gitMode(file.Mode), id, path)
	}
	if entries.Len() > 0 {
		if _, err := managedGit(ctx, root, repo, index, entries.Bytes(), domain.GitActor{}, domain.GitActor{}, "update-index", "-z", "--index-info"); err != nil {
			return "", err
		}
	}
	return gitReadTree(ctx, root, repo, index)
}
func gitReadTree(ctx context.Context, root, repo *os.File, index string) (string, error) {
	out, err := managedGit(ctx, root, repo, index, nil, domain.GitActor{}, domain.GitActor{}, "write-tree")
	id := strings.TrimSpace(string(out))
	if err != nil || !domain.GitOID(id) {
		return "", ErrManagedWriteBlocked
	}
	return id, nil
}
