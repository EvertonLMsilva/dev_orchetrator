package file

import (
	"bytes"
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
)

var ErrStorage = errors.New("invalid or unavailable task storage")

const maxStateBytes = 1024 * 1024

type TaskRepository struct {
	mu          sync.RWMutex
	dir         string
	tasks       map[domain.TaskID]domain.Task
	failed      bool
	development bool
}

var _ ports.TaskRepository = (*TaskRepository)(nil)

// The composition owns the directory and exclusive process lease. Files are
// opened through Root; snapshots are bounded and replaced atomically.
func NewTaskRepository(dir string) (*TaskRepository, error) {
	return newTaskRepository(dir, false)
}

// Explicit trusted composition opt-in; the P6 constructor stays READ-only.
func NewDevelopmentTaskRepository(dir string) (*TaskRepository, error) {
	return newTaskRepository(dir, true)
}
func newTaskRepository(dir string, development bool) (*TaskRepository, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrStorage
	}
	defer root.Close()
	r := &TaskRepository{dir: dir, tasks: map[domain.TaskID]domain.Task{}, development: development}
	if _, e := root.Lstat("tasks.pending"); !errors.Is(e, os.ErrNotExist) {
		return nil, ErrStorage
	}
	info, err := root.Lstat("tasks.json")
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxStateBytes {
		return nil, ErrStorage
	}
	f, err := root.Open("tasks.json")
	if err != nil {
		return nil, ErrStorage
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil || len(data) > maxStateBytes {
		return nil, ErrStorage
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if !uniqueSnapshot(data) {
		return nil, ErrStorage
	}
	dec.DisallowUnknownFields()
	var tasks []domain.Task
	if dec.Decode(&tasks) != nil || tasks == nil {
		return nil, ErrStorage
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, ErrStorage
	}
	for _, t := range tasks {
		if !r.validTask(t) {
			return nil, ErrStorage
		}
		if _, ok := r.tasks[t.ID]; ok {
			return nil, ErrStorage
		}
		r.tasks[t.ID] = t
	}
	return r, nil
}

// ValidateProjects rejects orphaned state and competing active tasks on startup.
func (r *TaskRepository) ValidateProjects(projects map[domain.ProjectID]bool) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	active := map[domain.ProjectID]bool{}
	for _, t := range r.tasks {
		if !projects[t.ProjectID] {
			return ErrStorage
		}
		if t.Status == domain.TaskStatusCancelled {
			continue
		}
		if active[t.ProjectID] {
			return ErrStorage
		}
		active[t.ProjectID] = true
	}
	return nil
}
func uniqueSnapshot(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	token, e := d.Token()
	if e != nil || token != json.Delim('[') {
		return false
	}
	for d.More() {
		token, e = d.Token()
		if e != nil || token != json.Delim('{') {
			return false
		}
		keys := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			s, ok := key.(string)
			if e != nil || !ok || keys[s] {
				return false
			}
			keys[s] = true
			v, e := d.Token()
			if _, ok := v.(string); e != nil || !ok {
				return false
			}
		}
		if token, e = d.Token(); e != nil || token != json.Delim('}') || len(keys) != 4 {
			return false
		}
	}
	if token, e = d.Token(); e != nil || token != json.Delim(']') {
		return false
	}
	_, e = d.Token()
	return e == io.EOF
}
func validTask(t domain.Task) bool {
	if strings.TrimSpace(string(t.ID)) == "" || strings.TrimSpace(string(t.ProjectID)) == "" || t.Title != "Conversation request" {
		return false
	}
	switch t.Status {
	case domain.TaskStatusPlanned, domain.TaskStatusReadyForAnalysis, domain.TaskStatusAnalyzing, domain.TaskStatusBlocked, domain.TaskStatusCancelled:
		return true
	}
	return false
}
func (r *TaskRepository) validTask(t domain.Task) bool {
	if validTask(t) {
		return true
	}
	if !r.development || strings.TrimSpace(string(t.ID)) == "" || strings.TrimSpace(string(t.ProjectID)) == "" || t.Title != "Conversation request" {
		return false
	}
	switch t.Status {
	case domain.TaskStatusReadyForCodex, domain.TaskStatusInProgress, domain.TaskStatusDone, domain.TaskStatusFailed:
		return true
	}
	return false
}
func (r *TaskRepository) Save(ctx context.Context, t domain.Task) (result error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !r.validTask(t) {
		return ErrStorage
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed {
		return ErrStorage
	}
	defer func() {
		if result != nil {
			r.failed = true
		}
	}()
	tasks := make([]domain.Task, 0, len(r.tasks)+1)
	for id, v := range r.tasks {
		if id != t.ID {
			tasks = append(tasks, v)
		}
	}
	tasks = append(tasks, t)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	data, err := json.Marshal(tasks)
	if err != nil || len(data) > maxStateBytes {
		return ErrStorage
	}
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return ErrStorage
	}
	defer root.Close()
	f, err := root.OpenFile("tasks.pending", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrStorage
	}
	defer root.Remove("tasks.pending")
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrStorage
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if root.Rename("tasks.pending", "tasks.json") != nil {
		return ErrStorage
	}
	directory, err := root.Open(".")
	if err != nil {
		return ErrStorage
	}
	err = directory.Sync()
	directory.Close()
	if err != nil {
		return ErrStorage
	}
	r.tasks[t.ID] = t
	return nil
}
func (r *TaskRepository) FindByID(ctx context.Context, id domain.TaskID) (domain.Task, bool, error) {
	if ctx.Err() != nil {
		return domain.Task{}, false, ctx.Err()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.failed {
		return domain.Task{}, false, ErrStorage
	}
	t, ok := r.tasks[id]
	return t, ok, nil
}
func (r *TaskRepository) FindByProject(ctx context.Context, id domain.ProjectID) ([]domain.Task, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.failed {
		return nil, ErrStorage
	}
	out := []domain.Task{}
	for _, t := range r.tasks {
		if t.ProjectID == id {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *TaskRepository) Health() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.failed {
		return ErrStorage
	}
	return nil
}
