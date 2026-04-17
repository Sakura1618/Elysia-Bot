package runtimecore

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

func TestSQLiteStateStorePersistsCoreMetadata(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	event := eventmodel.Event{
		EventID:        "evt-store-1",
		TraceID:        "trace-store-1",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 2, 17, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:msg:store-1",
	}

	if err := store.RecordEvent(ctx, event); err != nil {
		t.Fatalf("record event: %v", err)
	}
	if err := store.SavePluginManifest(ctx, pluginsdk.PluginManifest{
		ID:         "plugin-echo",
		Name:       "Echo Plugin",
		Version:    "0.1.0",
		APIVersion: "v0",
		Mode:       pluginsdk.ModeSubprocess,
		Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-echo", Symbol: "Plugin"},
	}); err != nil {
		t.Fatalf("save plugin manifest: %v", err)
	}
	if err := store.SaveSession(ctx, SessionState{
		SessionID: "session-1",
		PluginID:  "plugin-echo",
		State:     map[string]any{"last_message": "hello"},
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if err := store.SaveIdempotencyKey(ctx, event.IdempotencyKey, event.EventID); err != nil {
		t.Fatalf("save idempotency key: %v", err)
	}
	job := NewJob("job-store-1", "ai.chat", 2, 30*time.Second)
	job.TraceID = "trace-job-store-1"
	job.EventID = event.EventID
	job.RunID = "run-job-store-1"
	job.Correlation = event.IdempotencyKey
	job.Payload = map[string]any{"prompt": "hello"}
	if err := store.SaveJob(ctx, job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	dueAt := time.Date(2026, 4, 7, 12, 0, 5, 0, time.UTC)
	if err := store.SaveSchedulePlan(ctx, storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "schedule-store-1",
			Kind:      ScheduleKindDelay,
			Delay:     5 * time.Second,
			Source:    "runtime-demo-scheduler",
			EventType: "message.received",
			Metadata:  map[string]any{"message_text": "hello scheduler"},
		},
		DueAt:     &dueAt,
		CreatedAt: time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}

	counts, err := store.Counts(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts["event_journal"] != 1 || counts["plugin_registry"] != 1 || counts["plugin_enabled_overlays"] != 0 || counts["plugin_configs"] != 0 || counts["plugin_status_snapshots"] != 0 || counts["sessions"] != 1 || counts["idempotency_keys"] != 1 || counts["jobs"] != 1 || counts["alerts"] != 0 || counts["schedule_plans"] != 1 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
}

func TestSQLiteStateStorePersistsAdapterInstancesAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	rawConfig := []byte(`{"mode":"demo-ingress","demo_path":"/demo/onebot/message","platform":"onebot/v11"}`)

	store, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.SaveAdapterInstance(ctx, AdapterInstanceState{
		InstanceID: "adapter-onebot-demo",
		Adapter:    "onebot",
		Source:     "onebot",
		RawConfig:  rawConfig,
		Status:     "registered",
		Health:     "ready",
		Online:     true,
	}); err != nil {
		t.Fatalf("save adapter instance: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	state, err := reopened.LoadAdapterInstance(ctx, "adapter-onebot-demo")
	if err != nil {
		t.Fatalf("load adapter instance: %v", err)
	}
	if state.InstanceID != "adapter-onebot-demo" || state.Adapter != "onebot" || state.Source != "onebot" {
		t.Fatalf("expected persisted adapter identity after reopen, got %+v", state)
	}
	var config map[string]any
	if err := json.Unmarshal(state.RawConfig, &config); err != nil {
		t.Fatalf("unmarshal raw adapter config: %v", err)
	}
	if config["mode"] != "demo-ingress" || config["demo_path"] != "/demo/onebot/message" || config["platform"] != "onebot/v11" {
		t.Fatalf("expected persisted adapter config fields after reopen, got %+v", config)
	}
	if state.Status != "registered" || state.Health != "ready" || !state.Online {
		t.Fatalf("expected persisted adapter status facts after reopen, got %+v", state)
	}
	states, err := reopened.ListAdapterInstances(ctx)
	if err != nil {
		t.Fatalf("list adapter instances: %v", err)
	}
	if len(states) != 1 || states[0].InstanceID != "adapter-onebot-demo" {
		t.Fatalf("expected one persisted adapter instance, got %+v", states)
	}
	counts, err := reopened.Counts(ctx)
	if err != nil {
		t.Fatalf("counts after reopen: %v", err)
	}
	if counts["adapter_instances"] != 1 {
		t.Fatalf("expected one persisted adapter instance row, got %+v", counts)
	}
}

func TestSQLiteStateStoreRetainsMetadataAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	store, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.SavePluginManifest(ctx, pluginsdk.PluginManifest{
		ID:         "plugin-echo",
		Name:       "Echo Plugin",
		Version:    "0.1.0",
		APIVersion: "v0",
		Mode:       pluginsdk.ModeSubprocess,
		Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-echo", Symbol: "Plugin"},
	}); err != nil {
		t.Fatalf("save plugin manifest: %v", err)
	}
	if err := store.SaveIdempotencyKey(ctx, "onebot:msg:reopen", "evt-reopen"); err != nil {
		t.Fatalf("save idempotency key: %v", err)
	}
	job := NewJob("job-reopen-1", "demo.echo", 1, 15*time.Second)
	job.TraceID = "trace-reopen-1"
	job.EventID = "evt-reopen"
	job.Correlation = "corr-reopen"
	if err := store.SaveJob(ctx, job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	if err := store.SaveSchedulePlan(ctx, storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "schedule-reopen-1",
			Kind:      ScheduleKindCron,
			CronExpr:  "0 * * * *",
			Source:    "runtime-demo-scheduler",
			EventType: "message.received",
		},
		CreatedAt: time.Date(2026, 4, 7, 13, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 7, 13, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}
	_ = store.Close()

	reopened, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	counts, err := reopened.Counts(ctx)
	if err != nil {
		t.Fatalf("counts after reopen: %v", err)
	}
	if counts["plugin_registry"] != 1 || counts["plugin_enabled_overlays"] != 0 || counts["plugin_configs"] != 0 || counts["plugin_status_snapshots"] != 0 || counts["idempotency_keys"] != 1 || counts["jobs"] != 1 || counts["alerts"] != 0 || counts["schedule_plans"] != 1 {
		t.Fatalf("expected persisted metadata after reopen, got %+v", counts)
	}
}

func TestSQLiteStateStorePersistsPluginConfigAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	rawConfig := []byte(`{"prefix":"persisted: "}`)

	store, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.SavePluginConfig(ctx, "plugin-echo", rawConfig); err != nil {
		t.Fatalf("save plugin config: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	state, err := reopened.LoadPluginConfig(ctx, "plugin-echo")
	if err != nil {
		t.Fatalf("load plugin config: %v", err)
	}
	if state.PluginID != "plugin-echo" {
		t.Fatalf("expected plugin-echo config state, got %+v", state)
	}
	if string(state.RawConfig) != string(rawConfig) {
		t.Fatalf("expected raw config %s, got %s", string(rawConfig), string(state.RawConfig))
	}
	counts, err := reopened.Counts(ctx)
	if err != nil {
		t.Fatalf("counts after reopen: %v", err)
	}
	if counts["plugin_configs"] != 1 {
		t.Fatalf("expected one persisted plugin config row, got %+v", counts)
	}
}

func TestSQLiteStateStorePersistsPluginEnabledOverlayAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	store, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.SavePluginManifest(ctx, pluginsdk.PluginManifest{
		ID:         "plugin-echo",
		Name:       "Echo Plugin",
		Version:    "0.1.0",
		APIVersion: "v0",
		Mode:       pluginsdk.ModeSubprocess,
		Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-echo", Symbol: "Plugin"},
	}); err != nil {
		t.Fatalf("save plugin manifest: %v", err)
	}
	if err := store.SavePluginEnabledState(ctx, "plugin-echo", false); err != nil {
		t.Fatalf("save plugin enabled state: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	state, err := reopened.LoadPluginEnabledState(ctx, "plugin-echo")
	if err != nil {
		t.Fatalf("load plugin enabled state: %v", err)
	}
	if state.PluginID != "plugin-echo" || state.Enabled {
		t.Fatalf("expected persisted disabled overlay after reopen, got %+v", state)
	}
	states, err := reopened.ListPluginEnabledStates(ctx)
	if err != nil {
		t.Fatalf("list plugin enabled states: %v", err)
	}
	if len(states) != 1 || states[0].PluginID != "plugin-echo" || states[0].Enabled {
		t.Fatalf("expected one persisted disabled plugin overlay, got %+v", states)
	}
	counts, err := reopened.Counts(ctx)
	if err != nil {
		t.Fatalf("counts after reopen: %v", err)
	}
	if counts["plugin_enabled_overlays"] != 1 {
		t.Fatalf("expected one persisted plugin enabled overlay, got %+v", counts)
	}
}

func TestSQLiteStateStoreReturnsNoRowsForMissingPluginEnabledState(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	if _, err := store.LoadPluginEnabledState(context.Background(), "plugin-missing"); err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows for missing plugin enabled state, got %v", err)
	}
}

func TestSQLiteStateStorePersistsPluginStatusSnapshotsAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	failureAt := time.Date(2026, 4, 15, 8, 0, 0, 0, time.UTC)
	recoveredAt := failureAt.Add(2 * time.Minute)

	store, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.SavePluginStatusSnapshot(ctx, DispatchResult{PluginID: "plugin-echo", Kind: "event", Success: false, Error: "timeout", At: failureAt}); err != nil {
		t.Fatalf("save failed plugin snapshot: %v", err)
	}
	if err := store.SavePluginStatusSnapshot(ctx, DispatchResult{PluginID: "plugin-echo", Kind: "event", Success: true, At: recoveredAt}); err != nil {
		t.Fatalf("save recovered plugin snapshot: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	snapshot, err := reopened.LoadPluginStatusSnapshot(ctx, "plugin-echo")
	if err != nil {
		t.Fatalf("load plugin snapshot after reopen: %v", err)
	}
	if snapshot.PluginID != "plugin-echo" || snapshot.LastDispatchKind != "event" || !snapshot.LastDispatchSuccess {
		t.Fatalf("expected reopened plugin snapshot identity/outcome, got %+v", snapshot)
	}
	if !snapshot.LastDispatchAt.Equal(recoveredAt) {
		t.Fatalf("expected last dispatch at %s, got %+v", recoveredAt, snapshot)
	}
	if snapshot.LastRecoveredAt == nil || !snapshot.LastRecoveredAt.Equal(recoveredAt) || snapshot.LastRecoveryFailureCount != 1 || snapshot.CurrentFailureStreak != 0 {
		t.Fatalf("expected recovery facts after reopen, got %+v", snapshot)
	}

	counts, err := reopened.Counts(ctx)
	if err != nil {
		t.Fatalf("counts after reopen: %v", err)
	}
	if counts["plugin_status_snapshots"] != 1 {
		t.Fatalf("expected one persisted plugin snapshot, got %+v", counts)
	}
}

func TestSQLiteStateStorePersistsSchedulePlansRoundTrip(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	createdAt := time.Date(2026, 4, 7, 14, 0, 0, 0, time.UTC)
	dueAt := time.Date(2026, 4, 7, 14, 5, 0, 0, time.UTC)
	claimedAt := time.Date(2026, 4, 7, 14, 4, 30, 0, time.UTC)
	record := storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "schedule-roundtrip-1",
			Kind:      ScheduleKindOneShot,
			ExecuteAt: time.Date(2026, 4, 7, 14, 10, 0, 0, time.UTC),
			Source:    "runtime-demo-scheduler",
			EventType: "message.received",
			Metadata:  map[string]any{"message_text": "hello once"},
		},
		DueAt:         &dueAt,
		DueAtEvidence: scheduleDueAtEvidencePersisted,
		ClaimOwner:    "runtime-local:scheduler",
		ClaimedAt:     &claimedAt,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}

	if err := store.SaveSchedulePlan(context.Background(), record); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}

	loaded, err := store.LoadSchedulePlan(context.Background(), record.Plan.ID)
	if err != nil {
		t.Fatalf("load schedule plan: %v", err)
	}
	if loaded.Plan.ID != record.Plan.ID || loaded.Plan.Kind != record.Plan.Kind || loaded.Plan.EventType != record.Plan.EventType {
		t.Fatalf("expected loaded schedule identity to match, got %+v", loaded)
	}
	if !loaded.Plan.ExecuteAt.Equal(record.Plan.ExecuteAt) || loaded.DueAt == nil || !loaded.DueAt.Equal(dueAt) {
		t.Fatalf("expected execute/due timestamps to round-trip, got %+v", loaded)
	}
	if loaded.DueAtEvidence != scheduleDueAtEvidencePersisted {
		t.Fatalf("expected dueAt evidence to round-trip, got %+v", loaded)
	}
	if loaded.ClaimOwner != record.ClaimOwner || loaded.ClaimedAt == nil || !loaded.ClaimedAt.Equal(claimedAt) {
		t.Fatalf("expected claim ownership fields to round-trip, got %+v", loaded)
	}
	if loaded.Plan.Metadata["message_text"] != "hello once" {
		t.Fatalf("expected schedule metadata to round-trip, got %+v", loaded.Plan.Metadata)
	}
	plans, err := store.ListSchedulePlans(context.Background())
	if err != nil {
		t.Fatalf("list schedule plans: %v", err)
	}
	if len(plans) != 1 || plans[0].Plan.ID != record.Plan.ID {
		t.Fatalf("expected one listed schedule plan, got %+v", plans)
	}

	if err := store.DeleteSchedulePlan(context.Background(), record.Plan.ID); err != nil {
		t.Fatalf("delete schedule plan: %v", err)
	}
	if _, err := store.LoadSchedulePlan(context.Background(), record.Plan.ID); err == nil {
		t.Fatal("expected deleted schedule plan to be gone")
	}
}

func TestSQLiteStateStoreRetainsSchedulePlansAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "schedule-reopen-2",
			Kind:      ScheduleKindDelay,
			Delay:     30 * time.Second,
			Source:    "runtime-demo-scheduler",
			EventType: "message.received",
		},
		CreatedAt: time.Date(2026, 4, 7, 15, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 7, 15, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}
	_ = store.Close()

	reopened, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	loaded, err := reopened.LoadSchedulePlan(context.Background(), "schedule-reopen-2")
	if err != nil {
		t.Fatalf("load schedule plan after reopen: %v", err)
	}
	if loaded.Plan.ID != "schedule-reopen-2" || loaded.Plan.Kind != ScheduleKindDelay || loaded.Plan.Delay != 30*time.Second {
		t.Fatalf("expected reopened schedule plan to round-trip, got %+v", loaded)
	}
	if loaded.DueAtEvidence != "" {
		t.Fatalf("expected legacy-style schedule without explicit evidence to reopen cleanly, got %+v", loaded)
	}
}

func TestSQLiteStateStoreRetainsScheduleClaimsAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	dueAt := time.Date(2026, 4, 7, 15, 0, 30, 0, time.UTC)
	claimedAt := dueAt.Add(5 * time.Second)
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "schedule-reopen-claimed",
			Kind:      ScheduleKindDelay,
			Delay:     30 * time.Second,
			Source:    "runtime-demo-scheduler",
			EventType: "message.received",
		},
		DueAt:         &dueAt,
		DueAtEvidence: scheduleDueAtEvidenceRecoveredClaim,
		ClaimOwner:    "runtime-local:test-scheduler",
		ClaimedAt:     &claimedAt,
		CreatedAt:     time.Date(2026, 4, 7, 15, 0, 0, 0, time.UTC),
		UpdatedAt:     claimedAt,
	}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}
	_ = store.Close()

	reopened, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	loaded, err := reopened.LoadSchedulePlan(context.Background(), "schedule-reopen-claimed")
	if err != nil {
		t.Fatalf("load schedule plan after reopen: %v", err)
	}
	if loaded.ClaimOwner != "runtime-local:test-scheduler" || loaded.ClaimedAt == nil || !loaded.ClaimedAt.Equal(claimedAt) {
		t.Fatalf("expected reopened schedule claim to round-trip, got %+v", loaded)
	}
	if loaded.DueAtEvidence != scheduleDueAtEvidenceRecoveredClaim {
		t.Fatalf("expected reopened claimed schedule dueAt evidence to round-trip, got %+v", loaded)
	}
}

func TestSQLiteStateStoreClaimSchedulePlanIsAtomicAndExclusive(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	createdAt := time.Date(2026, 4, 9, 9, 0, 0, 0, time.UTC)
	dueAt := createdAt.Add(30 * time.Second)
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "schedule-claim-exclusive",
			Kind:      ScheduleKindDelay,
			Delay:     30 * time.Second,
			Source:    "runtime-demo-scheduler",
			EventType: "message.received",
		},
		DueAt:         &dueAt,
		DueAtEvidence: scheduleDueAtEvidencePersisted,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}

	firstClaimedAt := dueAt.Add(5 * time.Second)
	firstClaimed, err := store.ClaimSchedulePlan(context.Background(), "schedule-claim-exclusive", dueAt, schedulePlanClaim{
		ClaimOwner: "runtime-local:first",
		ClaimedAt:  firstClaimedAt,
		UpdatedAt:  firstClaimedAt,
	})
	if err != nil {
		t.Fatalf("first atomic claim: %v", err)
	}
	if !firstClaimed {
		t.Fatal("expected first atomic claim to succeed")
	}

	secondClaimedAt := firstClaimedAt.Add(5 * time.Second)
	secondClaimed, err := store.ClaimSchedulePlan(context.Background(), "schedule-claim-exclusive", dueAt, schedulePlanClaim{
		ClaimOwner: "runtime-local:second",
		ClaimedAt:  secondClaimedAt,
		UpdatedAt:  secondClaimedAt,
	})
	if err != nil {
		t.Fatalf("second atomic claim: %v", err)
	}
	if secondClaimed {
		t.Fatal("expected second atomic claim to fail once row is already claimed")
	}

	loaded, err := store.LoadSchedulePlan(context.Background(), "schedule-claim-exclusive")
	if err != nil {
		t.Fatalf("load claimed schedule plan: %v", err)
	}
	if loaded.ClaimOwner != "runtime-local:first" || loaded.ClaimedAt == nil || !loaded.ClaimedAt.Equal(firstClaimedAt) {
		t.Fatalf("expected first claim ownership to remain persisted exclusively, got %+v", loaded)
	}
	if loaded.DueAt == nil || !loaded.DueAt.Equal(dueAt) {
		t.Fatalf("expected dueAt to remain unchanged after claim attempts, got %+v", loaded)
	}
}

func TestSQLiteStateStorePersistsJobsRoundTrip(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	startedAt := time.Date(2026, 4, 7, 10, 0, 2, 0, time.UTC)
	createdAt := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	nextRunAt := time.Date(2026, 4, 7, 10, 0, 5, 0, time.UTC)
	job := Job{
		ID:              "job-roundtrip-1",
		Type:            "ai.chat",
		TraceID:         "trace-roundtrip-1",
		EventID:         "evt-roundtrip-1",
		RunID:           "run-roundtrip-1",
		Status:          JobStatusRetrying,
		Payload:         map[string]any{"prompt": "hello", "attempt": float64(1)},
		RetryCount:      1,
		MaxRetries:      3,
		Timeout:         45 * time.Second,
		LastError:       "mock upstream failure",
		ReasonCode:      JobReasonCodeExecutionRetry,
		CreatedAt:       createdAt,
		StartedAt:       &startedAt,
		NextRunAt:       &nextRunAt,
		WorkerID:        "runtime-local:test-worker",
		LeaseAcquiredAt: &startedAt,
		LeaseExpiresAt:  &nextRunAt,
		HeartbeatAt:     &startedAt,
		DeadLetter:      false,
		Correlation:     "corr-roundtrip-1",
	}

	if err := store.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	loaded, err := store.LoadJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if loaded.ID != job.ID || loaded.Type != job.Type || loaded.Status != job.Status {
		t.Fatalf("expected loaded job identity/status to match, got %+v", loaded)
	}
	if loaded.RetryCount != job.RetryCount || loaded.MaxRetries != job.MaxRetries || loaded.Timeout != job.Timeout {
		t.Fatalf("expected retry/timeout fields to round-trip, got %+v", loaded)
	}
	if loaded.TraceID != job.TraceID || loaded.EventID != job.EventID || loaded.RunID != job.RunID || loaded.Correlation != job.Correlation {
		t.Fatalf("expected trace fields to round-trip, got %+v", loaded)
	}
	if loaded.LastError != job.LastError || loaded.DeadLetter != job.DeadLetter || loaded.ReasonCode != job.ReasonCode {
		t.Fatalf("expected error/dead-letter fields to round-trip, got %+v", loaded)
	}
	if loaded.WorkerID != job.WorkerID {
		t.Fatalf("expected worker id to round-trip, got %+v", loaded)
	}
	if !loaded.CreatedAt.Equal(createdAt) || loaded.StartedAt == nil || !loaded.StartedAt.Equal(startedAt) || loaded.NextRunAt == nil || !loaded.NextRunAt.Equal(nextRunAt) {
		t.Fatalf("expected timestamps to round-trip, got %+v", loaded)
	}
	if loaded.LeaseAcquiredAt == nil || !loaded.LeaseAcquiredAt.Equal(startedAt) || loaded.LeaseExpiresAt == nil || !loaded.LeaseExpiresAt.Equal(nextRunAt) || loaded.HeartbeatAt == nil || !loaded.HeartbeatAt.Equal(startedAt) {
		t.Fatalf("expected lease timestamps to round-trip, got %+v", loaded)
	}
	if loaded.Payload["prompt"] != "hello" {
		t.Fatalf("expected payload to round-trip, got %+v", loaded.Payload)
	}

	jobs, err := store.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("expected one listed job, got %+v", jobs)
	}
}

func TestSQLiteStateStoreRetainsJobsAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job := NewJob("job-reopen-2", "demo.echo", 0, 10*time.Second)
	job.TraceID = "trace-reopen-2"
	job.EventID = "evt-reopen-2"
	job.Correlation = "corr-reopen-2"
	if err := store.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	_ = store.Close()

	reopened, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	loaded, err := reopened.LoadJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("load job after reopen: %v", err)
	}
	if loaded.ID != job.ID || loaded.Status != JobStatusPending || loaded.TraceID != job.TraceID {
		t.Fatalf("expected job after reopen, got %+v", loaded)
	}
}

func TestSQLiteStateStorePersistsDeadLetterAlertsAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	finishedAt := time.Date(2026, 4, 7, 10, 15, 0, 0, time.UTC)
	job := NewJob("job-alert-reopen", "demo.echo", 0, 10*time.Second)
	job.Status = JobStatusDead
	job.LastError = "timeout"
	job.DeadLetter = true
	job.TraceID = "trace-alert-reopen"
	job.EventID = "evt-alert-reopen"
	job.RunID = "run-alert-reopen"
	job.Correlation = "corr-alert-reopen"
	job.FinishedAt = &finishedAt
	if err := store.RecordAlert(context.Background(), jobDeadLetterAlert(job)); err != nil {
		t.Fatalf("record alert: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenSQLiteStateStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	alerts, err := reopened.ListAlerts(context.Background())
	if err != nil {
		t.Fatalf("list alerts after reopen: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected one alert after reopen, got %+v", alerts)
	}
	alert := alerts[0]
	if alert.ObjectID != job.ID || alert.FailureType != alertFailureTypeJobDeadLetter || alert.LatestReason != job.LastError {
		t.Fatalf("expected persisted alert payload after reopen, got %+v", alert)
	}
	if !alert.FirstOccurredAt.Equal(finishedAt) || !alert.LatestOccurredAt.Equal(finishedAt) {
		t.Fatalf("expected persisted alert timestamps after reopen, got %+v", alert)
	}
	counts, err := reopened.Counts(context.Background())
	if err != nil {
		t.Fatalf("counts after alert reopen: %v", err)
	}
	if counts["alerts"] != 1 {
		t.Fatalf("expected one persisted alert row, got %+v", counts)
	}
}

func TestSQLiteStateStoreReturnsNoRowsForMissingPluginStatusSnapshot(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	if _, err := store.LoadPluginStatusSnapshot(context.Background(), "plugin-missing"); err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows for missing plugin snapshot, got %v", err)
	}
}

func TestSQLiteStateStoreIdempotencyLookup(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	if err := store.SaveIdempotencyKey(ctx, "onebot:msg:dup", "evt-dup"); err != nil {
		t.Fatalf("save idempotency key: %v", err)
	}

	found, err := store.HasIdempotencyKey(ctx, "onebot:msg:dup")
	if err != nil {
		t.Fatalf("has idempotency key: %v", err)
	}
	if !found {
		t.Fatal("expected idempotency key to be found")
	}
}

func TestSQLiteStateStoreLoadsStoredEventPayload(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	original := eventmodel.Event{
		EventID:        "evt-load-1",
		TraceID:        "trace-load-1",
		Source:         "webhook",
		Type:           "webhook.received",
		Timestamp:      time.Date(2026, 4, 3, 22, 0, 0, 0, time.UTC),
		IdempotencyKey: "webhook:load:1",
		Metadata:       map[string]any{"sample": "yes"},
	}
	if err := store.RecordEvent(context.Background(), original); err != nil {
		t.Fatalf("record event: %v", err)
	}

	loaded, err := store.LoadEvent(context.Background(), original.EventID)
	if err != nil {
		t.Fatalf("load event: %v", err)
	}
	if loaded.EventID != original.EventID || loaded.TraceID != original.TraceID || loaded.IdempotencyKey != original.IdempotencyKey {
		t.Fatalf("expected loaded event to match original identity, got %+v", loaded)
	}
	if loaded.Metadata["sample"] != "yes" {
		t.Fatalf("expected metadata to round-trip, got %+v", loaded.Metadata)
	}
}

func openTempSQLiteStore(t *testing.T) *SQLiteStateStore {
	t.Helper()

	store, err := OpenSQLiteStateStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	return store
}
