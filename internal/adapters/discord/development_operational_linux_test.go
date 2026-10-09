//go:build linux

package discord

import (
	"bytes"
	"context"
	"dev-orchestrator/internal/composition"
	"dev-orchestrator/internal/domain"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
)

func TestGatewayOperationalDeniedTargetResponds(t *testing.T) {
	for _, scenario := range []struct{ name, guild, channel, target string }{
		{"legacy-channel-id", `"guild_id":"663567354445299717"`, `"channel_id":"1557063843690061956"`, "summary.txt"},
		{"legacy-channel", `"guild_id":"663567354445299717"`, `"channel_id":"1557063843690061956","channel":{"id":"1557063843690061956","type":0}`, "summary.txt"},
		{"partial-guild-denied", `"guild":{"id":"663567354445299717"}`, `"channel":{"id":"1557063843690061956","type":0}`, "summary.txt"},
		{"partial-guild-permitted", `"guild":{"id":"663567354445299717"}`, `"channel":{"id":"1557063843690061956","type":0}`, "note.txt"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			previousWriter, previousFlags := log.Writer(), log.Flags()
			log.SetOutput(&diagnostics)
			log.SetFlags(0)
			t.Cleanup(func() { log.SetOutput(previousWriter); log.SetFlags(previousFlags) })
			data, err := os.ReadFile("../../composition/testdata/operational-development.example.json")
			if err != nil {
				t.Fatal(err)
			}
			var config composition.OperationalDevelopmentConfig
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			private := func() string {
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				return root
			}
			config.Registry.Routes[0].Source.GuildID = "663567354445299717"
			config.Registry.Routes[0].Source.ChannelID = "1557063843690061956"
			config.Mappings[0].Evidence.ExternalID = "355827548598435843"
			config.Mappings[0].Principal.ID = "pilot-operator"
			for i := range config.Grants {
				config.Grants[i].PrincipalID = "pilot-operator"
			}
			config.Registry.StateDir = private()
			config.ScratchRoot = private()
			for i := range config.Registry.Projects {
				config.Registry.Projects[i].Workspace = private()
			}
			service, err := composition.NewOperationalDevelopmentService(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
			// Stop at a controlled bootstrap boundary after the production gates.
			// No Planner, credentials, network, or automatic WRITE is needed.
			if scenario.target == "note.txt" {
				if err := os.Remove(config.Registry.Projects[0].Workspace); err != nil {
					t.Fatal(err)
				}
			}
			r := &conversationREST{updated: make(chan struct{})}
			g, err := NewGateway("test-only")
			if err != nil {
				t.Fatal(err)
			}
			g.commands = r
			g.client = &fakeConversationGateway{}
			if err := g.SetDevelopmentService(context.Background(), service); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = g.Close(context.Background()) })
			raw := []byte(`{"id":"123","application_id":"456",` + scenario.guild + `,` + scenario.channel + `,"member":{"user":{"id":"355827548598435843","username":"operator"},"roles":[]},"token":"test-only","version":1,"type":2,"data":{"id":"789","name":"develop","type":1,"options":[{"name":"intent","type":3,"value":"Crie ` + scenario.target + ` com o texto P11.3 INTENT FIX PASS."},{"name":"targets","type":3,"value":"[\"` + scenario.target + `\"]"}]}}`)
			started := time.Now()
			g.onEvent(nil, gateway.EventTypeRaw, 1, gateway.EventRaw{EventType: gateway.EventTypeInteractionCreate, Payload: bytes.NewReader(raw)})
			decoded, err := gateway.UnmarshalEventData(raw, gateway.EventTypeInteractionCreate)
			if err != nil {
				t.Fatal(err)
			}
			g.onEvent(nil, gateway.EventTypeInteractionCreate, 1, decoded)
			if r.responses != 1 || r.response.Type != sdk.InteractionResponseTypeDeferredCreateMessage {
				t.Fatal("Gateway did not ACK denied /develop interaction")
			}
			if time.Since(started) >= 3*time.Second {
				t.Fatal("ACK missed interaction deadline")
			}
			select {
			case <-r.updated:
			case <-time.After(time.Second):
				t.Fatal("denied target response missing")
			}
			prefix := "Targets solicitados negados. Referência: "
			if scenario.target == "note.txt" {
				prefix = "Bootstrap bloqueado. Referência: "
			}
			if !strings.HasPrefix(r.content, prefix) || strings.Contains(r.content, "P11.3 INTENT FIX PASS") {
				t.Fatal("missing sanitized target rejection")
			}
			stage := "TARGET_DENIED\n"
			if scenario.target == "note.txt" {
				stage = "BOOTSTRAP_DENIED\n"
			}
			if diagnostics.String() != stage {
				t.Fatal("diagnostic must contain only the fixed denial classification")
			}
			persisted, err := os.ReadFile(filepath.Join(config.Registry.StateDir, "operational.json"))
			if err != nil {
				t.Fatal("pre-generation target gate did not execute", err)
			}
			var records map[string]struct {
				ProjectID        domain.ProjectID
				RecoveryRequired bool
				Output           struct {
					Review json.RawMessage
					Result struct {
						TaskState                           domain.TaskStatus
						WriteTransaction, Branch, CommitOID string
						PlannerFailureStage                 string
					}
				}
			}
			if err := json.Unmarshal(persisted, &records); err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 {
				t.Fatal("duplicate/missing domain execution")
			}
			for _, record := range records {
				if scenario.target == "note.txt" && record.Output.Result.PlannerFailureStage != "BOOTSTRAP" {
					t.Fatal("permitted target did not pass route/auth/target/policy gates")
				}
				if record.ProjectID != "alpha" || record.RecoveryRequired || record.Output.Result.TaskState != domain.TaskStatusBlocked || string(record.Output.Review) != "null" || record.Output.Result.WriteTransaction != "" || record.Output.Result.Branch != "" || record.Output.Result.CommitOID != "" {
					t.Fatal("denial produced downstream effects")
				}
			}
			for _, root := range []string{config.ScratchRoot, config.Registry.Projects[0].Workspace, config.Registry.Projects[1].Workspace} {
				entries, err := os.ReadDir(root)
				if scenario.target == "note.txt" && root == config.Registry.Projects[0].Workspace && os.IsNotExist(err) {
					continue
				}
				if err != nil || len(entries) != 0 {
					t.Fatal("denied target bootstrapped/generated/wrote workspace", err)
				}
			}
		})
	}
}
