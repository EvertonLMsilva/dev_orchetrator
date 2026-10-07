// Package readcontracts defines safe READ projections, independent of transport,
// domain entities, authorization and query execution. Validate grants no authority.
// JSON input must use Decode; direct json.Unmarshal is not a supported contract
// validation path, because it loses field presence and permits unknown fields.
package readcontracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
)

const (
	SchemaVersion     = "1"
	MaxIDBytes        = 128
	MaxTextBytes      = 256
	MaxTasks          = 100
	MaxOffset         = 10000
	MaxJSONBytes      = 65536
	MaxChangedEntries = 100000
)

var ErrInvalid = errors.New("invalid READ contract")

type Availability string

const (
	Available   Availability = "AVAILABLE"
	Unavailable Availability = "UNAVAILABLE"
)

func (a Availability) valid() bool { return a == Available || a == Unavailable }

type ProjectStatusRequest struct {
	ProjectID     string `json:"projectId"`
	CorrelationID string `json:"correlationId"`
}

// Each operation has a distinct contract even when its current fields match.
type GitStatusRequest ProjectStatusRequest
type ExecutionStatusRequest ProjectStatusRequest
type ProjectTasksRequest struct {
	ProjectID     string `json:"projectId"`
	CorrelationID string `json:"correlationId"`
	Limit         int    `json:"limit"`
	Offset        int    `json:"offset"`
}

func bounded(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && !strings.ContainsAny(s, "\x00\r\n")
}
func identity(p, c string) error {
	if !bounded(p, MaxIDBytes) || !bounded(c, MaxIDBytes) {
		return ErrInvalid
	}
	return nil
}
func (r ProjectStatusRequest) Validate() error   { return identity(r.ProjectID, r.CorrelationID) }
func (r GitStatusRequest) Validate() error       { return identity(r.ProjectID, r.CorrelationID) }
func (r ExecutionStatusRequest) Validate() error { return identity(r.ProjectID, r.CorrelationID) }
func (r ProjectTasksRequest) Validate() error {
	if identity(r.ProjectID, r.CorrelationID) != nil || r.Limit < 1 || r.Limit > MaxTasks || r.Offset < 0 || r.Offset > MaxOffset {
		return ErrInvalid
	}
	return nil
}

type Metadata struct {
	SchemaVersion string `json:"schemaVersion"`
	ProjectID     string `json:"projectId"`
	CorrelationID string `json:"correlationId"`
	// ObservedAt timestamps the query observation, never an inferred execution event.
	ObservedAt time.Time `json:"observedAt"`
}

func (m Metadata) Validate() error {
	if m.SchemaVersion != SchemaVersion || identity(m.ProjectID, m.CorrelationID) != nil || m.ObservedAt.IsZero() || m.ObservedAt.Year() < 1 || m.ObservedAt.Year() > 9999 {
		return ErrInvalid
	}
	return nil
}

type QueryAvailability struct {
	ProjectStatus   Availability `json:"projectStatus"`
	ProjectTasks    Availability `json:"projectTasks"`
	GitStatus       Availability `json:"gitStatus"`
	ExecutionStatus Availability `json:"executionStatus"`
}
type ProjectStatusResponse struct {
	Metadata
	Name    string            `json:"name"`
	Queries QueryAvailability `json:"queries"`
}

func (r ProjectStatusResponse) Validate() error {
	if r.Metadata.Validate() != nil || !bounded(r.Name, MaxTextBytes) || !r.Queries.ProjectStatus.valid() || !r.Queries.ProjectTasks.valid() || !r.Queries.GitStatus.valid() || r.Queries.ExecutionStatus != Unavailable {
		return ErrInvalid
	}
	return nil
}

type TaskSummary struct {
	TaskID     string `json:"taskId"`
	Title      string `json:"title"`
	TaskStatus string `json:"taskStatus"`
}

func taskStatus(s string) bool {
	switch s {
	case "PLANNED", "READY_FOR_ANALYSIS", "ANALYZING", "READY_FOR_CODEX", "IN_PROGRESS", "DONE", "BLOCKED", "FAILED", "CANCELLED":
		return true
	}
	return false
}
func (t TaskSummary) Validate() error {
	if !bounded(t.TaskID, MaxIDBytes) || !bounded(t.Title, MaxTextBytes) || !taskStatus(t.TaskStatus) {
		return ErrInvalid
	}
	return nil
}

type ProjectTasksResponse struct {
	Metadata
	Tasks   []TaskSummary `json:"tasks"`
	HasMore bool          `json:"hasMore"`
}

func (r ProjectTasksResponse) Validate() error {
	if r.Metadata.Validate() != nil || r.Tasks == nil || len(r.Tasks) > MaxTasks {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, t := range r.Tasks {
		if t.Validate() != nil || seen[t.TaskID] {
			return ErrInvalid
		}
		seen[t.TaskID] = true
	}
	return nil
}

// GitStatusResponse exposes only a bounded summary, never repository paths or raw output.
type GitStatusResponse struct {
	Metadata
	ChangedEntries int `json:"changedEntries"`
}

func (r GitStatusResponse) Validate() error {
	if r.Metadata.Validate() != nil || r.ChangedEntries < 0 || r.ChangedEntries > MaxChangedEntries {
		return ErrInvalid
	}
	return nil
}

// No consultable execution source is defined in MCP-1. TaskStatus is separate
// workflow information and must never imply a running or completed session.
type ExecutionStatusResponse struct {
	Metadata
	ExecutionObservation Availability `json:"executionObservation"`
	TaskID               string       `json:"taskId,omitempty"`
	TaskStatus           string       `json:"taskStatus,omitempty"`
}

func (r ExecutionStatusResponse) Validate() error {
	if r.Metadata.Validate() != nil || r.ExecutionObservation != Unavailable {
		return ErrInvalid
	}
	if r.TaskID != "" || r.TaskStatus != "" {
		if !bounded(r.TaskID, MaxIDBytes) || !taskStatus(r.TaskStatus) {
			return ErrInvalid
		}
	}
	return nil
}

// Decode is the strict JSON boundary for these DTOs. Standard json.Unmarshal
// alone does not enforce the field allowlist or semantic validation.
// Failed decoding leaves dst unchanged.
func Decode(data []byte, dst any) error {
	if len(data) > MaxJSONBytes {
		return ErrInvalid
	}
	typ := reflect.TypeOf(dst)
	if typ == nil || typ.Kind() != reflect.Pointer || reflect.ValueOf(dst).IsNil() {
		return ErrInvalid
	}
	switch dst.(type) {
	case *ProjectStatusRequest, *ProjectTasksRequest, *GitStatusRequest, *ExecutionStatusRequest, *ProjectStatusResponse, *ProjectTasksResponse, *GitStatusResponse, *ExecutionStatusResponse:
	default:
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := checkObject(d, typ.Elem()); err != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	candidate := reflect.New(typ.Elem())
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(candidate.Interface()) != nil {
		return ErrInvalid
	}
	if candidate.Interface().(interface{ Validate() error }).Validate() != nil {
		return ErrInvalid
	}
	reflect.ValueOf(dst).Elem().Set(candidate.Elem())
	return nil
}
func fields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			for k, v := range fields(f.Type) {
				out[k] = v
			}
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			out[name] = f.Type
		}
	}
	return out
}
func checkObject(d *json.Decoder, t reflect.Type) error {
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return ErrInvalid
	}
	allowed := fields(t)
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return ErrInvalid
		}
		k, ok := key.(string)
		ft, exists := allowed[k]
		if !ok || !exists || seen[k] {
			return ErrInvalid
		}
		seen[k] = true
		if ft.Kind() == reflect.Struct && ft != reflect.TypeOf(time.Time{}) {
			if checkObject(d, ft) != nil {
				return ErrInvalid
			}
		} else if ft.Kind() == reflect.Slice {
			tok, err := d.Token()
			if err != nil || tok != json.Delim('[') {
				return ErrInvalid
			}
			count := 0
			for d.More() {
				count++
				if count > MaxTasks || checkObject(d, ft.Elem()) != nil {
					return ErrInvalid
				}
			}
			if tok, err = d.Token(); err != nil || tok != json.Delim(']') {
				return ErrInvalid
			}
		} else {
			var raw json.RawMessage
			if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return ErrInvalid
			}
			// Validate explicitly supplied IDs before decoding erases the
			// distinction between an absent optional ID and an empty string.
			if k == "projectId" || k == "correlationId" || k == "taskId" {
				var id string
				if json.Unmarshal(raw, &id) != nil || !bounded(id, MaxIDBytes) {
					return ErrInvalid
				}
			}
		}
	}
	tok, err = d.Token()
	if err != nil || tok != json.Delim('}') {
		return ErrInvalid
	}
	return nil
}
