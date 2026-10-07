package file

import (
	"context"
	"dev-orchestrator/internal/domain"
	"testing"
)

func TestDevelopmentPersistenceDoesNotWeakenReadOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r, err := NewDevelopmentTaskRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", ProjectID: "pilot", Title: "Conversation request", Status: domain.TaskStatusDone}
	if err := r.Save(ctx, task); err != nil {
		t.Fatal(err)
	}
	restored, err := NewDevelopmentTaskRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := restored.FindByID(ctx, task.ID)
	if err != nil || !found || got != task {
		t.Fatal("persistent state", err)
	}
	if _, err := NewTaskRepository(dir); err == nil {
		t.Fatal("READ constructor accepted development state")
	}
	readonly, err := NewTaskRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := readonly.Save(ctx, task); err == nil {
		t.Fatal("READ store accepted DONE")
	}
}
