package memory

import (
	"context"
	"dev-orchestrator/internal/domain"
	"fmt"
	"sync"
	"testing"
)

func TestApprovalRepository(t *testing.T) {
	r := NewApprovalRepository()
	ctx := context.Background()
	if got, found, err := r.FindByID(ctx, "missing"); got != (domain.Approval{}) || found || err != nil {
		t.Fatal(got, found, err)
	}
	task := domain.TaskID("t")
	a := domain.Approval{ID: "a", ProjectID: "p", TaskID: &task, Action: "opaque", Status: domain.ApprovalPending}
	for _, status := range []domain.ApprovalStatus{domain.ApprovalPending, domain.ApprovalApproved} {
		a.Status = status
		if err := r.Save(ctx, a); err != nil {
			t.Fatal(err)
		}
		got, found, err := r.FindByID(ctx, a.ID)
		if err != nil || !found || got.ID != a.ID || got.ProjectID != a.ProjectID || got.Action != a.Action || got.Status != a.Status || got.TaskID == nil || *got.TaskID != task {
			t.Fatal(got, found, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := r.Save(canceled, domain.Approval{ID: "c"}); err != context.Canceled {
		t.Fatal(err)
	}
	if got, found, err := r.FindByID(canceled, "a"); got != (domain.Approval{}) || found || err != context.Canceled {
		t.Fatal(got, found, err)
	}
	if _, found, _ := r.FindByID(ctx, "c"); found {
		t.Fatal("canceled save persisted")
	}
}

func TestApprovalRepositoryConcurrent(t *testing.T) {
	r := NewApprovalRepository()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := domain.Approval{ID: domain.ApprovalID(fmt.Sprint(i)), Status: domain.ApprovalPending}
			if err := r.Save(context.Background(), a); err != nil {
				t.Error(err)
			}
			if got, found, err := r.FindByID(context.Background(), a.ID); got != a || !found || err != nil {
				t.Error(got, found, err)
			}
		}(i)
	}
	wg.Wait()
}
