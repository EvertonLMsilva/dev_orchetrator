//go:build linux

package composition

import (
	"bytes"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/infrastructure"
	"encoding/json"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (c DevelopmentConfig) Validate() error {
	if !validDiscordID(c.Route.GuildID) || !validDiscordID(c.Route.ChannelID) || c.Policy.Validate() != nil || len(c.Policy.WriteTargets) != 1 || c.Policy.WriteTargets[0] != "note.txt" || len(c.Policy.InputTargets) != 0 || c.Policy.Limits.MaxFileBytes > 256 {
		return ErrConfig
	}
	if _, err := infrastructure.NewActorAuthority(c.Mappings, c.Grants); err != nil {
		return ErrConfig
	}
	for _, m := range c.Mappings {
		if m.Evidence.Provider != "discord" || !validDiscordID(m.Evidence.ExternalID) {
			return ErrConfig
		}
	}
	dirs := []string{c.ControlRoot, c.ScratchRoot, c.StateRoot}
	for i, path := range dirs {
		if _, err := canonicalDir(path); err != nil || contains("/var/lib/dev-orchestrator/runtime-auth", path) || contains(path, "/var/lib/dev-orchestrator/runtime-auth") {
			return ErrConfig
		}
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return ErrConfig
		}
		var st unix.Stat_t
		if unix.Lstat(path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || st.Uid != uint32(os.Geteuid()) {
			return ErrConfig
		}
		for _, other := range dirs[:i] {
			if path == other || strings.HasPrefix(path, other+"/") || strings.HasPrefix(other, path+"/") {
				return ErrConfig
			}
		}
	}
	if (domain.CandidateContext{ProjectID: c.ProjectID, TaskID: c.TaskID, CorrelationID: c.CorrelationID}).Validate() != nil || !domain.GitBranchName(c.Branch) || c.PolicyVersion == "" {
		return ErrConfig
	}
	return nil
}
func LoadDevelopmentConfig(path string) (DevelopmentConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return DevelopmentConfig{}, ErrConfig
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 || uniqueJSON(data) != nil {
		return DevelopmentConfig{}, ErrConfig
	}
	var c DevelopmentConfig
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || c.Validate() != nil {
		return DevelopmentConfig{}, ErrConfig
	}
	return c, nil
}
