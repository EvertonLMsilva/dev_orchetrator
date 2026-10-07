package application

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"dev-orchestrator/internal/application/readcontracts"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var errReadQuery = errors.New("READ query unavailable or invalid")

// readBoundary is internal application composition only. It grants no access
// authority and must not be connected to external callers before the ADR 0002
// identity, authentication, authorization and audit gates are implemented.
// Private symbols intentionally prevent construction by an external adapter.
type readBoundary struct {
	projects ports.ProjectRepository
	tasks    ports.TaskRepository
	agent    *LocalAgentDispatcher
}

func newReadBoundary(p ports.ProjectRepository, t ports.TaskRepository, a *LocalAgentDispatcher) *readBoundary {
	return &readBoundary{projects: p, tasks: t, agent: a}
}

// dispatch accepts only the distinct approved request value types. Validation
// is repeated defensively; unsupported operations never reach dependencies.
func (b *readBoundary) dispatch(ctx context.Context, operation string, request any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var projectID, correlationID string
	switch operation {
	case "project.status":
		r, ok := request.(readcontracts.ProjectStatusRequest)
		if !ok || r.Validate() != nil {
			return nil, errReadQuery
		}
		projectID, correlationID = r.ProjectID, r.CorrelationID
	case "project.tasks":
		r, ok := request.(readcontracts.ProjectTasksRequest)
		if !ok || r.Validate() != nil {
			return nil, errReadQuery
		}
		projectID, correlationID = r.ProjectID, r.CorrelationID
	case "git.status":
		r, ok := request.(readcontracts.GitStatusRequest)
		if !ok || r.Validate() != nil {
			return nil, errReadQuery
		}
		projectID, correlationID = r.ProjectID, r.CorrelationID
	case "execution.status":
		r, ok := request.(readcontracts.ExecutionStatusRequest)
		if !ok || r.Validate() != nil {
			return nil, errReadQuery
		}
		projectID, correlationID = r.ProjectID, r.CorrelationID
	default:
		return nil, errReadQuery
	}
	if b == nil || b.projects == nil {
		return nil, errReadQuery
	}
	project, found, err := b.projects.FindByID(ctx, domain.ProjectID(projectID))
	if err != nil || !found || string(project.ID) != projectID {
		return nil, errReadQuery
	}
	meta := readcontracts.Metadata{SchemaVersion: readcontracts.SchemaVersion, ProjectID: projectID, CorrelationID: correlationID, ObservedAt: time.Now().UTC()}
	var result interface{ Validate() error }
	switch operation {
	case "project.status":
		tasks, git := readcontracts.Unavailable, readcontracts.Unavailable
		if b.tasks != nil {
			tasks = readcontracts.Available
		}
		if b.agent != nil && b.agent.projects != nil && b.agent.allowlist.Allows(domain.ActionTypeGitStatus) {
			git = readcontracts.Available
		}
		result = readcontracts.ProjectStatusResponse{Metadata: meta, Name: project.Name, Queries: readcontracts.QueryAvailability{ProjectStatus: readcontracts.Available, ProjectTasks: tasks, GitStatus: git, ExecutionStatus: readcontracts.Unavailable}}
	case "project.tasks":
		if b.tasks == nil {
			return nil, errReadQuery
		}
		all, e := b.tasks.FindByProject(ctx, project.ID)
		if e != nil {
			return nil, errReadQuery
		}
		summaries := make([]readcontracts.TaskSummary, 0, len(all))
		seen := map[string]bool{}
		for _, task := range all {
			summary := readcontracts.TaskSummary{TaskID: string(task.ID), Title: task.Title, TaskStatus: string(task.Status)}
			if task.ProjectID != project.ID || summary.Validate() != nil || seen[summary.TaskID] {
				return nil, errReadQuery
			}
			seen[summary.TaskID] = true
			summaries = append(summaries, summary)
		}
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].TaskID < summaries[j].TaskID })
		r := request.(readcontracts.ProjectTasksRequest)
		start := min(r.Offset, len(summaries))
		end := min(start+r.Limit, len(summaries))
		page := make([]readcontracts.TaskSummary, end-start)
		copy(page, summaries[start:end])
		result = readcontracts.ProjectTasksResponse{Metadata: meta, Tasks: page, HasMore: end < len(summaries)}
	case "git.status":
		if b.agent == nil {
			return nil, errReadQuery
		}
		value, e := b.agent.Execute(ctx, domain.Action{Type: domain.ActionTypeGitStatus, ProjectID: project.ID})
		if e != nil || value.Type != domain.ActionTypeGitStatus || value.Validate() != nil {
			return nil, errReadQuery
		}
		result = readcontracts.GitStatusResponse{Metadata: meta, ChangedEntries: len(value.GitStatusResult.Entries)}
	case "execution.status":
		// No reliable consultable execution source exists; never infer from tasks.
		result = readcontracts.ExecutionStatusResponse{Metadata: meta, ExecutionObservation: readcontracts.Unavailable}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if result.Validate() != nil {
		return nil, errReadQuery
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > readcontracts.MaxJSONBytes {
		return nil, errReadQuery
	}
	return result, nil
}
