package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dev-orchestrator/internal/domain"
)

func TestTestTargetRegistry(t *testing.T) {
	if reflect.TypeOf(TestTargetRegistry{}).NumField() != 0 {
		t.Fatal("registry must expose no mutable configuration")
	}
	for id, want := range map[domain.TestTargetID]string{"all": "./...", "domain": "./internal/domain", "application": "./internal/application", "ports": "./internal/ports", "adapters": "./internal/adapters/..."} {
		got, err := (TestTargetRegistry{}).Resolve(id)
		if err != nil || got != want {
			t.Fatalf("%s: %q, %v", id, got, err)
		}
	}
	for _, id := range []domain.TestTargetID{"", "unknown", "-race", "all -race", "../outside", "./..."} {
		if _, err := (TestTargetRegistry{}).Resolve(id); !errors.Is(err, ErrUnknownTestTarget) {
			t.Fatalf("%q: %v", id, err)
		}
	}
	// No map, setter, or registration API is exposed; every zero value has the same closed registry.
	a, b := TestTargetRegistry{}, TestTargetRegistry{}
	x, _ := a.Resolve("all")
	y, _ := b.Resolve("all")
	if x != y {
		t.Fatal("registry values differ")
	}
}

func testModule(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "domain")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(root, "go.mod"):         "module fixture\n\ngo 1.22\n",
		filepath.Join(dir, "fixture_test.go"): "package fixture\nimport (\"testing\"; \"fmt\"; \"os\"; \"time\")\nvar _ = fmt.Print\nvar _ = os.Stderr\nvar _ = time.Sleep\nfunc TestFixture(t *testing.T) {" + body + "}\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRunTestsPassing(t *testing.T) {
	root := testModule(t, "")
	for _, id := range []domain.TestTargetID{"domain", "all"} {
		result, err := RunTests(context.Background(), root, domain.RunTestsParams{Target: id})
		if err != nil || !result.Success || result.ExitCode != 0 || result.Target != id || !strings.Contains(result.Stdout, "ok") {
			t.Fatalf("%+v: %v", result, err)
		}
	}
}

func TestRunTestsFailingOutput(t *testing.T) {
	root := testModule(t, `fmt.Println("stdout-marker"); fmt.Fprintln(os.Stderr,"stderr-marker"); t.Fatal("failure-marker")`)
	result, err := RunTests(context.Background(), root, domain.RunTestsParams{Target: "domain"})
	// go test merges test-binary stderr into its own stdout.
	if err != nil || result.Success || result.ExitCode != 1 || !strings.Contains(result.Stdout, "stdout-marker") || !strings.Contains(result.Stdout, "stderr-marker") {
		t.Fatalf("%+v: %v", result, err)
	}
}

func TestRunTestsRejectedTargets(t *testing.T) {
	root := testModule(t, "")
	for _, id := range []domain.TestTargetID{"-race", "all -race", "../outside", "unknown", ""} {
		if _, err := RunTests(context.Background(), root, domain.RunTestsParams{Target: id}); !errors.Is(err, ErrUnknownTestTarget) {
			t.Fatalf("%q: %v", id, err)
		}
	}
}

func TestRunTestsModuleBoundary(t *testing.T) {
	root := testModule(t, "")
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := RunTests(context.Background(), child, domain.RunTestsParams{Target: "all"}); !errors.Is(err, ErrTestWorkspace) {
		t.Fatal(err)
	}
}

func TestRunTestsSymlinkBoundary(t *testing.T) {
	for _, name := range []string{"go.mod", "internal/domain"} {
		t.Run(name, func(t *testing.T) {
			root := testModule(t, "")
			outside := testModule(t, "")
			path := filepath.Join(root, name)
			// Rename only fixture paths; preserve the directory for cleanup.
			if err := os.Rename(path, path+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, name), path); err != nil {
				t.Fatal(err)
			}
			if _, err := RunTests(context.Background(), root, domain.RunTestsParams{Target: "domain"}); !errors.Is(err, ErrTestWorkspace) {
				t.Fatal(err)
			}
		})
	}
}

func TestRunTestsIgnoresGoEnvironment(t *testing.T) {
	root := testModule(t, "")
	t.Setenv("GOFLAGS", "-invalid-planner-flag")
	t.Setenv("GOWORK", filepath.Join(t.TempDir(), "external.work"))
	t.Setenv("GOENV", filepath.Join(t.TempDir(), "external.env"))
	result, err := RunTests(context.Background(), root, domain.RunTestsParams{Target: "all"})
	if err != nil || !result.Success {
		t.Fatalf("%+v: %v", result, err)
	}
}

func TestRunTestsCancellation(t *testing.T) {
	root := testModule(t, `time.Sleep(30*time.Second)`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunTests(ctx, root, domain.RunTestsParams{Target: "all"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, deadline := range []bool{false, true} {
		ctx, cancel = context.WithCancel(context.Background())
		want := context.Canceled
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
			want = context.DeadlineExceeded
		} else {
			time.AfterFunc(500*time.Millisecond, cancel)
		}
		started := time.Now()
		_, err := RunTests(ctx, root, domain.RunTestsParams{Target: "all"})
		cancel()
		if !errors.Is(err, want) || time.Since(started) > 5*time.Second {
			t.Fatalf("%v after %v", err, time.Since(started))
		}
	}
}

func TestRunTestsOutputLimit(t *testing.T) {
	root := testModule(t, `for i:=0;i<200000;i++ { fmt.Println("output output output") }; t.Fail()`)
	_, err := RunTests(context.Background(), root, domain.RunTestsParams{Target: "all"})
	if !errors.Is(err, ErrTestOutputLimit) {
		t.Fatal(err)
	}
}

func TestRunTestsStderr(t *testing.T) {
	root := testModule(t, "")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("invalid module contents\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := RunTests(context.Background(), root, domain.RunTestsParams{Target: "all"})
	if err != nil || result.Success || !strings.Contains(result.Stderr, "go.mod") {
		t.Fatalf("%+v: %v", result, err)
	}
}

func TestTestOutputStreamsBounded(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			output := processOutput{limit: MaxTestOutputBytes, stop: func() {}}
			if _, err := output.Write(make([]byte, MaxTestOutputBytes)); err != nil {
				t.Fatal(err)
			}
			if _, err := output.Write([]byte("x")); !errors.Is(err, ErrProcessOutputLimit) || output.buffer.Len() > MaxTestOutputBytes {
				t.Fatal(err)
			}
		})
	}
}
