package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func TestTaskRepository(t *testing.T) {
	var r ports.TaskRepository = NewTaskRepository()
	ctx := context.Background()
	task := domain.Task{ID: "t1", ProjectID: "p1", Title: "first", Status: domain.TaskStatusPlanned}
	if got, found, err := r.FindByID(ctx, task.ID); got != (domain.Task{}) || found || err != nil {
		t.Fatalf("missing: %v %v %v", got, found, err)
	}
	for _, title := range []string{"first", "updated"} {
		task.Title = title
		if err := r.Save(ctx, task); err != nil {
			t.Fatal(err)
		}
		if got, found, err := r.FindByID(ctx, task.ID); got != task || !found || err != nil {
			t.Fatalf("round trip: %v %v %v", got, found, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	changed := task
	changed.Title = "cancelled"
	if err := r.Save(cancelled, changed); !errors.Is(err, context.Canceled) {
		t.Fatalf("save cancellation: %v", err)
	}
	if got, found, err := r.FindByID(cancelled, task.ID); got != (domain.Task{}) || found || !errors.Is(err, context.Canceled) {
		t.Fatalf("find cancellation: %v %v %v", got, found, err)
	}
	if got, err := r.FindByProject(cancelled, task.ProjectID); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("project cancellation: %v %v", got, err)
	}
	if got, _, _ := r.FindByID(ctx, task.ID); got != task {
		t.Fatal("cancelled save mutated storage")
	}
}

func TestTaskRepositoryFindByProject(t *testing.T) {
	r := NewTaskRepository()
	ctx := context.Background()
	tasks := []domain.Task{
		{ID: "a", ProjectID: "p1", Title: "a"},
		{ID: "b", ProjectID: "p1", Title: "b"},
		{ID: "c", ProjectID: "p10", Title: "c"},
	}
	for _, task := range tasks {
		if err := r.Save(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.FindByProject(ctx, "p1")
	if err != nil || len(got) != 2 {
		t.Fatalf("matches: %v %v", got, err)
	}
	seen := map[domain.TaskID]bool{}
	for _, task := range got {
		if task.ProjectID != "p1" || seen[task.ID] {
			t.Fatalf("unexpected task: %v", task)
		}
		seen[task.ID] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Fatal("missing matching task")
	}
	got[0].Title = "mutated result"
	if stored, _, _ := r.FindByID(ctx, got[0].ID); stored.Title == got[0].Title {
		t.Fatal("result aliases storage")
	}
	moved := tasks[0]
	moved.ProjectID = "p2"
	if err := r.Save(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if got, err := r.FindByProject(ctx, "p1"); err != nil || len(got) != 1 || got[0] != tasks[1] {
		t.Fatalf("overwrite project: %v %v", got, err)
	}
	if got, err := r.FindByProject(ctx, "absent"); err != nil || len(got) != 0 {
		t.Fatalf("empty project: %v %v", got, err)
	}
}

func TestTaskRepositoryConcurrent(t *testing.T) {
	r := NewTaskRepository()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			task := domain.Task{ID: domain.TaskID(fmt.Sprint(i)), ProjectID: "p1", Title: "task", Status: domain.TaskStatusPlanned}
			for j := 0; j < 12; j++ {
				if err := r.Save(ctx, task); err != nil {
					t.Error(err)
					return
				}
				if got, found, err := r.FindByID(ctx, task.ID); got != task || !found || err != nil {
					t.Errorf("concurrent round trip: %v %v %v", got, found, err)
				}
				got, err := r.FindByProject(ctx, "p1")
				if err != nil {
					t.Error(err)
					return
				}
				seen := map[domain.TaskID]bool{}
				for _, item := range got {
					if item.ProjectID != "p1" || seen[item.ID] {
						t.Errorf("inconsistent project result: %v", item)
					}
					seen[item.ID] = true
				}
			}
		}(i)
	}
	wg.Wait()
	if got, err := r.FindByProject(ctx, "p1"); err != nil || len(got) != 12 {
		t.Fatalf("lost tasks: %v %v", got, err)
	}
}
