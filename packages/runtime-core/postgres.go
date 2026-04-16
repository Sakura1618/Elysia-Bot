package runtimecore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

const PostgresSchemaV0 = `
CREATE TABLE IF NOT EXISTS event_log (
  event_id TEXT PRIMARY KEY,
  trace_id TEXT NOT NULL,
  source TEXT NOT NULL,
  type TEXT NOT NULL,
  payload_json JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS job_state (
  job_id TEXT PRIMARY KEY,
  job_type TEXT NOT NULL,
  status TEXT NOT NULL,
  payload_json JSONB NOT NULL,
  retry_count INT NOT NULL,
  max_retries INT NOT NULL,
  timeout_seconds INT NOT NULL,
  last_error TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  started_at TIMESTAMPTZ NULL,
  finished_at TIMESTAMPTZ NULL,
  next_run_at TIMESTAMPTZ NULL,
  dead_letter BOOLEAN NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_state (
  workflow_id TEXT PRIMARY KEY,
  current_index INT NOT NULL,
  waiting_for TEXT NOT NULL,
  sleeping_until TIMESTAMPTZ NULL,
  completed BOOLEAN NOT NULL,
  compensated BOOLEAN NOT NULL,
  state_json JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS plugin_registry_pg (
  plugin_id TEXT PRIMARY KEY,
  version TEXT NOT NULL,
  api_version TEXT NOT NULL,
  mode TEXT NOT NULL,
  manifest_json JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS idempotency_keys_pg (
  idempotency_key TEXT PRIMARY KEY,
  event_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL,
  allowed BOOLEAN NOT NULL,
  reason TEXT NULL,
  occurred_at TIMESTAMPTZ NOT NULL
);
`

type PostgresStore struct {
	pool postgresPool
}

type rowScanner interface {
	Scan(dest ...any) error
}

type postgresPool interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
	Close()
}

var _ EventJournalReader = (*PostgresStore)(nil)

func OpenPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	store := &PostgresStore{pool: pool}
	if err := store.Init(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (s *PostgresStore) Close() {
	s.pool.Close()
}

func (s *PostgresStore) Init(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, PostgresSchemaV0)
	if err != nil {
		return fmt.Errorf("init postgres schema: %w", err)
	}
	return nil
}

func WritePostgresMigration(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(PostgresSchemaV0), 0o644)
}

func (s *PostgresStore) SaveEvent(ctx context.Context, event eventmodel.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO event_log (event_id, trace_id, source, type, payload_json, created_at) VALUES ($1,$2,$3,$4,$5,$6)`, event.EventID, event.TraceID, event.Source, event.Type, payload, time.Now().UTC())
	return err
}

func (s *PostgresStore) SaveJob(ctx context.Context, job Job) error {
	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO job_state (job_id, job_type, status, payload_json, retry_count, max_retries, timeout_seconds, last_error, created_at, started_at, finished_at, next_run_at, dead_letter) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (job_id) DO UPDATE SET status=excluded.status, payload_json=excluded.payload_json, retry_count=excluded.retry_count, max_retries=excluded.max_retries, timeout_seconds=excluded.timeout_seconds, last_error=excluded.last_error, started_at=excluded.started_at, finished_at=excluded.finished_at, next_run_at=excluded.next_run_at, dead_letter=excluded.dead_letter`, job.ID, job.Type, job.Status, payload, job.RetryCount, job.MaxRetries, int(job.Timeout.Seconds()), job.LastError, job.CreatedAt, job.StartedAt, job.FinishedAt, job.NextRunAt, job.DeadLetter)
	return err
}

func (s *PostgresStore) SaveWorkflow(ctx context.Context, workflow Workflow) error {
	payload, err := json.Marshal(workflow.State)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO workflow_state (workflow_id, current_index, waiting_for, sleeping_until, completed, compensated, state_json, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (workflow_id) DO UPDATE SET current_index=excluded.current_index, waiting_for=excluded.waiting_for, sleeping_until=excluded.sleeping_until, completed=excluded.completed, compensated=excluded.compensated, state_json=excluded.state_json, updated_at=excluded.updated_at`, workflow.ID, workflow.CurrentIndex, workflow.WaitingFor, workflow.SleepingUntil, workflow.Completed, workflow.Compensated, payload, time.Now().UTC())
	return err
}

func (s *PostgresStore) SavePluginManifest(ctx context.Context, manifest pluginsdk.PluginManifest) error {
	payload, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO plugin_registry_pg (plugin_id, version, api_version, mode, manifest_json, updated_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (plugin_id) DO UPDATE SET version=excluded.version, api_version=excluded.api_version, mode=excluded.mode, manifest_json=excluded.manifest_json, updated_at=excluded.updated_at`, manifest.ID, manifest.Version, manifest.APIVersion, manifest.Mode, payload, time.Now().UTC())
	return err
}

func (s *PostgresStore) SaveIdempotencyKey(ctx context.Context, key, eventID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO idempotency_keys_pg (idempotency_key, event_id, created_at) VALUES ($1,$2,$3) ON CONFLICT (idempotency_key) DO NOTHING`, key, eventID, time.Now().UTC())
	return err
}

func (s *PostgresStore) SaveAudit(ctx context.Context, entry pluginsdk.AuditEntry) error {
	occurredAt, err := time.Parse(time.RFC3339, entry.OccurredAt)
	if err != nil {
		return fmt.Errorf("parse audit occurred_at: %w", err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO audit_log (actor, action, target, allowed, reason, occurred_at) VALUES ($1,$2,$3,$4,$5,$6)`, entry.Actor, entry.Action, entry.Target, entry.Allowed, auditEntryReason(entry), occurredAt)
	return err
}

func (s *PostgresStore) LoadEvent(ctx context.Context, eventID string) (eventmodel.Event, error) {
	row := s.pool.QueryRow(ctx, `SELECT payload_json FROM event_log WHERE event_id = $1`, eventID)
	return loadPostgresEvent(row)
}

func (s *PostgresStore) LoadPluginManifest(ctx context.Context, pluginID string) (pluginsdk.PluginManifest, error) {
	row := s.pool.QueryRow(ctx, `SELECT manifest_json FROM plugin_registry_pg WHERE plugin_id = $1`, pluginID)
	return decodePostgresManifest(row)
}

func (s *PostgresStore) FindIdempotencyKey(ctx context.Context, key string) (string, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT event_id FROM idempotency_keys_pg WHERE idempotency_key = $1`, key)
	return decodePostgresIdempotencyLookup(row)
}

func (s *PostgresStore) Counts(ctx context.Context) (map[string]int, error) {
	tables := map[string]string{
		"event_journal":    `SELECT COUNT(*) FROM event_log`,
		"idempotency_keys": `SELECT COUNT(*) FROM idempotency_keys_pg`,
	}
	counts := make(map[string]int, len(tables))
	for name, query := range tables {
		var count int
		if err := s.pool.QueryRow(ctx, query).Scan(&count); err != nil {
			return nil, fmt.Errorf("count %s: %w", name, err)
		}
		counts[name] = count
	}
	return counts, nil
}

func decodePostgresIdempotencyLookup(row rowScanner) (string, bool, error) {
	var eventID string
	err := row.Scan(&eventID)
	if err != nil {
		if isPostgresNoRows(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return eventID, true, nil
}

func decodePostgresEvent(row rowScanner) (eventmodel.Event, error) {
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		return eventmodel.Event{}, err
	}
	var event eventmodel.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return eventmodel.Event{}, fmt.Errorf("decode postgres event payload: %w", err)
	}
	return event, nil
}

func decodePostgresManifest(row rowScanner) (pluginsdk.PluginManifest, error) {
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		return pluginsdk.PluginManifest{}, err
	}
	var manifest pluginsdk.PluginManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return pluginsdk.PluginManifest{}, fmt.Errorf("decode postgres manifest payload: %w", err)
	}
	return manifest, nil
}

func loadPostgresEvent(row rowScanner) (eventmodel.Event, error) {
	event, err := decodePostgresEvent(row)
	if err != nil {
		return eventmodel.Event{}, fmt.Errorf("load event journal: %w", err)
	}
	return event, nil
}

func isPostgresNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
