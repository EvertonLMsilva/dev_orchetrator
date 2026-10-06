package file

import (
	"context"
	"dev-orchestrator/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskRepositoryRestartAndInvalidState(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	r, err := NewTaskRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := domain.NewTask("task", "project", "Conversation request")
	if err = r.Save(ctx, task); err != nil {
		t.Fatal(err)
	}
	r2, err := NewTaskRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := r2.FindByID(ctx, task.ID)
	if err != nil || !found || got != task {
		t.Fatalf("restart: %#v %v", got, err)
	}
	task.Status = "INVALID"
	if r.Save(ctx, task) == nil {
		t.Fatal("invalid state accepted")
	}
	if err = os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(`[{"ID":"task","ProjectID":"project","Title":"request","Status":"INVALID"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewTaskRepository(dir); err == nil {
		t.Fatal("corrupt restart accepted")
	}
}
func TestTaskRepositoryFailureDoesNotCommit(t *testing.T) {
	dir := t.TempDir()
	r, err := NewTaskRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(dir, "tasks.json"), 0700); err != nil {
		t.Fatal(err)
	}
	task, _ := domain.NewTask("task", "project", "Conversation request")
	if r.Save(context.Background(), task) == nil {
		t.Fatal("write failure accepted")
	}
	if _, found, _ := r.FindByID(context.Background(), task.ID); found {
		t.Fatal("failed write published")
	}
}
