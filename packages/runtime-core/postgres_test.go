package runtimecore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

const postgresTestDSNEnv = "BOT_PLATFORM_POSTGRES_TEST_DSN"

type fakeRow struct {
	values []any
	err    error
}

type fakePostgresPool struct {
	execSQL      []string
	execArgs     [][]any
	execErr      error
	querySQL     []string
	queryArgs    [][]any
	queryRow     pgx.Row
	closed       bool
	commandTag   pgconn.CommandTag
	queryRowFunc func(ctx context.Context, sql string, arguments ...any) pgx.Row
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, value := range r.values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func (p *fakePostgresPool) Exec(_ context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	p.execSQL = append(p.execSQL, sql)
	p.execArgs = append(p.execArgs, append([]any(nil), arguments...))
	return p.commandTag, p.execErr
}

func (p *fakePostgresPool) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	p.querySQL = append(p.querySQL, sql)
	p.queryArgs = append(p.queryArgs, append([]any(nil), arguments...))
	if p.queryRowFunc != nil {
		return p.queryRowFunc(ctx, sql, arguments...)
	}
	return p.queryRow
}

func (p *fakePostgresPool) Close() {
	p.closed = true
}

func TestWritePostgresMigrationCreatesSchemaFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "postgres", "001_init.sql")
	if err := WritePostgresMigration(path); err != nil {
		t.Fatalf("write postgres migration: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read postgres migration: %v", err)
	}
	content := string(raw)
	for _, expected := range []string{"event_log", "job_state", "workflow_state", "plugin_registry_pg", "idempotency_keys_pg", "audit_log"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("expected migration to contain %q, got %s", expected, content)
		}
	}
}

func TestPostgresStoreSaveMethodsIssueExpectedWrites(t *testing.T) {
	t.Parallel()

	pool := &fakePostgresPool{}
	store := &PostgresStore{pool: pool}
	ctx := context.Background()
	event := eventmodel.Event{EventID: "evt-pg", TraceID: "trace-pg", Source: "webhook", Type: "webhook.received", Timestamp: time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC), IdempotencyKey: "webhook:pg:1"}
	job := Job{ID: "job-pg", Type: "ai.chat", Status: JobStatusPending, Payload: map[string]any{"prompt": "hi"}, MaxRetries: 3, Timeout: 30 * time.Second, CreatedAt: time.Date(2026, 4, 6, 10, 1, 0, 0, time.UTC)}
	workflow := Workflow{ID: "wf-pg", CurrentIndex: 1, WaitingFor: "message.received", Completed: false, Compensated: false, State: map[string]any{"step": "wait"}}
	manifest := pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess}
	audit := pluginsdk.AuditEntry{Actor: "admin-user", Action: "enable", Target: "plugin-echo", Allowed: true, OccurredAt: "2026-04-06T10:02:00Z"}
	setAuditEntryReason(&audit, "rollout_prepared")

	if err := store.SaveEvent(ctx, event); err != nil {
		t.Fatalf("save event: %v", err)
	}
	if err := store.SaveJob(ctx, job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	if err := store.SaveWorkflow(ctx, workflow); err != nil {
		t.Fatalf("save workflow: %v", err)
	}
	if err := store.SavePluginManifest(ctx, manifest); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	if err := store.SaveIdempotencyKey(ctx, "idem-1", event.EventID); err != nil {
		t.Fatalf("save idempotency key: %v", err)
	}
	if err := store.SaveAudit(ctx, audit); err != nil {
		t.Fatalf("save audit: %v", err)
	}

	if len(pool.execSQL) != 6 {
		t.Fatalf("expected 6 exec calls, got %d", len(pool.execSQL))
	}
	for _, expected := range []string{"INSERT INTO event_log", "INSERT INTO job_state", "INSERT INTO workflow_state", "INSERT INTO plugin_registry_pg", "INSERT INTO idempotency_keys_pg", "INSERT INTO audit_log"} {
		matched := false
		for _, sql := range pool.execSQL {
			if strings.Contains(sql, expected) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("expected exec statements to include %q, got %+v", expected, pool.execSQL)
		}
	}
	if len(pool.execArgs[0]) != 6 || pool.execArgs[0][0] != event.EventID {
		t.Fatalf("unexpected event exec args %+v", pool.execArgs[0])
	}
	if len(pool.execArgs[1]) != 13 || pool.execArgs[1][0] != job.ID || pool.execArgs[1][1] != job.Type {
		t.Fatalf("unexpected job exec args %+v", pool.execArgs[1])
	}
	if len(pool.execArgs[4]) != 3 || pool.execArgs[4][0] != "idem-1" || pool.execArgs[4][1] != event.EventID {
		t.Fatalf("unexpected idempotency exec args %+v", pool.execArgs[4])
	}
	if len(pool.execArgs[5]) != 6 || pool.execArgs[5][4] != "rollout_prepared" {
		t.Fatalf("unexpected audit exec args %+v", pool.execArgs[5])
	}
}

func TestPostgresStoreLoadMethodsIssueExpectedQueries(t *testing.T) {
	t.Parallel()

	eventPayload, _ := json.Marshal(eventmodel.Event{EventID: "evt-load", TraceID: "trace-load", Source: "webhook", Type: "webhook.received", Timestamp: time.Date(2026, 4, 6, 11, 0, 0, 0, time.UTC), IdempotencyKey: "webhook:load:1"})
	manifestPayload, _ := json.Marshal(pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess})
	pool := &fakePostgresPool{queryRowFunc: func(_ context.Context, sql string, _ ...any) pgx.Row {
		switch {
		case strings.Contains(sql, "FROM event_log"):
			return fakeRow{values: []any{eventPayload}}
		case strings.Contains(sql, "FROM plugin_registry_pg"):
			return fakeRow{values: []any{manifestPayload}}
		case strings.Contains(sql, "FROM idempotency_keys_pg"):
			return fakeRow{values: []any{"evt-load"}}
		default:
			return fakeRow{err: errors.New("unexpected query")}
		}
	}}
	store := &PostgresStore{pool: pool}
	ctx := context.Background()

	event, err := store.LoadEvent(ctx, "evt-load")
	if err != nil || event.EventID != "evt-load" {
		t.Fatalf("load event: event=%+v err=%v", event, err)
	}
	manifest, err := store.LoadPluginManifest(ctx, "plugin-echo")
	if err != nil || manifest.ID != "plugin-echo" {
		t.Fatalf("load manifest: manifest=%+v err=%v", manifest, err)
	}
	eventID, found, err := store.FindIdempotencyKey(ctx, "idem-load")
	if err != nil || !found || eventID != "evt-load" {
		t.Fatalf("find idempotency key: eventID=%q found=%v err=%v", eventID, found, err)
	}

	if len(pool.querySQL) != 3 {
		t.Fatalf("expected 3 query calls, got %d", len(pool.querySQL))
	}
	if !strings.Contains(pool.querySQL[0], "FROM event_log") || !strings.Contains(pool.querySQL[1], "FROM plugin_registry_pg") || !strings.Contains(pool.querySQL[2], "FROM idempotency_keys_pg") {
		t.Fatalf("unexpected query SQLs %+v", pool.querySQL)
	}
}

func TestPostgresStoreCountsReadsSmokeTables(t *testing.T) {
	t.Parallel()

	pool := &fakePostgresPool{queryRowFunc: func(_ context.Context, sql string, _ ...any) pgx.Row {
		switch {
		case strings.Contains(sql, "FROM event_log"):
			return fakeRow{values: []any{3}}
		case strings.Contains(sql, "FROM idempotency_keys_pg"):
			return fakeRow{values: []any{5}}
		default:
			return fakeRow{err: errors.New("unexpected query")}
		}
	}}
	store := &PostgresStore{pool: pool}
	counts, err := store.Counts(context.Background())
	if err != nil {
		t.Fatalf("count postgres smoke tables: %v", err)
	}
	if counts["event_journal"] != 3 || counts["idempotency_keys"] != 5 {
		t.Fatalf("unexpected counts %+v", counts)
	}
	if len(pool.querySQL) != 2 {
		t.Fatalf("expected 2 count queries, got %+v", pool.querySQL)
	}
}

func TestPostgresStoreInitAndCloseDelegateToPool(t *testing.T) {
	t.Parallel()

	pool := &fakePostgresPool{}
	store := &PostgresStore{pool: pool}
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	if len(pool.execSQL) != 1 || !strings.Contains(pool.execSQL[0], "CREATE TABLE IF NOT EXISTS event_log") {
		t.Fatalf("expected init to execute schema, got %+v", pool.execSQL)
	}
	store.Close()
	if !pool.closed {
		t.Fatal("expected close to delegate to pool")
	}
}

func TestPostgresStoreLiveRoundTrip(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(postgresTestDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run live Postgres smoke test", postgresTestDSNEnv)
	}

	ctx := context.Background()
	store, err := OpenPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	eventID := fmt.Sprintf("evt-live-%d", time.Now().UnixNano())
	event := eventmodel.Event{
		EventID:        eventID,
		TraceID:        fmt.Sprintf("trace-live-%d", time.Now().UnixNano()),
		Source:         "webhook",
		Type:           "webhook.received",
		Timestamp:      time.Now().UTC(),
		IdempotencyKey: fmt.Sprintf("idem-live-%s", eventID),
		Metadata:       map[string]any{"smoke": true},
	}

	if err := store.SaveEvent(ctx, event); err != nil {
		t.Fatalf("save live event: %v", err)
	}
	loaded, err := store.LoadEvent(ctx, eventID)
	if err != nil {
		t.Fatalf("load live event: %v", err)
	}
	if loaded.EventID != event.EventID || loaded.TraceID != event.TraceID || loaded.Type != event.Type {
		t.Fatalf("unexpected loaded event %+v", loaded)
	}

	if err := store.SaveIdempotencyKey(ctx, event.IdempotencyKey, event.EventID); err != nil {
		t.Fatalf("save live idempotency key: %v", err)
	}
	lookupEventID, found, err := store.FindIdempotencyKey(ctx, event.IdempotencyKey)
	if err != nil {
		t.Fatalf("find live idempotency key: %v", err)
	}
	if !found || lookupEventID != event.EventID {
		t.Fatalf("unexpected live idempotency lookup eventID=%q found=%v", lookupEventID, found)
	}

	manifest := pluginsdk.PluginManifest{
		ID:         fmt.Sprintf("plugin-live-%d", time.Now().UnixNano()),
		Name:       "Live Smoke Plugin",
		Version:    "0.1.0",
		APIVersion: "v0",
		Mode:       pluginsdk.ModeSubprocess,
		Permissions: []string{
			"message:read",
		},
	}
	if err := store.SavePluginManifest(ctx, manifest); err != nil {
		t.Fatalf("save live manifest: %v", err)
	}
	loadedManifest, err := store.LoadPluginManifest(ctx, manifest.ID)
	if err != nil {
		t.Fatalf("load live manifest: %v", err)
	}
	if loadedManifest.ID != manifest.ID || loadedManifest.Version != manifest.Version || loadedManifest.Mode != manifest.Mode {
		t.Fatalf("unexpected live manifest %+v", loadedManifest)
	}
}

func TestDecodePostgresEventAndManifestPayloads(t *testing.T) {
	t.Parallel()

	eventPayload, _ := json.Marshal(eventmodel.Event{EventID: "evt-pg", TraceID: "trace-pg", Source: "webhook", Type: "webhook.received", Timestamp: time.Date(2026, 4, 4, 15, 0, 0, 0, time.UTC), IdempotencyKey: "webhook:pg:1"})
	event, err := decodePostgresEvent(fakeRow{values: []any{eventPayload}})
	if err != nil || event.EventID != "evt-pg" || event.TraceID != "trace-pg" {
		t.Fatalf("expected postgres event payload decode to succeed, got event=%+v err=%v", event, err)
	}

	manifestPayload, _ := json.Marshal(pluginsdk.PluginManifest{ID: "plugin-echo", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess})
	manifest, err := decodePostgresManifest(fakeRow{values: []any{manifestPayload}})
	if err != nil || manifest.ID != "plugin-echo" || manifest.APIVersion != "v0" {
		t.Fatalf("expected postgres manifest payload decode to succeed, got manifest=%+v err=%v", manifest, err)
	}
}

func TestDecodePostgresIdempotencyLookupHandlesHitAndMiss(t *testing.T) {
	t.Parallel()

	eventID, found, err := decodePostgresIdempotencyLookup(fakeRow{values: []any{"evt-pg-hit"}})
	if err != nil || !found || eventID != "evt-pg-hit" {
		t.Fatalf("expected idempotency hit, got eventID=%q found=%v err=%v", eventID, found, err)
	}

	eventID, found, err = decodePostgresIdempotencyLookup(fakeRow{err: pgx.ErrNoRows})
	if err != nil || found || eventID != "" {
		t.Fatalf("expected idempotency miss, got eventID=%q found=%v err=%v", eventID, found, err)
	}
}

func TestDecodePostgresIdempotencyLookupDoesNotTreatArbitraryErrorAsMiss(t *testing.T) {
	t.Parallel()

	rowErr := errors.New("transport failed: no rows from upstream")
	_, found, err := decodePostgresIdempotencyLookup(fakeRow{err: rowErr})
	if err == nil || found || !errors.Is(err, rowErr) {
		t.Fatalf("expected arbitrary error to propagate, got found=%v err=%v", found, err)
	}
}

func TestDecodePostgresEventAndManifestPropagateNoRows(t *testing.T) {
	t.Parallel()

	if _, err := decodePostgresEvent(fakeRow{err: pgx.ErrNoRows}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected event no-row error to propagate, got %v", err)
	}
	if _, err := decodePostgresManifest(fakeRow{err: pgx.ErrNoRows}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected manifest no-row error to propagate, got %v", err)
	}
}

func TestPostgresLoadEventWrapsReplayJournalReadErrors(t *testing.T) {
	t.Parallel()

	_, err := loadPostgresEvent(fakeRow{err: pgx.ErrNoRows})
	if err == nil || !strings.Contains(err.Error(), "load event journal") || !strings.Contains(err.Error(), pgx.ErrNoRows.Error()) {
		t.Fatalf("expected replay-facing load error wrapping, got %v", err)
	}
}

func TestDecodePostgresHelpersPropagateUnexpectedErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	if _, err := decodePostgresEvent(fakeRow{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("expected event decode to propagate error, got %v", err)
	}
	if _, err := decodePostgresManifest(fakeRow{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("expected manifest decode to propagate error, got %v", err)
	}
	if _, _, err := decodePostgresIdempotencyLookup(fakeRow{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("expected idempotency decode to propagate error, got %v", err)
	}
}
