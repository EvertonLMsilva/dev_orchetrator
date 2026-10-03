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

func TestProjectRepository(t *testing.T) {
	var r ports.ProjectRepository = NewProjectRepository()
	ctx := context.Background()
	p := domain.Project{ID: "p1", Name: "first", Workspace: "workspace"}
	if got, found, err := r.FindByID(ctx, p.ID); got != (domain.Project{}) || found || err != nil {
		t.Fatalf("missing: %v %v %v", got, found, err)
	}
	for _, name := range []string{"first", "updated"} {
		p.Name = name
		if err := r.Save(ctx, p); err != nil {
			t.Fatal(err)
		}
		if got, found, err := r.FindByID(ctx, p.ID); got != p || !found || err != nil {
			t.Fatalf("round trip: %v %v %v", got, found, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	changed := p
	changed.Name = "cancelled"
	if err := r.Save(cancelled, changed); !errors.Is(err, context.Canceled) {
		t.Fatalf("save cancellation: %v", err)
	}
	if got, found, err := r.FindByID(cancelled, p.ID); got != (domain.Project{}) || found || !errors.Is(err, context.Canceled) {
		t.Fatalf("find cancellation: %v %v %v", got, found, err)
	}
	if got, _, _ := r.FindByID(ctx, p.ID); got != p {
		t.Fatal("cancelled save mutated storage")
	}
}

func TestProjectRepositoryConcurrent(t *testing.T) {
	r := NewProjectRepository()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := domain.Project{ID: domain.ProjectID(fmt.Sprint(i)), Name: "project", Workspace: "workspace"}
			for j := 0; j < 12; j++ {
				if err := r.Save(ctx, p); err != nil {
					t.Error(err)
					return
				}
				if got, found, err := r.FindByID(ctx, p.ID); got != p || !found || err != nil {
					t.Errorf("concurrent round trip: %v %v %v", got, found, err)
				}
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < 12; i++ {
		if _, found, err := r.FindByID(ctx, domain.ProjectID(fmt.Sprint(i))); !found || err != nil {
			t.Fatalf("lost project %d: %v", i, err)
		}
	}
}
