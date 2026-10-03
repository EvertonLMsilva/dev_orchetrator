package domain

import "testing"

func TestNewApproval(t *testing.T) {
	task := TaskID(" task ")
	want := Approval{ID: " id ", ProjectID: " project ", TaskID: &task, Action: " action ", Status: ApprovalPending}
	got, err := NewApproval(want.ID, want.ProjectID, want.TaskID, want.Action)
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, err := NewApproval("a", "p", nil, "opaque"); err != nil || got.TaskID != nil || got.Status != ApprovalPending {
		t.Fatal(got, err)
	}
	for _, blank := range []string{"", " \t\n"} {
		invalidTask := TaskID(blank)
		for _, tc := range []struct {
			id      ApprovalID
			project ProjectID
			task    *TaskID
			action  string
			want    error
		}{
			{ApprovalID(blank), "p", nil, "a", ErrApprovalIDRequired},
			{"a", ProjectID(blank), nil, "a", ErrApprovalProjectIDRequired},
			{"a", "p", &invalidTask, "a", ErrApprovalTaskIDInvalid},
			{"a", "p", nil, blank, ErrApprovalActionRequired},
		} {
			if got, err := NewApproval(tc.id, tc.project, tc.task, tc.action); err != tc.want || got != (Approval{}) {
				t.Fatalf("got %+v, %v; want %v", got, err, tc.want)
			}
		}
	}
}

func TestApprovalTransitions(t *testing.T) {
	task := TaskID("t")
	for _, status := range []ApprovalStatus{ApprovalPending, ApprovalApproved, ApprovalRejected, "unknown", ""} {
		for _, approve := range []bool{true, false} {
			original := Approval{ID: "a", ProjectID: "p", TaskID: &task, Action: "opaque", Status: status}
			before := original
			var got Approval
			var err error
			if approve {
				got, err = original.Approve()
			} else {
				got, err = original.Reject()
			}
			if original != before {
				t.Fatal("receiver changed")
			}
			if status != ApprovalPending {
				if err != ErrApprovalNotPending || got != (Approval{}) {
					t.Fatal(got, err)
				}
				continue
			}
			want := before
			want.Status = ApprovalRejected
			if approve {
				want.Status = ApprovalApproved
			}
			if err != nil || got != want {
				t.Fatal(got, err)
			}
		}
	}
}
