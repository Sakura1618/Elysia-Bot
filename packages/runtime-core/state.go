package runtimecore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	_ "modernc.org/sqlite"
)

const SQLiteSchemaV0 = `
CREATE TABLE IF NOT EXISTS event_journal (
  event_id TEXT PRIMARY KEY,
  trace_id TEXT NOT NULL,
  source TEXT NOT NULL,
  type TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plugin_registry (
  plugin_id TEXT PRIMARY KEY,
  version TEXT NOT NULL,
  api_version TEXT NOT NULL,
  mode TEXT NOT NULL,
  manifest_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plugin_enabled_overlays (
  plugin_id TEXT PRIMARY KEY,
  enabled INTEGER NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plugin_configs (
  plugin_id TEXT PRIMARY KEY,
  config_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plugin_status_snapshots (
  plugin_id TEXT PRIMARY KEY,
  last_dispatch_kind TEXT NOT NULL,
  last_dispatch_success INTEGER NOT NULL,
  last_dispatch_error TEXT NOT NULL,
  last_dispatch_at TEXT NOT NULL,
  last_recovered_at TEXT,
  last_recovery_failure_count INTEGER NOT NULL,
  current_failure_streak INTEGER NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS adapter_instances (
  instance_id TEXT PRIMARY KEY,
  adapter TEXT NOT NULL,
  source TEXT NOT NULL,
  config_json TEXT NOT NULL,
  status TEXT NOT NULL,
  health TEXT NOT NULL,
  online INTEGER NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  session_id TEXT PRIMARY KEY,
  plugin_id TEXT NOT NULL,
  state_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  idempotency_key TEXT PRIMARY KEY,
  event_id TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS jobs (
  job_id TEXT PRIMARY KEY,
  job_type TEXT NOT NULL,
  status TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  retry_count INTEGER NOT NULL,
  max_retries INTEGER NOT NULL,
  timeout_ms INTEGER NOT NULL,
  last_error TEXT NOT NULL,
  reason_code TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  next_run_at TEXT,
  worker_id TEXT NOT NULL DEFAULT '',
  lease_acquired_at TEXT,
  lease_expires_at TEXT,
  heartbeat_at TEXT,
  dead_letter INTEGER NOT NULL,
  trace_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  correlation TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS alerts (
  alert_id TEXT PRIMARY KEY,
  object_type TEXT NOT NULL,
  object_id TEXT NOT NULL,
  failure_type TEXT NOT NULL,
  first_occurred_at TEXT NOT NULL,
  latest_occurred_at TEXT NOT NULL,
  latest_reason TEXT NOT NULL,
  trace_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  correlation TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS schedule_plans (
  schedule_id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  cron_expr TEXT NOT NULL,
  delay_ms INTEGER NOT NULL,
  execute_at TEXT,
  due_at TEXT,
  due_at_evidence TEXT NOT NULL DEFAULT '',
  claim_owner TEXT NOT NULL DEFAULT '',
  claimed_at TEXT,
  source TEXT NOT NULL,
  event_type TEXT NOT NULL,
  metadata_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS operator_identities (
  actor_id TEXT PRIMARY KEY,
  roles_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rbac_snapshots (
  snapshot_key TEXT PRIMARY KEY,
  console_read_permission TEXT NOT NULL,
  policies_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS replay_operation_records (
  replay_id TEXT PRIMARY KEY,
  source_event_id TEXT NOT NULL,
  replay_event_id TEXT NOT NULL,
  status TEXT NOT NULL,
  reason TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rollout_operation_records (
  operation_id TEXT PRIMARY KEY,
  plugin_id TEXT NOT NULL,
  action TEXT NOT NULL,
  current_version TEXT NOT NULL,
  candidate_version TEXT NOT NULL,
  status TEXT NOT NULL,
  reason TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`

const CurrentRBACSnapshotKey = "current"

const sqliteWorkflowInstancesSchema = `
CREATE TABLE IF NOT EXISTS workflow_instances (
  workflow_id TEXT PRIMARY KEY,
  plugin_id TEXT NOT NULL,
  status TEXT NOT NULL,
  workflow_json TEXT NOT NULL,
  last_event_id TEXT NOT NULL,
  last_event_type TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`

type SQLiteStateStore struct {
	db *sql.DB
}

func (s *SQLiteStateStore) DBForTests() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

type SessionState = pluginsdk.SessionState

type PluginStatusSnapshot struct {
	PluginID                 string
	LastDispatchKind         string
	LastDispatchSuccess      bool
	LastDispatchError        string
	LastDispatchAt           time.Time
	LastRecoveredAt          *time.Time
	LastRecoveryFailureCount int
	CurrentFailureStreak     int
	UpdatedAt                time.Time
}

type PluginEnabledState struct {
	PluginID  string
	Enabled   bool
	UpdatedAt time.Time
}

type PluginConfigState struct {
	PluginID  string
	RawConfig json.RawMessage
	UpdatedAt time.Time
}

type AdapterInstanceState struct {
	InstanceID string
	Adapter    string
	Source     string
	RawConfig  json.RawMessage
	Status     string
	Health     string
	Online     bool
	UpdatedAt  time.Time
}

type OperatorIdentityState struct {
	ActorID   string
	Roles     []string
	UpdatedAt time.Time
}

type RBACSnapshotState struct {
	SnapshotKey           string
	ConsoleReadPermission string
	Policies              map[string]pluginsdk.AuthorizationPolicy
	UpdatedAt             time.Time
}

type ReplayOperationRecord struct {
	ReplayID      string
	SourceEventID string
	ReplayEventID string
	Status        string
	Reason        string
	OccurredAt    time.Time
	UpdatedAt     time.Time
}

type RolloutOperationRecord struct {
	OperationID      string
	PluginID         string
	Action           string
	CurrentVersion   string
	CandidateVersion string
	Status           string
	Reason           string
	OccurredAt       time.Time
	UpdatedAt        time.Time
}

type storedSchedulePlan struct {
	Plan          SchedulePlan
	DueAt         *time.Time
	DueAtEvidence string
	ClaimOwner    string
	ClaimedAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type schedulePlanClaim struct {
	ClaimOwner string
	ClaimedAt  time.Time
	UpdatedAt  time.Time
}

func OpenSQLiteStateStore(path string) (*SQLiteStateStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &SQLiteStateStore{db: db}
	if err := store.Init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *SQLiteStateStore) Init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000;`); err != nil {
		return fmt.Errorf("set sqlite busy timeout: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, SQLiteSchemaV0); err != nil {
		return fmt.Errorf("init sqlite schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE schedule_plans ADD COLUMN due_at_evidence TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("add schedule due_at_evidence column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE schedule_plans ADD COLUMN claim_owner TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("add schedule claim_owner column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE schedule_plans ADD COLUMN claimed_at TEXT`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("add schedule claimed_at column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE jobs ADD COLUMN reason_code TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("add jobs reason_code column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE jobs ADD COLUMN worker_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("add jobs worker_id column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE jobs ADD COLUMN lease_acquired_at TEXT`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("add jobs lease_acquired_at column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE jobs ADD COLUMN lease_expires_at TEXT`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("add jobs lease_expires_at column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE jobs ADD COLUMN heartbeat_at TEXT`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("add jobs heartbeat_at column: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStateStore) SaveWorkflowInstance(ctx context.Context, instance WorkflowInstanceState) error {
	if s == nil {
		return fmt.Errorf(`save workflow instance: sqlite state store is required`)
	}
	if err := s.ensureWorkflowInstanceTable(ctx); err != nil {
		return err
	}
	instance.WorkflowID = strings.TrimSpace(instance.WorkflowID)
	instance.PluginID = strings.TrimSpace(instance.PluginID)
	if instance.WorkflowID == `` {
		return fmt.Errorf(`save workflow instance: workflow id is required`)
	}
	if instance.PluginID == `` {
		return fmt.Errorf(`save workflow instance: plugin id is required`)
	}
	instance.Workflow.ID = instance.WorkflowID
	if strings.TrimSpace(string(instance.Status)) == `` {
		instance.Status = workflowRuntimeStatus(instance.Workflow)
	}
	createdAt := instance.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := instance.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	payload, err := json.Marshal(cloneWorkflow(instance.Workflow))
	if err != nil {
		return fmt.Errorf(`marshal workflow instance: %w`, err)
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO workflow_instances (
  workflow_id, plugin_id, status, workflow_json, last_event_id, last_event_type, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(workflow_id) DO UPDATE SET
  status=excluded.status,
  workflow_json=excluded.workflow_json,
  last_event_id=excluded.last_event_id,
  last_event_type=excluded.last_event_type,
  created_at=excluded.created_at,
  updated_at=excluded.updated_at
WHERE workflow_instances.plugin_id = excluded.plugin_id
`, instance.WorkflowID, instance.PluginID, string(instance.Status), string(payload), strings.TrimSpace(instance.LastEventID), strings.TrimSpace(instance.LastEventType), formatSQLiteTimestamp(createdAt), formatSQLiteTimestamp(updatedAt))
	if err != nil {
		return fmt.Errorf(`upsert workflow instance: %w`, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf(`save workflow instance affected rows: %w`, err)
	}
	if affected == 0 {
		stored, loadErr := s.LoadWorkflowInstance(ctx, instance.WorkflowID)
		if loadErr == nil {
			stored.PluginID = strings.TrimSpace(stored.PluginID)
			if stored.PluginID != `` && stored.PluginID != instance.PluginID {
				return fmt.Errorf(`save workflow instance: workflow %q is owned by plugin %q, not %q`, instance.WorkflowID, stored.PluginID, instance.PluginID)
			}
		}
		return fmt.Errorf(`save workflow instance: workflow %q was not persisted`, instance.WorkflowID)
	}
	return nil
}

func (s *SQLiteStateStore) LoadWorkflowInstance(ctx context.Context, workflowID string) (WorkflowInstanceState, error) {
	if s == nil {
		return WorkflowInstanceState{}, fmt.Errorf(`load workflow instance: sqlite state store is required`)
	}
	if err := s.ensureWorkflowInstanceTable(ctx); err != nil {
		return WorkflowInstanceState{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT workflow_id, plugin_id, status, workflow_json, last_event_id, last_event_type, created_at, updated_at
FROM workflow_instances
WHERE workflow_id = ?
`, strings.TrimSpace(workflowID))
	if err != nil {
		return WorkflowInstanceState{}, fmt.Errorf(`load workflow instance: %w`, err)
	}
	defer rows.Close()
	instances, err := scanWorkflowInstances(rows)
	if err != nil {
		return WorkflowInstanceState{}, err
	}
	if len(instances) == 0 {
		return WorkflowInstanceState{}, sql.ErrNoRows
	}
	return instances[0], nil
}

func (s *SQLiteStateStore) ListWorkflowInstances(ctx context.Context) ([]WorkflowInstanceState, error) {
	if s == nil {
		return nil, nil
	}
	if err := s.ensureWorkflowInstanceTable(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT workflow_id, plugin_id, status, workflow_json, last_event_id, last_event_type, created_at, updated_at
FROM workflow_instances
ORDER BY created_at ASC, workflow_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf(`list workflow instances: %w`, err)
	}
	defer rows.Close()
	return scanWorkflowInstances(rows)
}

func (s *SQLiteStateStore) ensureWorkflowInstanceTable(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf(`ensure workflow instance table: sqlite state store is required`)
	}
	if _, err := s.db.ExecContext(ctx, sqliteWorkflowInstancesSchema); err != nil {
		return fmt.Errorf(`ensure workflow instance table: %w`, err)
	}
	return nil
}

func (s *SQLiteStateStore) RecordEvent(ctx context.Context, event eventmodel.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO event_journal (event_id, trace_id, source, type, payload_json, created_at)
VALUES (?, ?, ?, ?, ?, ?)
`, event.EventID, event.TraceID, event.Source, event.Type, string(payload), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert event journal: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) LoadEvent(ctx context.Context, eventID string) (eventmodel.Event, error) {
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM event_journal WHERE event_id = ?`, eventID).Scan(&payload)
	if err == sql.ErrNoRows {
		return eventmodel.Event{}, fmt.Errorf("load event journal: %w", err)
	}
	if err != nil {
		return eventmodel.Event{}, fmt.Errorf("load event journal: %w", err)
	}

	var event eventmodel.Event
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return eventmodel.Event{}, fmt.Errorf("unmarshal event payload: %w", err)
	}
	return event, nil
}

func (s *SQLiteStateStore) SavePluginManifest(ctx context.Context, manifest pluginsdk.PluginManifest) error {
	payload, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal plugin manifest: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO plugin_registry (plugin_id, version, api_version, mode, manifest_json, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(plugin_id) DO UPDATE SET
  version=excluded.version,
  api_version=excluded.api_version,
  mode=excluded.mode,
  manifest_json=excluded.manifest_json,
  updated_at=excluded.updated_at
`, manifest.ID, manifest.Version, manifest.APIVersion, manifest.Mode, string(payload), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("upsert plugin registry: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) LoadPluginManifest(ctx context.Context, pluginID string) (pluginsdk.PluginManifest, error) {
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT manifest_json FROM plugin_registry WHERE plugin_id = ?`, pluginID).Scan(&payload)
	if err == sql.ErrNoRows {
		return pluginsdk.PluginManifest{}, sql.ErrNoRows
	}
	if err != nil {
		return pluginsdk.PluginManifest{}, fmt.Errorf("load plugin manifest: %w", err)
	}
	var manifest pluginsdk.PluginManifest
	if err := json.Unmarshal([]byte(payload), &manifest); err != nil {
		return pluginsdk.PluginManifest{}, fmt.Errorf("unmarshal plugin manifest: %w", err)
	}
	return manifest, nil
}

func (s *SQLiteStateStore) SavePluginEnabledState(ctx context.Context, pluginID string, enabled bool) error {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return fmt.Errorf("save plugin enabled state: plugin id is required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO plugin_enabled_overlays (plugin_id, enabled, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(plugin_id) DO UPDATE SET
  enabled=excluded.enabled,
  updated_at=excluded.updated_at
`, pluginID, boolToSQLiteInt(enabled), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("upsert plugin enabled state: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) LoadPluginEnabledState(ctx context.Context, pluginID string) (PluginEnabledState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT plugin_id, enabled, updated_at
FROM plugin_enabled_overlays
WHERE plugin_id = ?
`, pluginID)
	if err != nil {
		return PluginEnabledState{}, fmt.Errorf("load plugin enabled state: %w", err)
	}
	defer rows.Close()
	states, err := scanPluginEnabledStates(rows)
	if err != nil {
		return PluginEnabledState{}, err
	}
	if len(states) == 0 {
		return PluginEnabledState{}, sql.ErrNoRows
	}
	return states[0], nil
}

func (s *SQLiteStateStore) ListPluginEnabledStates(ctx context.Context) ([]PluginEnabledState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT plugin_id, enabled, updated_at
FROM plugin_enabled_overlays
ORDER BY plugin_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list plugin enabled states: %w", err)
	}
	defer rows.Close()
	return scanPluginEnabledStates(rows)
}

func (s *SQLiteStateStore) SavePluginConfig(ctx context.Context, pluginID string, rawConfig json.RawMessage) error {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return fmt.Errorf("save plugin config: plugin id is required")
	}
	if len(rawConfig) == 0 {
		return fmt.Errorf("save plugin config: raw config is required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO plugin_configs (plugin_id, config_json, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(plugin_id) DO UPDATE SET
  config_json=excluded.config_json,
  updated_at=excluded.updated_at
`, pluginID, string(rawConfig), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("upsert plugin config: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) LoadPluginConfig(ctx context.Context, pluginID string) (PluginConfigState, error) {
	var (
		state        PluginConfigState
		rawConfig    string
		updatedAtRaw string
	)
	err := s.db.QueryRowContext(ctx, `
SELECT plugin_id, config_json, updated_at
FROM plugin_configs
WHERE plugin_id = ?
`, pluginID).Scan(&state.PluginID, &rawConfig, &updatedAtRaw)
	if err == sql.ErrNoRows {
		return PluginConfigState{}, sql.ErrNoRows
	}
	if err != nil {
		return PluginConfigState{}, fmt.Errorf("load plugin config: %w", err)
	}
	updatedAt, err := parseSQLiteTimestamp(updatedAtRaw)
	if err != nil {
		return PluginConfigState{}, fmt.Errorf("parse plugin config updated_at: %w", err)
	}
	state.RawConfig = append(json.RawMessage(nil), rawConfig...)
	state.UpdatedAt = updatedAt
	return state, nil
}

func (s *SQLiteStateStore) ListPluginConfigs(ctx context.Context) ([]PluginConfigState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT plugin_id, config_json, updated_at
FROM plugin_configs
ORDER BY plugin_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list plugin configs: %w", err)
	}
	defer rows.Close()
	configs := make([]PluginConfigState, 0)
	for rows.Next() {
		var (
			state        PluginConfigState
			rawConfig    string
			updatedAtRaw string
		)
		if err := rows.Scan(&state.PluginID, &rawConfig, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan plugin config: %w", err)
		}
		updatedAt, err := parseSQLiteTimestamp(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse plugin config updated_at: %w", err)
		}
		state.RawConfig = append(json.RawMessage(nil), rawConfig...)
		state.UpdatedAt = updatedAt
		configs = append(configs, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin configs: %w", err)
	}
	return configs, nil
}

func (s *SQLiteStateStore) SaveAdapterInstance(ctx context.Context, state AdapterInstanceState) error {
	state.InstanceID = strings.TrimSpace(state.InstanceID)
	if state.InstanceID == "" {
		return fmt.Errorf("save adapter instance: instance id is required")
	}
	state.Adapter = strings.TrimSpace(state.Adapter)
	if state.Adapter == "" {
		return fmt.Errorf("save adapter instance: adapter is required")
	}
	state.Source = strings.TrimSpace(state.Source)
	if state.Source == "" {
		return fmt.Errorf("save adapter instance: source is required")
	}
	if len(state.RawConfig) == 0 {
		state.RawConfig = json.RawMessage(`{}`)
	}
	var rawConfigValue any
	if err := json.Unmarshal(state.RawConfig, &rawConfigValue); err != nil {
		return fmt.Errorf("save adapter instance: unmarshal config: %w", err)
	}
	normalizedRawConfig, err := json.Marshal(rawConfigValue)
	if err != nil {
		return fmt.Errorf("save adapter instance: marshal config: %w", err)
	}
	state.Status = strings.TrimSpace(state.Status)
	if state.Status == "" {
		state.Status = "registered"
	}
	state.Health = strings.TrimSpace(state.Health)
	if state.Health == "" {
		state.Health = "unknown"
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO adapter_instances (instance_id, adapter, source, config_json, status, health, online, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(instance_id) DO UPDATE SET
  adapter=excluded.adapter,
  source=excluded.source,
  config_json=excluded.config_json,
  status=excluded.status,
  health=excluded.health,
  online=excluded.online,
  updated_at=excluded.updated_at
`, state.InstanceID, state.Adapter, state.Source, string(normalizedRawConfig), state.Status, state.Health, boolToSQLiteInt(state.Online), formatSQLiteTimestamp(state.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert adapter instance: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) LoadAdapterInstance(ctx context.Context, instanceID string) (AdapterInstanceState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT instance_id, adapter, source, config_json, status, health, online, updated_at
FROM adapter_instances
WHERE instance_id = ?
`, instanceID)
	if err != nil {
		return AdapterInstanceState{}, fmt.Errorf("load adapter instance: %w", err)
	}
	defer rows.Close()
	states, err := scanAdapterInstances(rows)
	if err != nil {
		return AdapterInstanceState{}, err
	}
	if len(states) == 0 {
		return AdapterInstanceState{}, sql.ErrNoRows
	}
	return states[0], nil
}

func (s *SQLiteStateStore) ListAdapterInstances(ctx context.Context) ([]AdapterInstanceState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT instance_id, adapter, source, config_json, status, health, online, updated_at
FROM adapter_instances
ORDER BY instance_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list adapter instances: %w", err)
	}
	defer rows.Close()
	return scanAdapterInstances(rows)
}

func (s *SQLiteStateStore) RecordDispatchResult(result DispatchResult) error {
	if s == nil {
		return nil
	}
	return s.SavePluginStatusSnapshot(context.Background(), result)
}

func (s *SQLiteStateStore) SavePluginStatusSnapshot(ctx context.Context, result DispatchResult) error {
	if strings.TrimSpace(result.PluginID) == "" {
		return fmt.Errorf("save plugin status snapshot: plugin id is required")
	}
	if result.At.IsZero() {
		result.At = time.Now().UTC()
	}
	current, err := s.LoadPluginStatusSnapshot(ctx, result.PluginID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("load plugin status snapshot: %w", err)
	}
	var previous *PluginStatusSnapshot
	if err == nil {
		previous = &current
	}
	snapshot := nextPluginStatusSnapshot(previous, result)
	_, err = s.db.ExecContext(ctx, `
INSERT INTO plugin_status_snapshots (
  plugin_id, last_dispatch_kind, last_dispatch_success, last_dispatch_error, last_dispatch_at,
  last_recovered_at, last_recovery_failure_count, current_failure_streak, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(plugin_id) DO UPDATE SET
  last_dispatch_kind=excluded.last_dispatch_kind,
  last_dispatch_success=excluded.last_dispatch_success,
  last_dispatch_error=excluded.last_dispatch_error,
  last_dispatch_at=excluded.last_dispatch_at,
  last_recovered_at=excluded.last_recovered_at,
  last_recovery_failure_count=excluded.last_recovery_failure_count,
  current_failure_streak=excluded.current_failure_streak,
  updated_at=excluded.updated_at
`, snapshot.PluginID, snapshot.LastDispatchKind, boolToSQLiteInt(snapshot.LastDispatchSuccess), snapshot.LastDispatchError, formatSQLiteTimestamp(snapshot.LastDispatchAt), nullableSQLiteTimestamp(snapshot.LastRecoveredAt), snapshot.LastRecoveryFailureCount, snapshot.CurrentFailureStreak, formatSQLiteTimestamp(snapshot.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert plugin status snapshot: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) LoadPluginStatusSnapshot(ctx context.Context, pluginID string) (PluginStatusSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT plugin_id, last_dispatch_kind, last_dispatch_success, last_dispatch_error, last_dispatch_at,
       last_recovered_at, last_recovery_failure_count, current_failure_streak, updated_at
FROM plugin_status_snapshots
WHERE plugin_id = ?
`, pluginID)
	if err != nil {
		return PluginStatusSnapshot{}, fmt.Errorf("load plugin status snapshot: %w", err)
	}
	defer rows.Close()
	snapshots, err := scanPluginStatusSnapshots(rows)
	if err != nil {
		return PluginStatusSnapshot{}, err
	}
	if len(snapshots) == 0 {
		return PluginStatusSnapshot{}, sql.ErrNoRows
	}
	return snapshots[0], nil
}

func (s *SQLiteStateStore) ListPluginStatusSnapshots(ctx context.Context) ([]PluginStatusSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT plugin_id, last_dispatch_kind, last_dispatch_success, last_dispatch_error, last_dispatch_at,
       last_recovered_at, last_recovery_failure_count, current_failure_streak, updated_at
FROM plugin_status_snapshots
ORDER BY plugin_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list plugin status snapshots: %w", err)
	}
	defer rows.Close()
	return scanPluginStatusSnapshots(rows)
}

func (s *SQLiteStateStore) SaveSession(ctx context.Context, session SessionState) error {
	payload, err := json.Marshal(session.State)
	if err != nil {
		return fmt.Errorf("marshal session state: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO sessions (session_id, plugin_id, state_json, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
  plugin_id=excluded.plugin_id,
  state_json=excluded.state_json,
  updated_at=excluded.updated_at
`, session.SessionID, session.PluginID, string(payload), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) SaveIdempotencyKey(ctx context.Context, key string, eventID string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO idempotency_keys (idempotency_key, event_id, created_at)
VALUES (?, ?, ?)
`, key, eventID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert idempotency key: %w", err)
	}
	return nil
}

type sqliteExecContexter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *SQLiteStateStore) SaveJob(ctx context.Context, job Job) error {
	if err := saveJobWithExecutor(ctx, s.db, job); err != nil {
		return err
	}
	return nil
}

func saveJobWithExecutor(ctx context.Context, executor sqliteExecContexter, job Job) error {
	if err := job.Validate(); err != nil {
		return fmt.Errorf("validate job: %w", err)
	}

	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return fmt.Errorf("marshal job payload: %w", err)
	}

	_, err = executor.ExecContext(ctx, `
INSERT INTO jobs (
  job_id, job_type, status, payload_json, retry_count, max_retries, timeout_ms, last_error,
  reason_code, created_at, started_at, finished_at, next_run_at, worker_id, lease_acquired_at,
  lease_expires_at, heartbeat_at, dead_letter, trace_id, event_id, run_id, correlation, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(job_id) DO UPDATE SET
  job_type=excluded.job_type,
  status=excluded.status,
  payload_json=excluded.payload_json,
  retry_count=excluded.retry_count,
  max_retries=excluded.max_retries,
  timeout_ms=excluded.timeout_ms,
  last_error=excluded.last_error,
  reason_code=excluded.reason_code,
  created_at=excluded.created_at,
  started_at=excluded.started_at,
  finished_at=excluded.finished_at,
  next_run_at=excluded.next_run_at,
  worker_id=excluded.worker_id,
  lease_acquired_at=excluded.lease_acquired_at,
  lease_expires_at=excluded.lease_expires_at,
  heartbeat_at=excluded.heartbeat_at,
  dead_letter=excluded.dead_letter,
  trace_id=excluded.trace_id,
  event_id=excluded.event_id,
  run_id=excluded.run_id,
  correlation=excluded.correlation,
  updated_at=excluded.updated_at
`, job.ID, job.Type, job.Status, string(payload), job.RetryCount, job.MaxRetries, job.Timeout.Milliseconds(), job.LastError,
		string(job.ReasonCode), formatSQLiteTimestamp(job.CreatedAt), nullableSQLiteTimestamp(job.StartedAt), nullableSQLiteTimestamp(job.FinishedAt), nullableSQLiteTimestamp(job.NextRunAt), job.WorkerID,
		nullableSQLiteTimestamp(job.LeaseAcquiredAt), nullableSQLiteTimestamp(job.LeaseExpiresAt), nullableSQLiteTimestamp(job.HeartbeatAt), boolToSQLiteInt(job.DeadLetter),
		job.TraceID, job.EventID, job.RunID, job.Correlation, formatSQLiteTimestamp(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("upsert job: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) RecordAlert(ctx context.Context, alert AlertRecord) error {
	if err := saveAlertWithExecutor(ctx, s.db, alert); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStateStore) PersistJobDeadLetter(ctx context.Context, job Job, alert AlertRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin dead-letter persistence transaction: %w", err)
	}
	if err := saveJobWithExecutor(ctx, tx, job); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := saveAlertWithExecutor(ctx, tx, alert); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit dead-letter persistence transaction: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) PersistJobDeadLetterRetry(ctx context.Context, job Job, alertID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin dead-letter retry persistence transaction: %w", err)
	}
	if err := saveJobWithExecutor(ctx, tx, job); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := deleteAlertWithExecutor(ctx, tx, alertID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit dead-letter retry persistence transaction: %w", err)
	}
	return nil
}

func saveAlertWithExecutor(ctx context.Context, executor sqliteExecContexter, alert AlertRecord) error {
	normalized, err := normalizeAlertRecord(alert)
	if err != nil {
		return fmt.Errorf("validate alert: %w", err)
	}
	_, err = executor.ExecContext(ctx, `
INSERT OR IGNORE INTO alerts (
  alert_id, object_type, object_id, failure_type, first_occurred_at, latest_occurred_at,
  latest_reason, trace_id, event_id, run_id, correlation, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, normalized.ID, normalized.ObjectType, normalized.ObjectID, normalized.FailureType,
		formatSQLiteTimestamp(normalized.FirstOccurredAt), formatSQLiteTimestamp(normalized.LatestOccurredAt), normalized.LatestReason,
		normalized.TraceID, normalized.EventID, normalized.RunID, normalized.Correlation, formatSQLiteTimestamp(normalized.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert alert: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) DeleteAlert(ctx context.Context, id string) error {
	return deleteAlertWithExecutor(ctx, s.db, id)
}

func deleteAlertWithExecutor(ctx context.Context, executor sqliteExecContexter, id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("delete alert: alert id is required")
	}
	if _, err := executor.ExecContext(ctx, `DELETE FROM alerts WHERE alert_id = ?`, trimmed); err != nil {
		return fmt.Errorf("delete alert: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) LoadJob(ctx context.Context, id string) (Job, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT job_id, job_type, status, payload_json, retry_count, max_retries, timeout_ms, last_error,
       reason_code, created_at, started_at, finished_at, next_run_at, worker_id, lease_acquired_at,
       lease_expires_at, heartbeat_at, dead_letter, trace_id, event_id, run_id, correlation
FROM jobs
WHERE job_id = ?
`, id)
	if err != nil {
		return Job{}, fmt.Errorf("load job: %w", err)
	}
	defer rows.Close()

	jobs, err := scanJobs(rows)
	if err != nil {
		return Job{}, err
	}
	if len(jobs) == 0 {
		return Job{}, fmt.Errorf("load job: %w", sql.ErrNoRows)
	}
	return jobs[0], nil
}

func (s *SQLiteStateStore) ListJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT job_id, job_type, status, payload_json, retry_count, max_retries, timeout_ms, last_error,
       reason_code, created_at, started_at, finished_at, next_run_at, worker_id, lease_acquired_at,
       lease_expires_at, heartbeat_at, dead_letter, trace_id, event_id, run_id, correlation
FROM jobs
ORDER BY created_at ASC, job_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	return scanJobs(rows)
}

func (s *SQLiteStateStore) ListAlerts(ctx context.Context) ([]AlertRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT alert_id, object_type, object_id, failure_type, first_occurred_at, latest_occurred_at,
       latest_reason, trace_id, event_id, run_id, correlation, created_at
FROM alerts
ORDER BY latest_occurred_at DESC, alert_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()
	return scanAlerts(rows)
}

func (s *SQLiteStateStore) SaveSchedulePlan(ctx context.Context, stored storedSchedulePlan) error {
	if err := stored.Plan.Validate(); err != nil {
		return fmt.Errorf("validate schedule plan: %w", err)
	}
	payload, err := json.Marshal(stored.Plan.Metadata)
	if err != nil {
		return fmt.Errorf("marshal schedule metadata: %w", err)
	}
	createdAt := stored.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := stored.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	dueAtEvidence := normalizeStoredScheduleDueAtEvidence(stored)
	claimOwner := strings.TrimSpace(stored.ClaimOwner)

	_, err = s.db.ExecContext(ctx, `
INSERT INTO schedule_plans (
	 schedule_id, kind, cron_expr, delay_ms, execute_at, due_at, due_at_evidence, claim_owner, claimed_at, source, event_type,
	 metadata_json, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(schedule_id) DO UPDATE SET
	 kind=excluded.kind,
	 cron_expr=excluded.cron_expr,
	 delay_ms=excluded.delay_ms,
	 execute_at=excluded.execute_at,
	 due_at=excluded.due_at,
	 due_at_evidence=excluded.due_at_evidence,
	 claim_owner=excluded.claim_owner,
	 claimed_at=excluded.claimed_at,
	 source=excluded.source,
	 event_type=excluded.event_type,
	 metadata_json=excluded.metadata_json,
	 created_at=excluded.created_at,
	 updated_at=excluded.updated_at
`, stored.Plan.ID, stored.Plan.Kind, stored.Plan.CronExpr, stored.Plan.Delay.Milliseconds(), nullableNonZeroSQLiteTimestamp(stored.Plan.ExecuteAt), nullableSQLiteTimestamp(stored.DueAt), dueAtEvidence, claimOwner, nullableSQLiteTimestamp(stored.ClaimedAt), stored.Plan.Source, stored.Plan.EventType, string(payload), formatSQLiteTimestamp(createdAt), formatSQLiteTimestamp(updatedAt))
	if err != nil {
		return fmt.Errorf("upsert schedule plan: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) LoadSchedulePlan(ctx context.Context, id string) (storedSchedulePlan, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT schedule_id, kind, cron_expr, delay_ms, execute_at, due_at, due_at_evidence, claim_owner, claimed_at, source, event_type,
       metadata_json, created_at, updated_at
FROM schedule_plans
WHERE schedule_id = ?
`, id)
	if err != nil {
		return storedSchedulePlan{}, fmt.Errorf("load schedule plan: %w", err)
	}
	defer rows.Close()
	plans, err := scanStoredSchedulePlans(rows)
	if err != nil {
		return storedSchedulePlan{}, err
	}
	if len(plans) == 0 {
		return storedSchedulePlan{}, fmt.Errorf("load schedule plan: %w", sql.ErrNoRows)
	}
	return plans[0], nil
}

func (s *SQLiteStateStore) ListSchedulePlans(ctx context.Context) ([]storedSchedulePlan, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT schedule_id, kind, cron_expr, delay_ms, execute_at, due_at, due_at_evidence, claim_owner, claimed_at, source, event_type,
       metadata_json, created_at, updated_at
FROM schedule_plans
ORDER BY created_at ASC, schedule_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list schedule plans: %w", err)
	}
	defer rows.Close()
	return scanStoredSchedulePlans(rows)
}

func (s *SQLiteStateStore) DeleteSchedulePlan(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM schedule_plans WHERE schedule_id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete schedule plan: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete schedule plan rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("delete schedule plan: %w", sql.ErrNoRows)
	}
	return nil
}

func (s *SQLiteStateStore) ClaimSchedulePlan(ctx context.Context, id string, expectedDueAt time.Time, claim schedulePlanClaim) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, fmt.Errorf("claim schedule plan: schedule id is required")
	}
	claim.ClaimOwner = strings.TrimSpace(claim.ClaimOwner)
	if claim.ClaimOwner == "" {
		return false, fmt.Errorf("claim schedule plan: claim owner is required")
	}
	if expectedDueAt.IsZero() {
		return false, fmt.Errorf("claim schedule plan: expected dueAt is required")
	}
	if claim.ClaimedAt.IsZero() {
		return false, fmt.Errorf("claim schedule plan: claimedAt is required")
	}
	if claim.UpdatedAt.IsZero() {
		claim.UpdatedAt = claim.ClaimedAt
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE schedule_plans
SET claim_owner = ?, claimed_at = ?, updated_at = ?
WHERE schedule_id = ?
  AND due_at = ?
  AND claim_owner = ''
  AND claimed_at IS NULL
`, claim.ClaimOwner, formatSQLiteTimestamp(claim.ClaimedAt), formatSQLiteTimestamp(claim.UpdatedAt), id, formatSQLiteTimestamp(expectedDueAt))
	if err != nil {
		return false, fmt.Errorf("claim schedule plan: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim schedule plan rows affected: %w", err)
	}
	return affected == 1, nil
}

func (s *SQLiteStateStore) HasIdempotencyKey(ctx context.Context, key string) (bool, error) {
	var found string
	err := s.db.QueryRowContext(ctx, `SELECT idempotency_key FROM idempotency_keys WHERE idempotency_key = ?`, key).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query idempotency key: %w", err)
	}
	return true, nil
}

func (s *SQLiteStateStore) Counts(ctx context.Context) (map[string]int, error) {
	tables := map[string]string{
		"event_journal":             `SELECT COUNT(*) FROM event_journal`,
		"plugin_registry":           `SELECT COUNT(*) FROM plugin_registry`,
		"plugin_enabled_overlays":   `SELECT COUNT(*) FROM plugin_enabled_overlays`,
		"plugin_configs":            `SELECT COUNT(*) FROM plugin_configs`,
		"plugin_status_snapshots":   `SELECT COUNT(*) FROM plugin_status_snapshots`,
		"adapter_instances":         `SELECT COUNT(*) FROM adapter_instances`,
		"sessions":                  `SELECT COUNT(*) FROM sessions`,
		"idempotency_keys":          `SELECT COUNT(*) FROM idempotency_keys`,
		"jobs":                      `SELECT COUNT(*) FROM jobs`,
		"alerts":                    `SELECT COUNT(*) FROM alerts`,
		"schedule_plans":            `SELECT COUNT(*) FROM schedule_plans`,
		"operator_identities":       `SELECT COUNT(*) FROM operator_identities`,
		"rbac_snapshots":            `SELECT COUNT(*) FROM rbac_snapshots`,
		"replay_operation_records":  `SELECT COUNT(*) FROM replay_operation_records`,
		"rollout_operation_records": `SELECT COUNT(*) FROM rollout_operation_records`,
	}

	counts := make(map[string]int, len(tables))
	for name, query := range tables {
		var count int
		if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return nil, fmt.Errorf("count %s: %w", name, err)
		}
		counts[name] = count
	}
	return counts, nil
}

func (s *SQLiteStateStore) SaveReplayOperationRecord(ctx context.Context, record ReplayOperationRecord) error {
	if s == nil {
		return fmt.Errorf("save replay operation record: sqlite state store is required")
	}
	return saveReplayOperationRecordWithExecutor(ctx, s.db, record)
}

func saveReplayOperationRecordWithExecutor(ctx context.Context, executor sqliteExecContexter, record ReplayOperationRecord) error {
	record.ReplayID = strings.TrimSpace(record.ReplayID)
	if record.ReplayID == "" {
		return fmt.Errorf("save replay operation record: replay id is required")
	}
	record.SourceEventID = strings.TrimSpace(record.SourceEventID)
	if record.SourceEventID == "" {
		return fmt.Errorf("save replay operation record: source event id is required")
	}
	record.ReplayEventID = strings.TrimSpace(record.ReplayEventID)
	if record.ReplayEventID == "" {
		return fmt.Errorf("save replay operation record: replay event id is required")
	}
	record.Status = strings.TrimSpace(record.Status)
	if record.Status == "" {
		return fmt.Errorf("save replay operation record: status is required")
	}
	record.Reason = strings.TrimSpace(record.Reason)
	occurredAt := record.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	updatedAt := record.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = occurredAt
	}
	_, err := executor.ExecContext(ctx, `
INSERT INTO replay_operation_records (replay_id, source_event_id, replay_event_id, status, reason, occurred_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(replay_id) DO UPDATE SET
  source_event_id=excluded.source_event_id,
  replay_event_id=excluded.replay_event_id,
  status=excluded.status,
  reason=excluded.reason,
  occurred_at=excluded.occurred_at,
  updated_at=excluded.updated_at
`, record.ReplayID, record.SourceEventID, record.ReplayEventID, record.Status, record.Reason, formatSQLiteTimestamp(occurredAt), formatSQLiteTimestamp(updatedAt))
	if err != nil {
		return fmt.Errorf("upsert replay operation record: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) ListReplayOperationRecords(ctx context.Context) ([]ReplayOperationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT replay_id, source_event_id, replay_event_id, status, reason, occurred_at, updated_at
FROM replay_operation_records
ORDER BY occurred_at DESC, replay_id DESC
`)
	if err != nil {
		return nil, fmt.Errorf("list replay operation records: %w", err)
	}
	defer rows.Close()
	return scanReplayOperationRecords(rows)
}

func (s *SQLiteStateStore) SaveRolloutOperationRecord(ctx context.Context, record RolloutOperationRecord) error {
	if s == nil {
		return fmt.Errorf("save rollout operation record: sqlite state store is required")
	}
	return saveRolloutOperationRecordWithExecutor(ctx, s.db, record)
}

func saveRolloutOperationRecordWithExecutor(ctx context.Context, executor sqliteExecContexter, record RolloutOperationRecord) error {
	record.OperationID = strings.TrimSpace(record.OperationID)
	if record.OperationID == "" {
		return fmt.Errorf("save rollout operation record: operation id is required")
	}
	record.PluginID = strings.TrimSpace(record.PluginID)
	if record.PluginID == "" {
		return fmt.Errorf("save rollout operation record: plugin id is required")
	}
	record.Action = strings.TrimSpace(record.Action)
	if record.Action == "" {
		return fmt.Errorf("save rollout operation record: action is required")
	}
	record.Status = strings.TrimSpace(record.Status)
	if record.Status == "" {
		return fmt.Errorf("save rollout operation record: status is required")
	}
	record.Reason = strings.TrimSpace(record.Reason)
	occurredAt := record.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	updatedAt := record.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = occurredAt
	}
	_, err := executor.ExecContext(ctx, `
INSERT INTO rollout_operation_records (operation_id, plugin_id, action, current_version, candidate_version, status, reason, occurred_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(operation_id) DO UPDATE SET
  plugin_id=excluded.plugin_id,
  action=excluded.action,
  current_version=excluded.current_version,
  candidate_version=excluded.candidate_version,
  status=excluded.status,
  reason=excluded.reason,
  occurred_at=excluded.occurred_at,
  updated_at=excluded.updated_at
`, record.OperationID, record.PluginID, record.Action, record.CurrentVersion, record.CandidateVersion, record.Status, record.Reason, formatSQLiteTimestamp(occurredAt), formatSQLiteTimestamp(updatedAt))
	if err != nil {
		return fmt.Errorf("upsert rollout operation record: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) ListRolloutOperationRecords(ctx context.Context) ([]RolloutOperationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT operation_id, plugin_id, action, current_version, candidate_version, status, reason, occurred_at, updated_at
FROM rollout_operation_records
ORDER BY occurred_at DESC, operation_id DESC
`)
	if err != nil {
		return nil, fmt.Errorf("list rollout operation records: %w", err)
	}
	defer rows.Close()
	return scanRolloutOperationRecords(rows)
}

func (s *SQLiteStateStore) ReplaceCurrentRBACState(ctx context.Context, identities []OperatorIdentityState, snapshot RBACSnapshotState) error {
	if s == nil {
		return fmt.Errorf("replace current rbac state: sqlite state store is required")
	}
	if strings.TrimSpace(snapshot.SnapshotKey) == "" {
		snapshot.SnapshotKey = CurrentRBACSnapshotKey
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin current rbac state transaction: %w", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM operator_identities`); err != nil {
		return rollback(fmt.Errorf("clear operator identities: %w", err))
	}
	for _, identity := range identities {
		if err := saveOperatorIdentityWithExecutor(ctx, tx, identity); err != nil {
			return rollback(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rbac_snapshots WHERE snapshot_key <> ?`, snapshot.SnapshotKey); err != nil {
		return rollback(fmt.Errorf("clear stale rbac snapshots: %w", err))
	}
	if err := saveRBACSnapshotWithExecutor(ctx, tx, snapshot); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit current rbac state transaction: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) SaveOperatorIdentity(ctx context.Context, state OperatorIdentityState) error {
	if s == nil {
		return fmt.Errorf("save operator identity: sqlite state store is required")
	}
	return saveOperatorIdentityWithExecutor(ctx, s.db, state)
}

func saveOperatorIdentityWithExecutor(ctx context.Context, executor sqliteExecContexter, state OperatorIdentityState) error {
	state.ActorID = strings.TrimSpace(state.ActorID)
	if state.ActorID == "" {
		return fmt.Errorf("save operator identity: actor id is required")
	}
	normalizedRoles := normalizeStringSlice(state.Roles)
	rolesJSON, err := json.Marshal(normalizedRoles)
	if err != nil {
		return fmt.Errorf("marshal operator identity roles: %w", err)
	}
	updatedAt := state.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err = executor.ExecContext(ctx, `
INSERT INTO operator_identities (actor_id, roles_json, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(actor_id) DO UPDATE SET
  roles_json=excluded.roles_json,
  updated_at=excluded.updated_at
`, state.ActorID, string(rolesJSON), formatSQLiteTimestamp(updatedAt))
	if err != nil {
		return fmt.Errorf("upsert operator identity: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) LoadOperatorIdentity(ctx context.Context, actorID string) (OperatorIdentityState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT actor_id, roles_json, updated_at
FROM operator_identities
WHERE actor_id = ?
`, strings.TrimSpace(actorID))
	if err != nil {
		return OperatorIdentityState{}, fmt.Errorf("load operator identity: %w", err)
	}
	defer rows.Close()
	states, err := scanOperatorIdentities(rows)
	if err != nil {
		return OperatorIdentityState{}, err
	}
	if len(states) == 0 {
		return OperatorIdentityState{}, sql.ErrNoRows
	}
	return states[0], nil
}

func (s *SQLiteStateStore) ListOperatorIdentities(ctx context.Context) ([]OperatorIdentityState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT actor_id, roles_json, updated_at
FROM operator_identities
ORDER BY actor_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list operator identities: %w", err)
	}
	defer rows.Close()
	return scanOperatorIdentities(rows)
}

func (s *SQLiteStateStore) SaveRBACSnapshot(ctx context.Context, snapshot RBACSnapshotState) error {
	if s == nil {
		return fmt.Errorf("save rbac snapshot: sqlite state store is required")
	}
	return saveRBACSnapshotWithExecutor(ctx, s.db, snapshot)
}

func saveRBACSnapshotWithExecutor(ctx context.Context, executor sqliteExecContexter, snapshot RBACSnapshotState) error {
	snapshot.SnapshotKey = strings.TrimSpace(snapshot.SnapshotKey)
	if snapshot.SnapshotKey == "" {
		snapshot.SnapshotKey = CurrentRBACSnapshotKey
	}
	policiesJSON, err := json.Marshal(cloneAuthorizationPolicies(snapshot.Policies))
	if err != nil {
		return fmt.Errorf("marshal rbac snapshot policies: %w", err)
	}
	updatedAt := snapshot.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err = executor.ExecContext(ctx, `
INSERT INTO rbac_snapshots (snapshot_key, console_read_permission, policies_json, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(snapshot_key) DO UPDATE SET
  console_read_permission=excluded.console_read_permission,
  policies_json=excluded.policies_json,
  updated_at=excluded.updated_at
`, snapshot.SnapshotKey, strings.TrimSpace(snapshot.ConsoleReadPermission), string(policiesJSON), formatSQLiteTimestamp(updatedAt))
	if err != nil {
		return fmt.Errorf("upsert rbac snapshot: %w", err)
	}
	return nil
}

func (s *SQLiteStateStore) LoadRBACSnapshot(ctx context.Context, snapshotKey string) (RBACSnapshotState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT snapshot_key, console_read_permission, policies_json, updated_at
FROM rbac_snapshots
WHERE snapshot_key = ?
`, strings.TrimSpace(snapshotKey))
	if err != nil {
		return RBACSnapshotState{}, fmt.Errorf("load rbac snapshot: %w", err)
	}
	defer rows.Close()
	states, err := scanRBACSnapshots(rows)
	if err != nil {
		return RBACSnapshotState{}, err
	}
	if len(states) == 0 {
		return RBACSnapshotState{}, sql.ErrNoRows
	}
	return states[0], nil
}

func (s *SQLiteStateStore) ListRBACSnapshots(ctx context.Context) ([]RBACSnapshotState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT snapshot_key, console_read_permission, policies_json, updated_at
FROM rbac_snapshots
ORDER BY snapshot_key ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list rbac snapshots: %w", err)
	}
	defer rows.Close()
	return scanRBACSnapshots(rows)
}

func nextPluginStatusSnapshot(previous *PluginStatusSnapshot, result DispatchResult) PluginStatusSnapshot {
	dispatchAt := result.At.UTC()
	snapshot := PluginStatusSnapshot{
		PluginID:            result.PluginID,
		LastDispatchKind:    result.Kind,
		LastDispatchSuccess: result.Success,
		LastDispatchError:   result.Error,
		LastDispatchAt:      dispatchAt,
		UpdatedAt:           time.Now().UTC(),
	}
	if previous != nil && previous.LastRecoveredAt != nil {
		recoveredAt := previous.LastRecoveredAt.UTC()
		snapshot.LastRecoveredAt = &recoveredAt
		snapshot.LastRecoveryFailureCount = previous.LastRecoveryFailureCount
	}
	if !result.Success {
		snapshot.CurrentFailureStreak = 1
		if previous != nil && !previous.LastDispatchSuccess {
			streak := previous.CurrentFailureStreak
			if streak <= 0 {
				streak = 1
			}
			snapshot.CurrentFailureStreak = streak + 1
		}
		return snapshot
	}
	if previous != nil && previous.CurrentFailureStreak > 0 {
		recoveredAt := dispatchAt
		snapshot.LastRecoveredAt = &recoveredAt
		snapshot.LastRecoveryFailureCount = previous.CurrentFailureStreak
	}
	return snapshot
}

func scanPluginStatusSnapshots(rows *sql.Rows) ([]PluginStatusSnapshot, error) {
	snapshots := make([]PluginStatusSnapshot, 0)
	for rows.Next() {
		var (
			snapshot            PluginStatusSnapshot
			lastDispatchSuccess int
			lastRecoveredAtRaw  sql.NullString
			lastDispatchAtRaw   string
			updatedAtRaw        string
		)
		if err := rows.Scan(
			&snapshot.PluginID,
			&snapshot.LastDispatchKind,
			&lastDispatchSuccess,
			&snapshot.LastDispatchError,
			&lastDispatchAtRaw,
			&lastRecoveredAtRaw,
			&snapshot.LastRecoveryFailureCount,
			&snapshot.CurrentFailureStreak,
			&updatedAtRaw,
		); err != nil {
			return nil, fmt.Errorf("scan plugin status snapshot: %w", err)
		}
		snapshot.LastDispatchSuccess = lastDispatchSuccess == 1
		lastDispatchAt, err := parseSQLiteTimestamp(lastDispatchAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse plugin status snapshot last_dispatch_at: %w", err)
		}
		snapshot.LastDispatchAt = lastDispatchAt
		if snapshot.LastRecoveredAt, err = parseNullableSQLiteTimestamp(lastRecoveredAtRaw); err != nil {
			return nil, fmt.Errorf("parse plugin status snapshot last_recovered_at: %w", err)
		}
		updatedAt, err := parseSQLiteTimestamp(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse plugin status snapshot updated_at: %w", err)
		}
		snapshot.UpdatedAt = updatedAt
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin status snapshots: %w", err)
	}
	return snapshots, nil
}

func scanPluginEnabledStates(rows *sql.Rows) ([]PluginEnabledState, error) {
	states := make([]PluginEnabledState, 0)
	for rows.Next() {
		var (
			state        PluginEnabledState
			enabled      int
			updatedAtRaw string
		)
		if err := rows.Scan(&state.PluginID, &enabled, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan plugin enabled state: %w", err)
		}
		state.Enabled = enabled == 1
		updatedAt, err := parseSQLiteTimestamp(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse plugin enabled state updated_at: %w", err)
		}
		state.UpdatedAt = updatedAt
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin enabled states: %w", err)
	}
	return states, nil
}

func scanAdapterInstances(rows *sql.Rows) ([]AdapterInstanceState, error) {
	states := make([]AdapterInstanceState, 0)
	for rows.Next() {
		var (
			state        AdapterInstanceState
			rawConfig    string
			online       int
			updatedAtRaw string
		)
		if err := rows.Scan(&state.InstanceID, &state.Adapter, &state.Source, &rawConfig, &state.Status, &state.Health, &online, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan adapter instance: %w", err)
		}
		state.RawConfig = append(json.RawMessage(nil), rawConfig...)
		state.Online = online == 1
		updatedAt, err := parseSQLiteTimestamp(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse adapter instance updated_at: %w", err)
		}
		state.UpdatedAt = updatedAt
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate adapter instances: %w", err)
	}
	return states, nil
}

func scanOperatorIdentities(rows *sql.Rows) ([]OperatorIdentityState, error) {
	states := make([]OperatorIdentityState, 0)
	for rows.Next() {
		var (
			state        OperatorIdentityState
			rolesJSON    string
			updatedAtRaw string
		)
		if err := rows.Scan(&state.ActorID, &rolesJSON, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan operator identity: %w", err)
		}
		if rolesJSON != "" && rolesJSON != "null" {
			if err := json.Unmarshal([]byte(rolesJSON), &state.Roles); err != nil {
				return nil, fmt.Errorf("unmarshal operator identity roles: %w", err)
			}
		}
		state.Roles = normalizeStringSlice(state.Roles)
		updatedAt, err := parseSQLiteTimestamp(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse operator identity updated_at: %w", err)
		}
		state.UpdatedAt = updatedAt
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operator identities: %w", err)
	}
	return states, nil
}

func scanRBACSnapshots(rows *sql.Rows) ([]RBACSnapshotState, error) {
	states := make([]RBACSnapshotState, 0)
	for rows.Next() {
		var (
			state        RBACSnapshotState
			policiesJSON string
			updatedAtRaw string
		)
		if err := rows.Scan(&state.SnapshotKey, &state.ConsoleReadPermission, &policiesJSON, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan rbac snapshot: %w", err)
		}
		if policiesJSON != "" && policiesJSON != "null" {
			if err := json.Unmarshal([]byte(policiesJSON), &state.Policies); err != nil {
				return nil, fmt.Errorf("unmarshal rbac snapshot policies: %w", err)
			}
		}
		state.Policies = cloneAuthorizationPolicies(state.Policies)
		updatedAt, err := parseSQLiteTimestamp(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse rbac snapshot updated_at: %w", err)
		}
		state.UpdatedAt = updatedAt
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rbac snapshots: %w", err)
	}
	return states, nil
}

func scanReplayOperationRecords(rows *sql.Rows) ([]ReplayOperationRecord, error) {
	records := make([]ReplayOperationRecord, 0)
	for rows.Next() {
		var (
			record        ReplayOperationRecord
			occurredAtRaw string
			updatedAtRaw  string
		)
		if err := rows.Scan(&record.ReplayID, &record.SourceEventID, &record.ReplayEventID, &record.Status, &record.Reason, &occurredAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan replay operation record: %w", err)
		}
		occurredAt, err := parseSQLiteTimestamp(occurredAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse replay operation record occurred_at: %w", err)
		}
		record.OccurredAt = occurredAt
		updatedAt, err := parseSQLiteTimestamp(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse replay operation record updated_at: %w", err)
		}
		record.UpdatedAt = updatedAt
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate replay operation records: %w", err)
	}
	return records, nil
}

func scanRolloutOperationRecords(rows *sql.Rows) ([]RolloutOperationRecord, error) {
	records := make([]RolloutOperationRecord, 0)
	for rows.Next() {
		var (
			record        RolloutOperationRecord
			occurredAtRaw string
			updatedAtRaw  string
		)
		if err := rows.Scan(&record.OperationID, &record.PluginID, &record.Action, &record.CurrentVersion, &record.CandidateVersion, &record.Status, &record.Reason, &occurredAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan rollout operation record: %w", err)
		}
		occurredAt, err := parseSQLiteTimestamp(occurredAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse rollout operation record occurred_at: %w", err)
		}
		record.OccurredAt = occurredAt
		updatedAt, err := parseSQLiteTimestamp(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse rollout operation record updated_at: %w", err)
		}
		record.UpdatedAt = updatedAt
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rollout operation records: %w", err)
	}
	return records, nil
}

func scanStoredSchedulePlans(rows *sql.Rows) ([]storedSchedulePlan, error) {
	plans := make([]storedSchedulePlan, 0)
	for rows.Next() {
		var (
			stored        storedSchedulePlan
			kind          string
			delayMS       int64
			executeAtRaw  sql.NullString
			dueAtRaw      sql.NullString
			dueAtEvidence string
			claimOwner    string
			claimedAtRaw  sql.NullString
			metadataJSON  string
			createdAtRaw  string
			updatedAtRaw  string
		)
		if err := rows.Scan(
			&stored.Plan.ID,
			&kind,
			&stored.Plan.CronExpr,
			&delayMS,
			&executeAtRaw,
			&dueAtRaw,
			&dueAtEvidence,
			&claimOwner,
			&claimedAtRaw,
			&stored.Plan.Source,
			&stored.Plan.EventType,
			&metadataJSON,
			&createdAtRaw,
			&updatedAtRaw,
		); err != nil {
			return nil, fmt.Errorf("scan schedule plan: %w", err)
		}
		stored.Plan.Kind = ScheduleKind(kind)
		stored.Plan.Delay = time.Duration(delayMS) * time.Millisecond
		executeAt, err := parseNullableScheduleTimestamp(executeAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse schedule execute_at: %w", err)
		}
		stored.Plan.ExecuteAt = executeAt
		dueAt, err := parseNullableSQLiteTimestamp(dueAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse schedule due_at: %w", err)
		}
		stored.DueAt = dueAt
		stored.DueAtEvidence = strings.TrimSpace(dueAtEvidence)
		stored.ClaimOwner = strings.TrimSpace(claimOwner)
		claimedAt, err := parseNullableSQLiteTimestamp(claimedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse schedule claimed_at: %w", err)
		}
		stored.ClaimedAt = claimedAt
		if createdAt, err := parseSQLiteTimestamp(createdAtRaw); err != nil {
			return nil, fmt.Errorf("parse schedule created_at: %w", err)
		} else {
			stored.CreatedAt = createdAt
		}
		if updatedAt, err := parseSQLiteTimestamp(updatedAtRaw); err != nil {
			return nil, fmt.Errorf("parse schedule updated_at: %w", err)
		} else {
			stored.UpdatedAt = updatedAt
		}
		if metadataJSON != "" && metadataJSON != "null" {
			if err := json.Unmarshal([]byte(metadataJSON), &stored.Plan.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshal schedule metadata: %w", err)
			}
		}
		plans = append(plans, stored)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schedule plans: %w", err)
	}
	return plans, nil
}

func normalizeStoredScheduleDueAtEvidence(stored storedSchedulePlan) string {
	evidence := strings.TrimSpace(stored.DueAtEvidence)
	if evidence != "" {
		return evidence
	}
	if stored.DueAt != nil && !stored.DueAt.IsZero() {
		return scheduleDueAtEvidencePersisted
	}
	return ""
}

func scanJobs(rows *sql.Rows) ([]Job, error) {
	jobs := make([]Job, 0)
	for rows.Next() {
		var (
			job                Job
			status             string
			reasonCode         string
			payloadJSON        string
			createdAtRaw       string
			startedAtRaw       sql.NullString
			finishedRaw        sql.NullString
			nextRunRaw         sql.NullString
			leaseAcquiredAtRaw sql.NullString
			leaseExpiresAtRaw  sql.NullString
			heartbeatAtRaw     sql.NullString
			deadLetter         int
			timeoutMS          int64
		)
		if err := rows.Scan(
			&job.ID,
			&job.Type,
			&status,
			&payloadJSON,
			&job.RetryCount,
			&job.MaxRetries,
			&timeoutMS,
			&job.LastError,
			&reasonCode,
			&createdAtRaw,
			&startedAtRaw,
			&finishedRaw,
			&nextRunRaw,
			&job.WorkerID,
			&leaseAcquiredAtRaw,
			&leaseExpiresAtRaw,
			&heartbeatAtRaw,
			&deadLetter,
			&job.TraceID,
			&job.EventID,
			&job.RunID,
			&job.Correlation,
		); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		job.Status = JobStatus(status)
		job.ReasonCode = pluginsdk.JobReasonCode(reasonCode)
		job.Timeout = time.Duration(timeoutMS) * time.Millisecond
		job.DeadLetter = deadLetter == 1

		if payloadJSON != "" && payloadJSON != "null" {
			if err := json.Unmarshal([]byte(payloadJSON), &job.Payload); err != nil {
				return nil, fmt.Errorf("unmarshal job payload: %w", err)
			}
		}

		createdAt, err := parseSQLiteTimestamp(createdAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse job created_at: %w", err)
		}
		job.CreatedAt = createdAt

		if job.StartedAt, err = parseNullableSQLiteTimestamp(startedAtRaw); err != nil {
			return nil, fmt.Errorf("parse job started_at: %w", err)
		}
		if job.FinishedAt, err = parseNullableSQLiteTimestamp(finishedRaw); err != nil {
			return nil, fmt.Errorf("parse job finished_at: %w", err)
		}
		if job.NextRunAt, err = parseNullableSQLiteTimestamp(nextRunRaw); err != nil {
			return nil, fmt.Errorf("parse job next_run_at: %w", err)
		}
		if job.LeaseAcquiredAt, err = parseNullableSQLiteTimestamp(leaseAcquiredAtRaw); err != nil {
			return nil, fmt.Errorf("parse job lease_acquired_at: %w", err)
		}
		if job.LeaseExpiresAt, err = parseNullableSQLiteTimestamp(leaseExpiresAtRaw); err != nil {
			return nil, fmt.Errorf("parse job lease_expires_at: %w", err)
		}
		if job.HeartbeatAt, err = parseNullableSQLiteTimestamp(heartbeatAtRaw); err != nil {
			return nil, fmt.Errorf("parse job heartbeat_at: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return jobs, nil
}

func scanAlerts(rows *sql.Rows) ([]AlertRecord, error) {
	alerts := make([]AlertRecord, 0)
	for rows.Next() {
		var (
			alert               AlertRecord
			firstOccurredAtRaw  string
			latestOccurredAtRaw string
			createdAtRaw        string
		)
		if err := rows.Scan(
			&alert.ID,
			&alert.ObjectType,
			&alert.ObjectID,
			&alert.FailureType,
			&firstOccurredAtRaw,
			&latestOccurredAtRaw,
			&alert.LatestReason,
			&alert.TraceID,
			&alert.EventID,
			&alert.RunID,
			&alert.Correlation,
			&createdAtRaw,
		); err != nil {
			return nil, fmt.Errorf("scan alert: %w", err)
		}
		firstOccurredAt, err := parseSQLiteTimestamp(firstOccurredAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse alert first_occurred_at: %w", err)
		}
		latestOccurredAt, err := parseSQLiteTimestamp(latestOccurredAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse alert latest_occurred_at: %w", err)
		}
		createdAt, err := parseSQLiteTimestamp(createdAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse alert created_at: %w", err)
		}
		alert.FirstOccurredAt = firstOccurredAt
		alert.LatestOccurredAt = latestOccurredAt
		alert.CreatedAt = createdAt
		alerts = append(alerts, alert)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate alerts: %w", err)
	}
	return alerts, nil
}

func formatSQLiteTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func scanWorkflowInstances(rows *sql.Rows) ([]WorkflowInstanceState, error) {
	instances := make([]WorkflowInstanceState, 0)
	for rows.Next() {
		var (
			instance     WorkflowInstanceState
			status       string
			workflowJSON string
			createdAtRaw string
			updatedAtRaw string
		)
		if err := rows.Scan(&instance.WorkflowID, &instance.PluginID, &status, &workflowJSON, &instance.LastEventID, &instance.LastEventType, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf(`scan workflow instance: %w`, err)
		}
		instance.Status = WorkflowRuntimeStatus(strings.TrimSpace(status))
		if err := json.Unmarshal([]byte(workflowJSON), &instance.Workflow); err != nil {
			return nil, fmt.Errorf(`unmarshal workflow instance: %w`, err)
		}
		instance.Workflow.ID = instance.WorkflowID
		if strings.TrimSpace(string(instance.Status)) == `` {
			instance.Status = workflowRuntimeStatus(instance.Workflow)
		}
		createdAt, err := parseSQLiteTimestamp(createdAtRaw)
		if err != nil {
			return nil, fmt.Errorf(`parse workflow instance created_at: %w`, err)
		}
		instance.CreatedAt = createdAt
		updatedAt, err := parseSQLiteTimestamp(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf(`parse workflow instance updated_at: %w`, err)
		}
		instance.UpdatedAt = updatedAt
		instances = append(instances, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(`iterate workflow instances: %w`, err)
	}
	return instances, nil
}

func nullableSQLiteTimestamp(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatSQLiteTimestamp(*value)
}

func nullableNonZeroSQLiteTimestamp(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatSQLiteTimestamp(value)
}

func parseSQLiteTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, value)
}

func parseNullableSQLiteTimestamp(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseSQLiteTimestamp(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseNullableScheduleTimestamp(value sql.NullString) (time.Time, error) {
	if !value.Valid || value.String == "" {
		return time.Time{}, nil
	}
	return parseSQLiteTimestamp(value.String)
}

func boolToSQLiteInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizeStringSlice(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	normalized := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		normalized = append(normalized, item)
	}
	if len(normalized) == 0 {
		return nil
	}
	sort.Strings(normalized)
	return normalized
}
