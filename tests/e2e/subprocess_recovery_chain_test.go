package e2e

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

func TestSchedulerRestoreDispatchesToHelperSubprocessPlugin(t *testing.T) {
	t.Parallel()

	storePath := filepath.Join(t.TempDir(), "state.db")
	store, err := runtimecore.OpenSQLiteStateStore(storePath)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer func() { _ = store.Close() }()

	seedScheduler := runtimecore.NewScheduler()
	seedScheduler.SetStore(store)
	if err := seedScheduler.Register(runtimecore.SchedulePlan{
		ID:        "delay-subprocess-restart",
		Kind:      runtimecore.ScheduleKindDelay,
		Delay:     time.Second,
		Source:    "scheduler",
		EventType: "schedule.triggered",
		Metadata:  map[string]any{"message": "subprocess restored"},
	}); err != nil {
		t.Fatalf("register seed delay plan: %v", err)
	}

	host := runtimecore.NewSubprocessPluginHost(testHelperPluginProcessFactory(t, "echo"))
	runtime := runtimecore.NewInMemoryRuntime(runtimecore.NoopSupervisor{}, host)
	plugin := pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{SchemaVersion: pluginsdk.SupportedPluginManifestSchemaVersion, ID: "plugin-subprocess-fixture", Name: "Subprocess Fixture", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Binary: "subprocess-fixture"}}, Handlers: pluginsdk.Handlers{Event: noopEventHandler{}}}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register subprocess fixture plugin: %v", err)
	}

	restartedScheduler := runtimecore.NewScheduler()
	restartedScheduler.SetStore(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatchErrs := make(chan error, 2)
	if err := restartedScheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		err := runtime.DispatchEvent(context.Background(), event)
		if err != nil {
			dispatchErrs <- err
		}
		return err
	}); err != nil {
		t.Fatalf("start restarted scheduler: %v", err)
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for len(runtime.DispatchResults()) == 0 && time.Now().Before(deadline) {
		select {
		case err := <-dispatchErrs:
			t.Fatalf("scheduler dispatch callback returned error: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	results := runtime.DispatchResults()
	if len(results) != 1 || !results[0].Success || results[0].PluginID != "plugin-subprocess-fixture" {
		t.Fatalf("unexpected dispatch results %+v", results)
	}
	if _, err := restartedScheduler.Plan("delay-subprocess-restart"); err == nil {
		t.Fatal("expected restored delay plan to be removed after subprocess dispatch")
	}
	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, marker := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, marker) {
			t.Fatalf("expected subprocess fixture stdout to include %q, got %s", marker, stdout)
		}
	}
}

func testHelperPluginProcessFactory(t *testing.T, mode string) func(context.Context) *exec.Cmd {
	t.Helper()
	return func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperSubprocessProcess", "--", mode)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_SUBPROCESS=1")
		return cmd
	}
}

type noopEventHandler struct{}

func (noopEventHandler) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	return nil
}

func TestHelperSubprocessProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_SUBPROCESS") != "1" {
		return
	}

	mode := os.Args[len(os.Args)-1]
	_, _ = os.Stderr.WriteString("stderr-online\n")
	_, _ = os.Stdout.WriteString("{\"type\":\"handshake\",\"status\":\"ok\",\"message\":\"handshake-ready\"}\n")

	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		line := reader.Text()
		if strings.Contains(line, `"type":"health"`) {
			_, _ = os.Stdout.WriteString("{\"type\":\"health\",\"status\":\"ok\",\"message\":\"healthy\"}\n")
			continue
		}
		if strings.Contains(line, `"type":"event"`) {
			_, _ = os.Stdout.WriteString(fmt.Sprintf("{\"type\":\"event\",\"status\":\"ok\",\"message\":\"event-ok:%s\"}\n", mode))
			continue
		}
	}
	os.Exit(0)
}
