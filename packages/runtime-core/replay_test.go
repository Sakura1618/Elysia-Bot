package runtimecore

import (
	"context"
	"strings"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

func TestEventReplayerRedispatchesStoredEventWithReplayIdentity(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	original := eventmodel.Event{
		EventID:        "evt-replay-source",
		TraceID:        "trace-replay-source",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 3, 23, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:replay:source",
		Metadata:       map[string]any{"origin": "stored"},
	}
	if err := store.RecordEvent(context.Background(), original); err != nil {
		t.Fatalf("record event: %v", err)
	}

	replayer := NewEventReplayer(store, runtime)
	replayer.now = func() time.Time { return time.Date(2026, 4, 3, 23, 30, 0, 123, time.UTC) }
	replayed, err := replayer.ReplayEvent(context.Background(), original.EventID)
	if err != nil {
		t.Fatalf("replay event: %v", err)
	}

	if !handler.called {
		t.Fatal("expected replay to redispatch through runtime")
	}
	if replayed.EventID == original.EventID || !strings.HasPrefix(replayed.EventID, "replay-evt-replay-source-") {
		t.Fatalf("expected replayed event id to be rewritten, got %+v", replayed)
	}
	if replayed.TraceID == original.TraceID || !strings.HasPrefix(replayed.TraceID, "replay-trace-replay-source-") {
		t.Fatalf("expected replayed trace id to be rewritten, got %+v", replayed)
	}
	if replayed.IdempotencyKey == original.IdempotencyKey || !strings.HasPrefix(replayed.IdempotencyKey, "replay:evt-replay-source:") {
		t.Fatalf("expected replayed idempotency key to be rewritten, got %+v", replayed)
	}
	if replayed.Metadata["replay"] != true || replayed.Metadata["replay_of"] != original.EventID || replayed.Metadata["replay_isolated"] != true {
		t.Fatalf("expected replay metadata markers, got %+v", replayed.Metadata)
	}
	if replayed.Metadata["replay_namespace"] != replayNamespace {
		t.Fatalf("expected replay namespace marker %q, got %+v", replayNamespace, replayed.Metadata)
	}
	if handler.ctx.EventID != replayed.EventID || handler.ctx.TraceID != replayed.TraceID {
		t.Fatalf("expected runtime execution context to use replayed identity, got %+v", handler.ctx)
	}
	records, err := store.ListReplayOperationRecords(context.Background())
	if err != nil {
		t.Fatalf("list replay operation records: %v", err)
	}
	if len(records) != 1 || records[0].SourceEventID != original.EventID || records[0].ReplayEventID != replayed.EventID || records[0].Status != "succeeded" || records[0].Reason != "replay_dispatched" {
		t.Fatalf("expected persisted replay success record, got %+v", records)
	}
}

func TestEventReplayerReturnsReplayIdentityOnDispatchFailure(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-fail", Name: "Fail Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/fail", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: failingEventHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	original := eventmodel.Event{EventID: "evt-replay-fail", TraceID: "trace-replay-fail", Source: "onebot", Type: "message.received", Timestamp: time.Date(2026, 4, 3, 23, 10, 0, 0, time.UTC), IdempotencyKey: "onebot:replay:fail"}
	if err := store.RecordEvent(context.Background(), original); err != nil {
		t.Fatalf("record event: %v", err)
	}

	replayer := NewEventReplayer(store, runtime)
	replayer.now = func() time.Time { return time.Date(2026, 4, 3, 23, 40, 0, 321, time.UTC) }
	replayed, err := replayer.ReplayEvent(context.Background(), original.EventID)
	if err == nil {
		t.Fatal("expected dispatch failure during replay")
	}
	if replayed.EventID == "" || replayed.TraceID == "" || replayed.IdempotencyKey == "" {
		t.Fatalf("expected replay identity to be preserved on failure, got %+v", replayed)
	}
	if replayed.Metadata["replay"] != true || replayed.Metadata["replay_of"] != original.EventID || replayed.Metadata["replay_isolated"] != true {
		t.Fatalf("expected replay metadata markers on failure, got %+v", replayed.Metadata)
	}
	if replayed.Metadata["replay_namespace"] != replayNamespace {
		t.Fatalf("expected replay namespace marker on failure, got %+v", replayed.Metadata)
	}
	records, listErr := store.ListReplayOperationRecords(context.Background())
	if listErr != nil {
		t.Fatalf("list replay operation records: %v", listErr)
	}
	if len(records) != 1 || records[0].SourceEventID != original.EventID || records[0].ReplayEventID != replayed.EventID || records[0].Status != "failed" || !strings.Contains(records[0].Reason, "dispatch") {
		t.Fatalf("expected persisted replay failure record, got %+v", records)
	}
}

func TestReplayPolicyDeclarationMatchesCurrentReplayBoundary(t *testing.T) {
	t.Parallel()

	declaration := ReplayPolicy()
	if declaration.Namespace != replayNamespace {
		t.Fatalf("expected replay namespace %q, got %+v", replayNamespace, declaration)
	}
	if declaration.IdentityIsolation == "" || declaration.ReplayOfReplay == "" || declaration.Summary == "" {
		t.Fatalf("expected non-empty replay policy summary fields, got %+v", declaration)
	}
	for _, expected := range []string{"single-event-explicit-replay", "isolated-replay-identity", "admin-command-authorized-entry"} {
		if !containsStringValue(declaration.SupportedModes, expected) {
			t.Fatalf("expected supported mode %q, got %+v", expected, declaration.SupportedModes)
		}
	}
	for _, expected := range []string{"batch-replay", "dry-run", "replay-write-api"} {
		if !containsStringValue(declaration.UnsupportedModes, expected) {
			t.Fatalf("expected unsupported mode %q, got %+v", expected, declaration.UnsupportedModes)
		}
	}
	if !containsStringValue(declaration.EntryPoints, "/admin replay <event-id>") {
		t.Fatalf("expected admin replay entry point, got %+v", declaration.EntryPoints)
	}
}

func containsStringValue(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func TestEventReplayerFailsWhenStoredEventIsMissing(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	replayer := NewEventReplayer(store, NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{}))

	_, err := replayer.ReplayEvent(context.Background(), "evt-missing")
	if err == nil {
		t.Fatal("expected replay of missing event to fail")
	}
	if !strings.Contains(err.Error(), "load event journal") {
		t.Fatalf("expected missing journal error, got %v", err)
	}
}

func TestEventReplayerRejectsReplayTaggedStoredEvent(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	original := eventmodel.Event{
		EventID:        "evt-replay-tagged",
		TraceID:        "trace-replay-tagged",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:replay:tagged",
		Metadata:       map[string]any{"replay": true, "replay_of": "evt-original", "replay_isolated": true},
	}
	if err := store.RecordEvent(context.Background(), original); err != nil {
		t.Fatalf("record event: %v", err)
	}

	replayer := NewEventReplayer(store, runtime)
	_, err := replayer.ReplayEvent(context.Background(), original.EventID)
	if err == nil || !strings.Contains(err.Error(), "cannot replay a replayed event") {
		t.Fatalf("expected replay-tagged event to be rejected, got %v", err)
	}
	if handler.called {
		t.Fatal("expected replay-tagged event not to be redispatched")
	}
}

func TestEventReplayerRejectsStoredEventWithReplayIdentityPrefixEvenWithoutMetadataFlags(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	original := eventmodel.Event{
		EventID:        "replay-evt-prefixed-1",
		TraceID:        "replay-trace-prefixed-1",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 4, 0, 10, 0, 0, time.UTC),
		IdempotencyKey: "replay:evt-prefixed-1:123",
		Metadata:       map[string]any{"origin": "stored-with-prefixed-identity"},
	}
	if err := store.RecordEvent(context.Background(), original); err != nil {
		t.Fatalf("record event: %v", err)
	}

	replayer := NewEventReplayer(store, runtime)
	_, err := replayer.ReplayEvent(context.Background(), original.EventID)
	if err == nil || !strings.Contains(err.Error(), "cannot replay a replayed event") {
		t.Fatalf("expected replay-identity-prefixed event to be rejected, got %v", err)
	}
	if handler.called {
		t.Fatal("expected prefixed replay identity not to be redispatched")
	}
}

func TestEventReplayerAllowsUnrelatedReplayOfMetadataWithoutReplayFlags(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	original := eventmodel.Event{
		EventID:        "evt-business-replayof",
		TraceID:        "trace-business-replayof",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 4, 0, 30, 0, 0, time.UTC),
		IdempotencyKey: "onebot:business:replayof",
		Metadata:       map[string]any{"replay_of": "business-reference"},
	}
	if err := store.RecordEvent(context.Background(), original); err != nil {
		t.Fatalf("record event: %v", err)
	}

	replayer := NewEventReplayer(store, runtime)
	replayed, err := replayer.ReplayEvent(context.Background(), original.EventID)
	if err != nil {
		t.Fatalf("expected unrelated replay_of metadata not to block replay, got %v", err)
	}
	if !handler.called || replayed.EventID == "" {
		t.Fatalf("expected replay to continue normally, got replayed=%+v handlerCalled=%v", replayed, handler.called)
	}
}
