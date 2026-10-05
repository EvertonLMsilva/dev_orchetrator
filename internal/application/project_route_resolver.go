package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"errors"
	"strings"
)

type ConversationSource struct {
	GuildID   string
	ChannelID string
}
type ProjectRoute struct {
	Source    ConversationSource
	ProjectID domain.ProjectID
}
type ProjectRouteResolver interface {
	Resolve(context.Context, ConversationSource) (domain.ProjectID, error)
}

var ErrConversationRoute = errors.New("invalid or unknown conversation route")

type ConfiguredProjectRoutes struct {
	routes map[ConversationSource]domain.ProjectID
}

func validConversationSource(s ConversationSource) bool {
	return strings.TrimSpace(s.GuildID) != "" && strings.TrimSpace(s.ChannelID) != ""
}
func NewConfiguredProjectRoutes(entries []ProjectRoute) (*ConfiguredProjectRoutes, error) {
	r := &ConfiguredProjectRoutes{routes: make(map[ConversationSource]domain.ProjectID)}
	for _, entry := range entries {
		if !validConversationSource(entry.Source) || strings.TrimSpace(string(entry.ProjectID)) == "" {
			return nil, ErrConversationRoute
		}
		if _, exists := r.routes[entry.Source]; exists {
			return nil, ErrConversationRoute
		}
		r.routes[entry.Source] = entry.ProjectID
	}
	return r, nil
}
func (r *ConfiguredProjectRoutes) Resolve(ctx context.Context, s ConversationSource) (domain.ProjectID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r == nil || !validConversationSource(s) {
		return "", ErrConversationRoute
	}
	id, ok := r.routes[s]
	if !ok {
		return "", ErrConversationRoute
	}
	return id, nil
}
