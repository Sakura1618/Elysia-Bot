package runtimecore

import (
	"context"
	"database/sql"
	"encoding/json"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

var errStateStoreNoRows = sql.ErrNoRows

func ErrStateStoreNoRows() error {
	return errStateStoreNoRows
}

type PluginManifestStateStore interface {
	SavePluginManifest(context.Context, pluginsdk.PluginManifest) error
	LoadPluginManifest(context.Context, string) (pluginsdk.PluginManifest, error)
}

type PluginEnabledStateStore interface {
	SavePluginEnabledState(context.Context, string, bool) error
	LoadPluginEnabledState(context.Context, string) (PluginEnabledState, error)
	ListPluginEnabledStates(context.Context) ([]PluginEnabledState, error)
}

type PluginConfigStateStore interface {
	SavePluginConfig(context.Context, string, json.RawMessage) error
	LoadPluginConfig(context.Context, string) (PluginConfigState, error)
	ListPluginConfigs(context.Context) ([]PluginConfigState, error)
}

type PluginStatusSnapshotStore interface {
	SavePluginStatusSnapshot(context.Context, DispatchResult) error
	LoadPluginStatusSnapshot(context.Context, string) (PluginStatusSnapshot, error)
	ListPluginStatusSnapshots(context.Context) ([]PluginStatusSnapshot, error)
	RecordDispatchResult(DispatchResult) error
}

type SessionStateStore interface {
	SaveSession(context.Context, SessionState) error
	LoadSession(context.Context, string) (SessionState, error)
	ListSessions(context.Context) ([]SessionState, error)
}

type AdapterInstanceStateStore interface {
	SaveAdapterInstance(context.Context, AdapterInstanceState) error
	LoadAdapterInstance(context.Context, string) (AdapterInstanceState, error)
	ListAdapterInstances(context.Context) ([]AdapterInstanceState, error)
}

type RBACStateStore interface {
	ReplaceCurrentRBACState(context.Context, []OperatorIdentityState, RBACSnapshotState) error
	LoadRBACSnapshot(context.Context, string) (RBACSnapshotState, error)
	ListOperatorIdentities(context.Context) ([]OperatorIdentityState, error)
	LoadOperatorIdentity(context.Context, string) (OperatorIdentityState, error)
}

type ReplayOperationStateStore interface {
	replayOperationRecorder
	ListReplayOperationRecords(context.Context) ([]ReplayOperationRecord, error)
}

type RolloutHeadStateStore interface {
	SaveRolloutHead(context.Context, RolloutHeadState) error
	LoadRolloutHead(context.Context, string) (RolloutHeadState, error)
	ListRolloutHeads(context.Context) ([]RolloutHeadState, error)
}

type RolloutStateStore interface {
	RolloutHeadStateStore
	SaveRolloutOperationRecord(context.Context, RolloutOperationRecord) error
	ListRolloutOperationRecords(context.Context) ([]RolloutOperationRecord, error)
}

type RuntimeStateStore interface {
	EventJournalReader
	PluginManifestStateStore
	PluginEnabledStateStore
	PluginConfigStateStore
	PluginStatusSnapshotStore
	SessionStateStore
	AdapterInstanceStateStore
	RBACStateStore
	ReplayOperationStateStore
	RolloutHeadStateStore
	jobQueueStore
	AlertSink
	deadLetterTransactionalAlertSink
	deadLetterRetryTransactionalAlertSink
	alertResolver
	schedulerStore
	workflowRuntimeStore
	AuditRecorder
	DispatchResultRecorder
	RecordEvent(context.Context, eventmodel.Event) error
	SaveIdempotencyKey(context.Context, string, string) error
	HasIdempotencyKey(context.Context, string) (bool, error)
	Counts(context.Context) (map[string]int, error)
	ListAlerts(context.Context) ([]AlertRecord, error)
	SaveAudit(context.Context, pluginsdk.AuditEntry) error
	ListAudits(context.Context) ([]pluginsdk.AuditEntry, error)
	AuditEntries() []pluginsdk.AuditEntry
}
