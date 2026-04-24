package runtimecore

import (
	"context"
	stdsql "database/sql"
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

type fakeRows struct {
	rows  [][]any
	index int
	err   error
}

type fakePostgresPool struct {
	execSQL      []string
	execArgs     [][]any
	execErr      error
	querySQL     []string
	queryArgs    [][]any
	queryRows    pgx.Rows
	queryRow     pgx.Row
	closed       bool
	commandTag   pgconn.CommandTag
	tx           *fakePostgresTx
	queryFunc    func(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error)
	queryRowFunc func(ctx context.Context, sql string, arguments ...any) pgx.Row
}

type fakePostgresTx struct {
	execSQL    []string
	execArgs   [][]any
	execErr    error
	committed  bool
	rolledBack bool
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

func (r *fakeRows) Next() bool {
	if r == nil {
		return false
	}
	return r.index < len(r.rows)
}

func (r *fakeRows) Scan(dest ...any) error {
	if r == nil {
		return errors.New("fake rows is nil")
	}
	if r.err != nil {
		return r.err
	}
	if r.index >= len(r.rows) {
		return errors.New("scan past rows")
	}
	for i, value := range r.rows[r.index] {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	r.index++
	return nil
}

func (r *fakeRows) Err() error {
	if r == nil {
		return nil
	}
	return r.err
}

func (r *fakeRows) Close() {}

func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Values() ([]any, error)                       { return nil, errors.New("not implemented") }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }

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

func (p *fakePostgresPool) Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error) {
	p.querySQL = append(p.querySQL, sql)
	p.queryArgs = append(p.queryArgs, append([]any(nil), arguments...))
	if p.queryFunc != nil {
		return p.queryFunc(ctx, sql, arguments...)
	}
	if p.queryRows != nil {
		return p.queryRows, nil
	}
	return nil, errors.New("unexpected query")
}

func (p *fakePostgresPool) Begin(_ context.Context) (pgx.Tx, error) {
	if p.tx == nil {
		p.tx = &fakePostgresTx{}
	}
	return p.tx, nil
}

func (p *fakePostgresPool) Close() {
	p.closed = true
}

func (tx *fakePostgresTx) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("nested begin not implemented")
}
func (tx *fakePostgresTx) Commit(context.Context) error   { tx.committed = true; return tx.execErr }
func (tx *fakePostgresTx) Rollback(context.Context) error { tx.rolledBack = true; return nil }
func (tx *fakePostgresTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("copy from not implemented")
}
func (tx *fakePostgresTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (tx *fakePostgresTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (tx *fakePostgresTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("prepare not implemented")
}
func (tx *fakePostgresTx) Exec(_ context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	tx.execSQL = append(tx.execSQL, sql)
	tx.execArgs = append(tx.execArgs, append([]any(nil), arguments...))
	return pgconn.CommandTag{}, tx.execErr
}
func (tx *fakePostgresTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("query not implemented")
}
func (tx *fakePostgresTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeRow{err: errors.New("query row not implemented")}
}
func (tx *fakePostgresTx) Conn() *pgx.Conn { return nil }

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

	pool := &fakePostgresPool{queryRow: fakeRow{err: pgx.ErrNoRows}, commandTag: pgconn.NewCommandTag("INSERT 0 1")}
	store := &PostgresStore{pool: pool}
	ctx := context.Background()
	event := eventmodel.Event{EventID: "evt-pg", TraceID: "trace-pg", Source: "webhook", Type: "webhook.received", Timestamp: time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC), IdempotencyKey: "webhook:pg:1"}
	startedAt := time.Date(2026, 4, 6, 10, 1, 30, 0, time.UTC)
	nextRunAt := startedAt.Add(30 * time.Second)
	job := Job{ID: "job-pg", Type: "ai.chat", Status: JobStatusPending, Payload: map[string]any{"prompt": "hi"}, MaxRetries: 3, Timeout: 30 * time.Second, CreatedAt: time.Date(2026, 4, 6, 10, 1, 0, 0, time.UTC), StartedAt: &startedAt, NextRunAt: &nextRunAt, WorkerID: "runtime-local:postgres-test", LeaseAcquiredAt: &startedAt, LeaseExpiresAt: &nextRunAt, HeartbeatAt: &startedAt, TraceID: "trace-job-pg", EventID: "evt-job-pg", RunID: "run-job-pg", Correlation: "corr-job-pg", ReasonCode: JobReasonCodeExecutionRetry}
	workflow := Workflow{ID: "wf-pg", CurrentIndex: 1, WaitingFor: "message.received", Completed: false, Compensated: false, State: map[string]any{"step": "wait"}}
	manifest := pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess}
	audit := pluginsdk.AuditEntry{
		Actor:         "admin-user",
		Permission:    "plugin:enable",
		Action:        "enable",
		Target:        "plugin-echo",
		Allowed:       true,
		TraceID:       "trace-pg-audit",
		EventID:       "evt-pg-audit",
		PluginID:      "plugin-echo",
		RunID:         "run-pg-audit",
		SessionID:     "session-operator-bearer-admin-user",
		CorrelationID: "corr-pg-audit",
		ErrorCategory: "operator",
		ErrorCode:     "rollout_prepared",
		OccurredAt:    "2026-04-06T10:02:00Z",
	}
	setAuditEntryReason(&audit, "rollout_prepared")
	adapterConfig := json.RawMessage(`{"mode":"demo-ingress"}`)
	alert := AlertRecord{ID: "job.dead_letter:job-pg", ObjectType: "job", ObjectID: "job-pg", FailureType: "job.dead_letter", FirstOccurredAt: startedAt, LatestOccurredAt: startedAt, LatestReason: "timeout", TraceID: job.TraceID, EventID: job.EventID, RunID: job.RunID, Correlation: job.Correlation, CreatedAt: startedAt}
	dueAt := time.Date(2026, 4, 6, 10, 5, 0, 0, time.UTC)
	claimedAt := dueAt.Add(5 * time.Second)
	workflowInstance := WorkflowInstanceState{WorkflowID: "wf-runtime-pg", PluginID: "plugin-workflow-demo", TraceID: "trace-workflow-pg", EventID: "evt-workflow-pg", RunID: "run-workflow-pg", CorrelationID: "corr-workflow-pg", Status: WorkflowRuntimeStatusWaitingEvent, Workflow: workflow, LastEventID: "evt-workflow-pg", LastEventType: "message.received", CreatedAt: time.Date(2026, 4, 6, 10, 6, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 4, 6, 10, 6, 30, 0, time.UTC)}

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
	if err := store.SavePluginEnabledState(ctx, "plugin-echo", false); err != nil {
		t.Fatalf("save plugin enabled state: %v", err)
	}
	if err := store.SavePluginConfig(ctx, "plugin-echo", json.RawMessage(`{"prefix":"persisted: "}`)); err != nil {
		t.Fatalf("save plugin config: %v", err)
	}
	if err := store.SavePluginStatusSnapshot(ctx, DispatchResult{PluginID: "plugin-echo", Kind: "event", Success: false, Error: "timeout", At: time.Date(2026, 4, 6, 10, 3, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("save plugin status snapshot: %v", err)
	}
	if err := store.SaveSession(ctx, SessionState{SessionID: "session-1", PluginID: "plugin-ai-chat", State: map[string]any{"topic": "hello"}}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if err := store.SaveAdapterInstance(ctx, AdapterInstanceState{InstanceID: "adapter-onebot-demo", Adapter: "onebot", Source: "onebot", RawConfig: adapterConfig, Status: "registered", Health: "ready", Online: true}); err != nil {
		t.Fatalf("save adapter instance: %v", err)
	}
	if err := store.SaveReplayOperationRecord(ctx, ReplayOperationRecord{ReplayID: "replay-op-1", SourceEventID: event.EventID, ReplayEventID: "replay-evt-pg", Status: "succeeded", Reason: "replay_dispatched"}); err != nil {
		t.Fatalf("save replay operation record: %v", err)
	}
	if err := store.SaveRolloutOperationRecord(ctx, RolloutOperationRecord{OperationID: "rollout-op-1", PluginID: "plugin-echo", Action: "prepare", CurrentVersion: "0.1.0", CandidateVersion: "0.2.0-candidate", Status: "prepared"}); err != nil {
		t.Fatalf("save rollout operation record: %v", err)
	}
	if err := store.SaveRolloutHead(ctx, RolloutHeadState{PluginID: "plugin-echo", Stable: RolloutSnapshotState{Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess}, Active: RolloutSnapshotState{Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess}, Candidate: &RolloutSnapshotState{Version: "0.2.0-candidate", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess}, Phase: "prepared", Status: "prepared", LastOperationID: "rollout-op-1", UpdatedAt: time.Date(2026, 4, 6, 10, 3, 30, 0, time.UTC)}); err != nil {
		t.Fatalf("save rollout head: %v", err)
	}
	if err := store.RecordAlert(ctx, alert); err != nil {
		t.Fatalf("record alert: %v", err)
	}
	if err := store.SaveSchedulePlan(ctx, storedSchedulePlan{Plan: SchedulePlan{ID: "schedule-pg", Kind: ScheduleKindDelay, Delay: 30 * time.Second, Source: "scheduler", EventType: "schedule.triggered", Metadata: map[string]any{"message_text": "hello"}}, DueAt: &dueAt, DueAtEvidence: scheduleDueAtEvidencePersisted, ClaimOwner: "runtime-local:scheduler", ClaimedAt: &claimedAt, CreatedAt: time.Date(2026, 4, 6, 10, 4, 30, 0, time.UTC), UpdatedAt: claimedAt}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}
	claimed, err := store.ClaimSchedulePlan(ctx, "schedule-pg", dueAt, schedulePlanClaim{ClaimOwner: "runtime-local:claiming-scheduler", ClaimedAt: claimedAt, UpdatedAt: claimedAt})
	if err != nil {
		t.Fatalf("claim schedule plan: %v", err)
	}
	if !claimed {
		t.Fatal("expected claim schedule plan to report one claimed row")
	}
	if err := store.SaveWorkflowInstance(ctx, workflowInstance); err != nil {
		t.Fatalf("save workflow instance: %v", err)
	}
	if err := store.ReplaceCurrentRBACState(ctx, []OperatorIdentityState{{ActorID: "viewer-user", Roles: []string{"viewer"}}}, RBACSnapshotState{SnapshotKey: CurrentRBACSnapshotKey, ConsoleReadPermission: "console:read", Policies: map[string]pluginsdk.AuthorizationPolicy{"viewer": {Permissions: []string{"console:read"}, PluginScope: []string{"console"}}}}); err != nil {
		t.Fatalf("replace current rbac state: %v", err)
	}

	if len(pool.execSQL) != 18 {
		t.Fatalf("expected 18 direct exec calls, got %d", len(pool.execSQL))
	}
	if pool.tx == nil {
		t.Fatal("expected rbac replacement to use a transaction")
	}
	if !pool.tx.committed || pool.tx.rolledBack {
		t.Fatalf("expected rbac transaction commit without rollback, got committed=%v rolledBack=%v", pool.tx.committed, pool.tx.rolledBack)
	}
	if len(pool.tx.execSQL) != 4 {
		t.Fatalf("expected 4 transactional exec calls for rbac replacement, got %d", len(pool.tx.execSQL))
	}
	for _, expected := range []string{"INSERT INTO event_log", "INSERT INTO jobs_pg", "INSERT INTO workflow_state", "INSERT INTO plugin_registry_pg", "INSERT INTO idempotency_keys_pg", "INSERT INTO audit_log", "INSERT INTO plugin_enabled_overlays_pg", "INSERT INTO plugin_configs_pg", "INSERT INTO plugin_status_snapshots_pg", "INSERT INTO sessions_pg", "INSERT INTO adapter_instances_pg", "INSERT INTO replay_operation_records_pg", "INSERT INTO rollout_operation_records_pg", "INSERT INTO rollout_heads_pg", "INSERT INTO alerts_pg", "INSERT INTO schedule_plans_pg", "UPDATE schedule_plans_pg", "INSERT INTO workflow_instances_pg", "INSERT INTO operator_identities_pg", "INSERT INTO rbac_snapshots_pg"} {
		matched := false
		for _, sql := range pool.execSQL {
			if strings.Contains(sql, expected) {
				matched = true
				break
			}
		}
		for _, sql := range pool.tx.execSQL {
			if strings.Contains(sql, expected) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("expected exec statements to include %q, got direct=%+v tx=%+v", expected, pool.execSQL, pool.tx.execSQL)
		}
	}
	if len(pool.execArgs[0]) != 6 || pool.execArgs[0][0] != event.EventID {
		t.Fatalf("unexpected event exec args %+v", pool.execArgs[0])
	}
	if len(pool.execArgs[1]) != 23 || pool.execArgs[1][0] != job.ID || pool.execArgs[1][1] != job.Type {
		t.Fatalf("unexpected job exec args %+v", pool.execArgs[1])
	}
	if len(pool.execArgs[4]) != 3 || pool.execArgs[4][0] != "idem-1" || pool.execArgs[4][1] != event.EventID {
		t.Fatalf("unexpected idempotency exec args %+v", pool.execArgs[4])
	}
	if len(pool.execArgs[5]) != 15 || pool.execArgs[5][1] != "plugin:enable" || pool.execArgs[5][5] != "rollout_prepared" || pool.execArgs[5][6] != "trace-pg-audit" || pool.execArgs[5][7] != "evt-pg-audit" || pool.execArgs[5][8] != "plugin-echo" || pool.execArgs[5][9] != "run-pg-audit" || pool.execArgs[5][10] != "session-operator-bearer-admin-user" || pool.execArgs[5][11] != "corr-pg-audit" || pool.execArgs[5][12] != "operator" || pool.execArgs[5][13] != "rollout_prepared" {
		t.Fatalf("unexpected audit exec args %+v", pool.execArgs[5])
	}
}

func TestPostgresStoreExecutionStateTransactionsAndReadbacks(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	startedAt := createdAt.Add(30 * time.Second)
	nextRunAt := startedAt.Add(45 * time.Second)
	finishedAt := nextRunAt.Add(15 * time.Second)
	workflow := NewWorkflow("wf-runtime-pg", WorkflowStep{Kind: WorkflowStepKindPersist, Name: "greeting", Value: "hello"}, WorkflowStep{Kind: WorkflowStepKindWaitEvent, Name: "wait", Value: "message.received"})
	workflow.State["greeting"] = "hello"
	workflow.CurrentIndex = 1
	workflow.WaitingFor = "message.received"
	workflowJSON, _ := json.Marshal(workflow)
	jobPayload, _ := json.Marshal(map[string]any{"prompt": "hello postgres"})
	metadataJSON := []byte(`{"message_text":"hello scheduler"}`)
	dueAt := createdAt.Add(5 * time.Minute)
	claimedAt := dueAt.Add(5 * time.Second)
	jobRow := []any{"job-pg-exec", "ai.chat", string(JobStatusDead), jobPayload, 1, 3, int64((45 * time.Second).Milliseconds()), "timeout", string(JobReasonCodeTimeout), createdAt, stdsql.NullTime{Time: startedAt, Valid: true}, stdsql.NullTime{Time: finishedAt, Valid: true}, stdsql.NullTime{Time: nextRunAt, Valid: true}, "runtime-local:worker-1", stdsql.NullTime{Time: startedAt, Valid: true}, stdsql.NullTime{Time: nextRunAt, Valid: true}, stdsql.NullTime{Time: startedAt, Valid: true}, true, "trace-job-pg-exec", "evt-job-pg-exec", "run-job-pg-exec", "corr-job-pg-exec"}
	alertRow := []any{"job.dead_letter:job-pg-exec", alertObjectTypeJob, "job-pg-exec", alertFailureTypeJobDeadLetter, finishedAt, finishedAt, "timeout", "trace-job-pg-exec", "evt-job-pg-exec", "run-job-pg-exec", "corr-job-pg-exec", finishedAt}
	scheduleRow := []any{"schedule-pg-exec", string(ScheduleKindDelay), "", int64((5 * time.Minute).Milliseconds()), stdsql.NullTime{}, stdsql.NullTime{Time: dueAt, Valid: true}, scheduleDueAtEvidencePersisted, "runtime-local:scheduler", stdsql.NullTime{Time: claimedAt, Valid: true}, "scheduler", "schedule.triggered", metadataJSON, createdAt, claimedAt}
	workflowRow := []any{"wf-runtime-pg", "plugin-workflow-demo", "trace-workflow-pg-exec", "evt-workflow-pg-exec-origin", "run-workflow-pg-exec", "corr-workflow-pg-exec", string(WorkflowRuntimeStatusWaitingEvent), workflowJSON, "evt-workflow-pg-exec-last", "message.received", createdAt, claimedAt}
	pool := &fakePostgresPool{
		commandTag: pgconn.NewCommandTag("UPDATE 1"),
		tx:         &fakePostgresTx{},
		queryRowFunc: func(_ context.Context, query string, _ ...any) pgx.Row {
			switch {
			case strings.Contains(query, "FROM jobs_pg"):
				return fakeRow{values: jobRow}
			case strings.Contains(query, "FROM schedule_plans_pg"):
				return fakeRow{values: scheduleRow}
			case strings.Contains(query, "FROM workflow_instances_pg"):
				return fakeRow{values: workflowRow}
			default:
				return fakeRow{err: errors.New("unexpected query")}
			}
		},
		queryFunc: func(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
			switch {
			case strings.Contains(query, "FROM jobs_pg"):
				return &fakeRows{rows: [][]any{jobRow}}, nil
			case strings.Contains(query, "FROM alerts_pg"):
				return &fakeRows{rows: [][]any{alertRow}}, nil
			case strings.Contains(query, "FROM schedule_plans_pg"):
				return &fakeRows{rows: [][]any{scheduleRow}}, nil
			case strings.Contains(query, "FROM workflow_instances_pg"):
				return &fakeRows{rows: [][]any{workflowRow}}, nil
			default:
				return nil, errors.New("unexpected query")
			}
		},
	}
	store := &PostgresStore{pool: pool}
	ctx := context.Background()
	deadJob := Job{ID: "job-pg-exec", Type: "ai.chat", Status: JobStatusDead, Payload: map[string]any{"prompt": "hello postgres"}, RetryCount: 1, MaxRetries: 3, Timeout: 45 * time.Second, LastError: "timeout", ReasonCode: JobReasonCodeTimeout, CreatedAt: createdAt, StartedAt: &startedAt, FinishedAt: &finishedAt, NextRunAt: &nextRunAt, WorkerID: "runtime-local:worker-1", LeaseAcquiredAt: &startedAt, LeaseExpiresAt: &nextRunAt, HeartbeatAt: &startedAt, DeadLetter: true, TraceID: "trace-job-pg-exec", EventID: "evt-job-pg-exec", RunID: "run-job-pg-exec", Correlation: "corr-job-pg-exec"}
	if err := store.PersistJobDeadLetter(ctx, deadJob, jobDeadLetterAlert(deadJob)); err != nil {
		t.Fatalf("persist dead-letter job: %v", err)
	}
	if err := store.PersistJobDeadLetterRetry(ctx, reviveDeadLetterJob(deadJob), deadLetterAlertID(deadJob.ID)); err != nil {
		t.Fatalf("persist dead-letter retry: %v", err)
	}
	if pool.tx == nil || !pool.tx.committed || pool.tx.rolledBack {
		t.Fatalf("expected transactional dead-letter writes to commit cleanly, got %+v", pool.tx)
	}
	for _, expected := range []string{"INSERT INTO jobs_pg", "INSERT INTO alerts_pg", "DELETE FROM alerts_pg"} {
		matched := false
		for _, sql := range pool.tx.execSQL {
			if strings.Contains(sql, expected) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("expected dead-letter transaction SQL to include %q, got %+v", expected, pool.tx.execSQL)
		}
	}
	loadedJob, err := store.LoadJob(ctx, "job-pg-exec")
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if loadedJob.ID != "job-pg-exec" || loadedJob.Status != JobStatusDead || loadedJob.ReasonCode != JobReasonCodeTimeout || loadedJob.WorkerID != "runtime-local:worker-1" {
		t.Fatalf("unexpected loaded job %+v", loadedJob)
	}
	if loadedJob.Payload["prompt"] != "hello postgres" {
		t.Fatalf("expected loaded job payload, got %+v", loadedJob.Payload)
	}
	jobs, err := store.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "job-pg-exec" {
		t.Fatalf("list jobs: jobs=%+v err=%v", jobs, err)
	}
	alerts, err := store.ListAlerts(ctx)
	if err != nil || len(alerts) != 1 || alerts[0].ObjectID != "job-pg-exec" || alerts[0].FailureType != alertFailureTypeJobDeadLetter {
		t.Fatalf("list alerts: alerts=%+v err=%v", alerts, err)
	}
	loadedSchedule, err := store.LoadSchedulePlan(ctx, "schedule-pg-exec")
	if err != nil {
		t.Fatalf("load schedule plan: %v", err)
	}
	if loadedSchedule.Plan.ID != "schedule-pg-exec" || loadedSchedule.DueAt == nil || !loadedSchedule.DueAt.Equal(dueAt) || loadedSchedule.ClaimOwner != "runtime-local:scheduler" {
		t.Fatalf("unexpected loaded schedule %+v", loadedSchedule)
	}
	plans, err := store.ListSchedulePlans(ctx)
	if err != nil || len(plans) != 1 || plans[0].Plan.ID != "schedule-pg-exec" {
		t.Fatalf("list schedule plans: plans=%+v err=%v", plans, err)
	}
	loadedWorkflow, err := store.LoadWorkflowInstance(ctx, "wf-runtime-pg")
	if err != nil {
		t.Fatalf("load workflow instance: %v", err)
	}
	if loadedWorkflow.WorkflowID != "wf-runtime-pg" || loadedWorkflow.PluginID != "plugin-workflow-demo" || loadedWorkflow.Status != WorkflowRuntimeStatusWaitingEvent {
		t.Fatalf("unexpected loaded workflow %+v", loadedWorkflow)
	}
	if loadedWorkflow.Workflow.State["greeting"] != "hello" || loadedWorkflow.LastEventType != "message.received" {
		t.Fatalf("expected workflow runtime state to round-trip, got %+v", loadedWorkflow)
	}
	instances, err := store.ListWorkflowInstances(ctx)
	if err != nil || len(instances) != 1 || instances[0].WorkflowID != "wf-runtime-pg" {
		t.Fatalf("list workflow instances: instances=%+v err=%v", instances, err)
	}
}

func TestPostgresStoreReplaceCurrentRBACStateRollsBackOnIdentityFailure(t *testing.T) {
	t.Parallel()

	pool := &fakePostgresPool{tx: &fakePostgresTx{execErr: errors.New("write failed")}}
	store := &PostgresStore{pool: pool}
	err := store.ReplaceCurrentRBACState(context.Background(), []OperatorIdentityState{{ActorID: "viewer-user", Roles: []string{"viewer"}}}, RBACSnapshotState{SnapshotKey: CurrentRBACSnapshotKey})
	if err == nil || !strings.Contains(err.Error(), "clear operator identities") && !strings.Contains(err.Error(), "upsert operator identity") {
		t.Fatalf("expected transactional rbac replace error, got %v", err)
	}
	if pool.tx == nil || !pool.tx.rolledBack || pool.tx.committed {
		t.Fatalf("expected rollback without commit on rbac failure, got tx=%+v", pool.tx)
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

func TestPostgresStoreControlStateReadbacks(t *testing.T) {
	t.Parallel()

	updatedAt := time.Date(2026, 4, 6, 11, 30, 0, 0, time.UTC)
	recoveredAt := updatedAt.Add(2 * time.Minute)
	pool := &fakePostgresPool{queryRowFunc: func(_ context.Context, query string, _ ...any) pgx.Row {
		switch {
		case strings.Contains(query, "FROM rollout_heads_pg"):
			return fakeRow{values: []any{"plugin-echo", []byte(`{"Version":"0.1.0","APIVersion":"v0","Mode":"subprocess"}`), []byte(`{"Version":"0.1.0","APIVersion":"v0","Mode":"subprocess"}`), []byte(`{"Version":"0.2.0-candidate","APIVersion":"v0","Mode":"subprocess"}`), "prepared", "prepared", "", "rollout-op-prepare-1", updatedAt}}
		case strings.Contains(query, "FROM sessions_pg"):
			return fakeRow{values: []any{OperatorBearerSessionID("viewer-user"), OperatorAuthSessionPluginID, []byte(`{"actor_id":"viewer-user","token_id":"console-viewer","auth_method":"bearer"}`)}}
		case strings.Contains(query, "FROM plugin_enabled_overlays_pg"):
			return fakeRow{values: []any{"plugin-echo", false, updatedAt}}
		case strings.Contains(query, "FROM plugin_configs_pg"):
			return fakeRow{values: []any{"plugin-echo", []byte(`{"prefix":"persisted: "}`), updatedAt}}
		case strings.Contains(query, "FROM plugin_status_snapshots_pg"):
			return fakeRow{values: []any{"plugin-echo", "event", true, "", updatedAt, stdsql.NullTime{Time: recoveredAt, Valid: true}, 1, 0, updatedAt}}
		case strings.Contains(query, "FROM adapter_instances_pg"):
			return fakeRow{values: []any{"adapter-onebot-demo", "onebot", "onebot", []byte(`{"mode":"demo-ingress"}`), "registered", "ready", true, updatedAt}}
		case strings.Contains(query, "FROM operator_identities_pg"):
			return fakeRow{values: []any{"viewer-user", []byte(`[
"viewer"]`), updatedAt}}
		case strings.Contains(query, "FROM rbac_snapshots_pg"):
			return fakeRow{values: []any{CurrentRBACSnapshotKey, "console:read", []byte(`{"viewer":{"permissions":["console:read"],"plugin_scope":["console"]}}`), updatedAt}}
		default:
			return fakeRow{err: errors.New("unexpected query")}
		}
	}}
	store := &PostgresStore{pool: pool}
	ctx := context.Background()

	enabled, err := store.LoadPluginEnabledState(ctx, "plugin-echo")
	if err != nil || enabled.PluginID != "plugin-echo" || enabled.Enabled {
		t.Fatalf("load plugin enabled state: state=%+v err=%v", enabled, err)
	}
	config, err := store.LoadPluginConfig(ctx, "plugin-echo")
	if err != nil || config.PluginID != "plugin-echo" || string(config.RawConfig) != `{"prefix":"persisted: "}` {
		t.Fatalf("load plugin config: state=%+v err=%v", config, err)
	}
	snapshot, err := store.LoadPluginStatusSnapshot(ctx, "plugin-echo")
	if err != nil || snapshot.PluginID != "plugin-echo" || !snapshot.LastDispatchSuccess || snapshot.LastRecoveredAt == nil || !snapshot.LastRecoveredAt.Equal(recoveredAt) {
		t.Fatalf("load plugin status snapshot: snapshot=%+v err=%v", snapshot, err)
	}
	adapter, err := store.LoadAdapterInstance(ctx, "adapter-onebot-demo")
	if err != nil || adapter.InstanceID != "adapter-onebot-demo" || adapter.Adapter != "onebot" || !adapter.Online {
		t.Fatalf("load adapter instance: state=%+v err=%v", adapter, err)
	}
	session, err := store.LoadSession(ctx, OperatorBearerSessionID("viewer-user"))
	if err != nil || session.SessionID != OperatorBearerSessionID("viewer-user") || session.PluginID != OperatorAuthSessionPluginID || session.State["actor_id"] != "viewer-user" {
		t.Fatalf("load session: state=%+v err=%v", session, err)
	}
	identity, err := store.LoadOperatorIdentity(ctx, "viewer-user")
	if err != nil || identity.ActorID != "viewer-user" || len(identity.Roles) != 1 || identity.Roles[0] != "viewer" {
		t.Fatalf("load operator identity: state=%+v err=%v", identity, err)
	}
	rbac, err := store.LoadRBACSnapshot(ctx, CurrentRBACSnapshotKey)
	if err != nil || rbac.ConsoleReadPermission != "console:read" || rbac.Policies["viewer"].Permissions[0] != "console:read" {
		t.Fatalf("load rbac snapshot: state=%+v err=%v", rbac, err)
	}
	head, err := store.LoadRolloutHead(ctx, "plugin-echo")
	if err != nil || head.PluginID != "plugin-echo" || head.Stable.Version != "0.1.0" || head.Active.Version != "0.1.0" || head.Candidate == nil || head.Candidate.Version != "0.2.0-candidate" || head.LastOperationID != "rollout-op-prepare-1" {
		t.Fatalf("load rollout head: state=%+v err=%v", head, err)
	}
}

func TestPostgresStoreControlStateListReadbacks(t *testing.T) {
	t.Parallel()

	updatedAt := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	occurredAt := updatedAt.Add(-5 * time.Minute)
	pool := &fakePostgresPool{queryFunc: func(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
		switch {
		case strings.Contains(query, "FROM rollout_heads_pg"):
			return &fakeRows{rows: [][]any{{"plugin-echo", []byte(`{"Version":"0.1.0","APIVersion":"v0","Mode":"subprocess"}`), []byte(`{"Version":"0.1.0","APIVersion":"v0","Mode":"subprocess"}`), []byte(`{"Version":"0.2.0-candidate","APIVersion":"v0","Mode":"subprocess"}`), "prepared", "prepared", "", "rollout-op-1", updatedAt}}}, nil
		case strings.Contains(query, "FROM sessions_pg"):
			return &fakeRows{rows: [][]any{{OperatorBearerSessionID("viewer-user"), OperatorAuthSessionPluginID, []byte(`{"actor_id":"viewer-user","token_id":"console-viewer","auth_method":"bearer"}`)}}}, nil
		case strings.Contains(query, "FROM plugin_enabled_overlays_pg"):
			return &fakeRows{rows: [][]any{{"plugin-echo", false, updatedAt}}}, nil
		case strings.Contains(query, "FROM plugin_configs_pg"):
			return &fakeRows{rows: [][]any{{"plugin-echo", []byte(`{"prefix":"persisted: "}`), updatedAt}}}, nil
		case strings.Contains(query, "FROM plugin_status_snapshots_pg"):
			return &fakeRows{rows: [][]any{{"plugin-echo", "event", false, "timeout", occurredAt, stdsql.NullTime{}, 0, 1, updatedAt}}}, nil
		case strings.Contains(query, "FROM adapter_instances_pg"):
			return &fakeRows{rows: [][]any{{"adapter-onebot-demo", "onebot", "onebot", []byte(`{"mode":"demo-ingress"}`), "registered", "ready", true, updatedAt}}}, nil
		case strings.Contains(query, "FROM operator_identities_pg"):
			return &fakeRows{rows: [][]any{{"viewer-user", []byte(`[
"viewer"]`), updatedAt}}}, nil
		case strings.Contains(query, "FROM replay_operation_records_pg"):
			return &fakeRows{rows: [][]any{{"replay-op-1", "evt-source-1", "replay-evt-1", "succeeded", "replay_dispatched", occurredAt, updatedAt}}}, nil
		case strings.Contains(query, "FROM rollout_operation_records_pg"):
			return &fakeRows{rows: [][]any{{"rollout-op-1", "plugin-echo", "prepare", "0.1.0", "0.2.0-candidate", "prepared", "", occurredAt, updatedAt}}}, nil
		case strings.Contains(query, "FROM audit_log"):
			return &fakeRows{rows: [][]any{{"admin-user", "plugin:enable", "enable", "plugin-echo", true, stdsql.NullString{String: "rollout_prepared", Valid: true}, "trace-1", "evt-1", "plugin-echo", "run-1", "session-operator-bearer-admin-user", "corr-1", "operator", "rollout_prepared", updatedAt}}}, nil
		default:
			return nil, errors.New("unexpected query")
		}
	}}
	store := &PostgresStore{pool: pool}
	ctx := context.Background()

	sessions, err := store.ListSessions(ctx)
	if err != nil || len(sessions) != 1 || sessions[0].SessionID != OperatorBearerSessionID("viewer-user") || sessions[0].PluginID != OperatorAuthSessionPluginID || sessions[0].State["token_id"] != "console-viewer" {
		t.Fatalf("list sessions: states=%+v err=%v", sessions, err)
	}
	enabledStates, err := store.ListPluginEnabledStates(ctx)
	if err != nil || len(enabledStates) != 1 || enabledStates[0].PluginID != "plugin-echo" {
		t.Fatalf("list plugin enabled states: states=%+v err=%v", enabledStates, err)
	}
	configs, err := store.ListPluginConfigs(ctx)
	if err != nil || len(configs) != 1 || string(configs[0].RawConfig) != `{"prefix":"persisted: "}` {
		t.Fatalf("list plugin configs: states=%+v err=%v", configs, err)
	}
	snapshots, err := store.ListPluginStatusSnapshots(ctx)
	if err != nil || len(snapshots) != 1 || snapshots[0].CurrentFailureStreak != 1 {
		t.Fatalf("list plugin status snapshots: states=%+v err=%v", snapshots, err)
	}
	adapters, err := store.ListAdapterInstances(ctx)
	if err != nil || len(adapters) != 1 || adapters[0].InstanceID != "adapter-onebot-demo" {
		t.Fatalf("list adapter instances: states=%+v err=%v", adapters, err)
	}
	identities, err := store.ListOperatorIdentities(ctx)
	if err != nil || len(identities) != 1 || identities[0].ActorID != "viewer-user" {
		t.Fatalf("list operator identities: states=%+v err=%v", identities, err)
	}
	replayRecords, err := store.ListReplayOperationRecords(ctx)
	if err != nil || len(replayRecords) != 1 || replayRecords[0].ReplayID != "replay-op-1" {
		t.Fatalf("list replay operation records: states=%+v err=%v", replayRecords, err)
	}
	rolloutRecords, err := store.ListRolloutOperationRecords(ctx)
	if err != nil || len(rolloutRecords) != 1 || rolloutRecords[0].OperationID != "rollout-op-1" {
		t.Fatalf("list rollout operation records: states=%+v err=%v", rolloutRecords, err)
	}
	rolloutHeads, err := store.ListRolloutHeads(ctx)
	if err != nil || len(rolloutHeads) != 1 || rolloutHeads[0].PluginID != "plugin-echo" || rolloutHeads[0].Candidate == nil || rolloutHeads[0].Candidate.Version != "0.2.0-candidate" {
		t.Fatalf("list rollout heads: states=%+v err=%v", rolloutHeads, err)
	}
	audits, err := store.ListAudits(ctx)
	if err != nil || len(audits) != 1 || audits[0].Reason != "rollout_prepared" || audits[0].SessionID != "session-operator-bearer-admin-user" {
		t.Fatalf("list audits: states=%+v err=%v", audits, err)
	}
	if len(store.AuditEntries()) != 1 || store.AuditEntries()[0].Action != "enable" {
		t.Fatalf("audit entries helper: %+v", store.AuditEntries())
	}
}

func TestPostgresStoreCountsReadsSmokeTables(t *testing.T) {
	t.Parallel()

	pool := &fakePostgresPool{queryRowFunc: func(_ context.Context, query string, _ ...any) pgx.Row {
		switch {
		case strings.Contains(query, "FROM event_log"):
			return fakeRow{values: []any{3}}
		case strings.Contains(query, "FROM jobs_pg"):
			return fakeRow{values: []any{2}}
		case strings.Contains(query, "FROM alerts_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM schedule_plans_pg"):
			return fakeRow{values: []any{4}}
		case strings.Contains(query, "FROM workflow_instances_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM plugin_registry_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM plugin_enabled_overlays_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM plugin_configs_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM plugin_status_snapshots_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM rollout_heads_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM adapter_instances_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM sessions_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM idempotency_keys_pg"):
			return fakeRow{values: []any{5}}
		case strings.Contains(query, "FROM operator_identities_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM rbac_snapshots_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM replay_operation_records_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM rollout_operation_records_pg"):
			return fakeRow{values: []any{1}}
		case strings.Contains(query, "FROM audit_log"):
			return fakeRow{values: []any{1}}
		default:
			return fakeRow{err: errors.New("unexpected query")}
		}
	}}
	store := &PostgresStore{pool: pool}
	counts, err := store.Counts(context.Background())
	if err != nil {
		t.Fatalf("count postgres smoke tables: %v", err)
	}
	if counts["event_journal"] != 3 || counts["jobs"] != 2 || counts["alerts"] != 1 || counts["schedule_plans"] != 4 || counts["workflow_instances"] != 1 || counts["idempotency_keys"] != 5 || counts["plugin_configs"] != 1 || counts["audit_log"] != 1 {
		t.Fatalf("unexpected counts %+v", counts)
	}
	if len(pool.querySQL) != 18 {
		t.Fatalf("expected 18 count queries, got %+v", pool.querySQL)
	}
}

func TestPostgresScanRolloutHeadRoundTrip(t *testing.T) {
	t.Parallel()

	updatedAt := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	state, err := scanPostgresRolloutHead(fakeRow{values: []any{
		"plugin-echo",
		[]byte(`{"Version":"0.1.0","APIVersion":"v0","Mode":"subprocess"}`),
		[]byte(`{"Version":"0.1.0","APIVersion":"v0","Mode":"subprocess"}`),
		[]byte(`{"Version":"0.2.0-candidate","APIVersion":"v0","Mode":"subprocess"}`),
		"prepared",
		"prepared",
		"",
		"rollout-op-prepare-1",
		updatedAt,
	}})
	if err != nil {
		t.Fatalf("scan rollout head: %v", err)
	}
	if state.PluginID != "plugin-echo" || state.Stable.Version != "0.1.0" || state.Active.Version != "0.1.0" || state.Candidate == nil || state.Candidate.Version != "0.2.0-candidate" || state.LastOperationID != "rollout-op-prepare-1" || !state.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("unexpected scanned rollout head %+v", state)
	}
}

func TestPostgresStoreInitAndCloseDelegateToPool(t *testing.T) {
	t.Parallel()

	pool := &fakePostgresPool{}
	store := &PostgresStore{pool: pool}
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	if len(pool.execSQL) != 2 || !strings.Contains(pool.execSQL[0], "CREATE TABLE IF NOT EXISTS event_log") || !strings.Contains(pool.execSQL[1], "ALTER TABLE audit_log ADD COLUMN session_id") {
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
