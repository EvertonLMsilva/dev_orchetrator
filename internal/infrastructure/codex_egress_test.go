package infrastructure

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func TestProxyDiagnosticDockerStream(t *testing.T) {
	for _, valid := range []bool{true, false} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload := []byte("CONNECT_HOST chatgpt.com 443\n")
			header := make([]byte, 8)
			header[0] = 1
			if !valid {
				header[0] = 3
			}
			binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
			w.Write(header)
			w.Write(payload)
		}))
		sdk, err := client.New(client.WithHost(server.URL), client.WithVersion("1.53"))
		if err != nil {
			t.Fatal(err)
		}
		owned := &ownedCodexEgress{DockerDriver: &DockerDriver{client: sdk}, proxy: "proxy"}
		got, err := owned.proxyDiagnostics(context.Background(), []string{"chatgpt.com"})
		if valid && (err != nil || len(got) != 1 || got[0].Decision != "ALLOW") {
			t.Fatal("complete Docker log stream rejected")
		}
		if !valid && (err == nil || got != nil) {
			t.Fatal("malformed Docker stream accepted")
		}
		sdk.Close()
		server.Close()
	}
}

func TestSafeProxyDiagnostics(t *testing.T) {
	logs := "CONNECT_HOST chatgpt.com 443\nCONNECT_HOST unknown.example 443\nCONNECT_HOST chatgpt.com 80\nCONNECT_HOST user:TOKEN_SECRET@chatgpt.com 443\nDENIED_HOST unknown.example\nraw TOKEN_SECRET\n"
	got := safeProxyDiagnostics(logs, []string{"chatgpt.com"})
	want := []codexProxyDiagnostic{{"chatgpt.com", 443, "ALLOW"}, {"unknown.example", 443, "DENY"}, {"chatgpt.com", 80, "DENY"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("proxy diagnostics mismatch")
	}
}

func TestCodexEgressProvisioningCleanup(t *testing.T) {
	for _, stage := range []string{"success", "private", "external", "proxy-create", "proxy-start", "health", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			var removed []string
			networks := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(r.URL.Path, "/v1.53")
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodDelete {
					removed = append(removed, path)
					if stage == "cleanup" && path == "/containers/workload" {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"TEST_SECRET_DO_NOT_LEAK"}`)
						return
					}
					w.WriteHeader(204)
					return
				}
				switch path {
				case "/networks/create":
					networks++
					var request network.CreateRequest
					if json.NewDecoder(r.Body).Decode(&request) != nil || request.Internal != (networks == 1) || request.Driver != "bridge" || !strings.HasPrefix(request.Name, "codex-egress-") {
						t.Error("unsafe network spec")
					}
					if stage == "private" && networks == 1 || stage == "external" && networks == 2 {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"TEST_SECRET_DO_NOT_LEAK"}`)
						return
					}
					fmt.Fprintf(w, `{"Id":"network-%d"}`, networks)
				case "/containers/create":
					if stage == "proxy-create" {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"TEST_SECRET_DO_NOT_LEAK"}`)
						return
					}
					fmt.Fprint(w, `{"Id":"proxy"}`)
				case "/containers/proxy/start":
					if stage == "proxy-start" {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"TEST_SECRET_DO_NOT_LEAK"}`)
						return
					}
					w.WriteHeader(204)
				case "/containers/proxy/exec":
					fmt.Fprint(w, `{"Id":"health"}`)
				case "/exec/health/start":
					w.WriteHeader(200)
				case "/exec/health/json":
					exit := 0
					if stage == "health" {
						exit = 1
					}
					fmt.Fprintf(w, `{"Running":false,"ExitCode":%d}`, exit)
				default:
					t.Errorf("unexpected Docker operation: %s", path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			sdk, err := client.New(client.WithHost(server.URL), client.WithVersion("1.53"))
			if err != nil {
				t.Fatal(err)
			}
			defer sdk.Close()
			d := &DockerDriver{client: sdk}
			owned, err := d.prepareCodexEgress(context.Background(), []string{"chatgpt.com"})
			if stage == "success" || stage == "cleanup" {
				if err != nil || owned == nil {
					t.Fatal("provisioning failed")
				}
				err = owned.Remove(context.Background(), "workload")
				if again := owned.Remove(context.Background(), "workload"); (again == nil) != (err == nil) {
					t.Fatal("non-idempotent cleanup")
				}
			} else if err == nil {
				t.Fatal("provisioning failure accepted")
			}
			if stage == "cleanup" && err == nil {
				t.Fatal("cleanup failure lost")
			}
			if err != nil && strings.Contains(err.Error(), "TEST_SECRET") {
				t.Fatal("daemon secret exposed")
			}
			var want []string
			switch stage {
			case "private":
				want = nil
			case "external":
				want = []string{"/networks/network-1"}
			case "proxy-create":
				want = []string{"/networks/network-1", "/networks/network-2"}
			case "proxy-start", "health":
				want = []string{"/containers/proxy", "/networks/network-1", "/networks/network-2"}
			default:
				want = []string{"/containers/workload", "/containers/proxy", "/networks/network-1", "/networks/network-2"}
			}
			if !reflect.DeepEqual(removed, want) {
				t.Fatalf("cleanup mismatch: %v", removed)
			}
		})
	}
}
