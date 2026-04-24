package runtimecore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type ConsoleAPI struct {
	runtime          *InMemoryRuntime
	queue            *JobQueue
	jobs             consoleJobReader
	alerts           consoleAlertReader
	replayOps        consoleReplayOperationReader
	rolloutOps       consoleRolloutOperationReader
	rolloutHeads     consoleRolloutHeadReader
	schedules        consoleScheduleReader
	workflows        consoleWorkflowReader
	adapterInstances consoleAdapterInstanceReader
	pluginSnapshots  consolePluginSnapshotReader
	pluginEnabled    consolePluginEnabledStateReader
	pluginConfigs    consolePluginConfigReader
	pluginConfigMeta map[string]ConsolePluginConfigBinding
	recovery         consoleRecoverySource
	config           Config
	logs             []string
	audits           AuditLogReader
	meta             map[string]any
	readAuthorizer   ConsoleReadRequestAuthorizer
	authorizerSource CurrentAuthorizerProvider
}

type ConsolePluginPublish struct {
	SourceType          string `json:"sourceType"`
	SourceURI           string `json:"sourceUri"`
	RuntimeVersionRange string `json:"runtimeVersionRange"`
}

type ConsolePlugin struct {
	ID                       string                `json:"id"`
	Name                     string                `json:"name"`
	Version                  string                `json:"version"`
	APIVersion               string                `json:"apiVersion"`
	Mode                     string                `json:"mode"`
	Permissions              []string              `json:"permissions"`
	ConfigSchema             map[string]any        `json:"configSchema,omitempty"`
	Config                   map[string]any        `json:"config,omitempty"`
	Entry                    pluginsdk.PluginEntry `json:"entry"`
	Publish                  *ConsolePluginPublish `json:"publish,omitempty"`
	Enabled                  bool                  `json:"enabled"`
	EnabledStateSource       string                `json:"enabledStateSource,omitempty"`
	EnabledStatePersisted    bool                  `json:"enabledStatePersisted"`
	EnabledStateUpdatedAt    *time.Time            `json:"enabledStateUpdatedAt,omitempty"`
	ConfigStateKind          string                `json:"configStateKind,omitempty"`
	ConfigSource             string                `json:"configSource,omitempty"`
	ConfigPersisted          bool                  `json:"configPersisted"`
	ConfigUpdatedAt          *time.Time            `json:"configUpdatedAt,omitempty"`
	StatusSource             string                `json:"statusSource,omitempty"`
	StatusEvidence           string                `json:"statusEvidence,omitempty"`
	StatusSummary            string                `json:"statusSummary,omitempty"`
	RuntimeStateLive         bool                  `json:"runtimeStateLive"`
	StatusPersisted          bool                  `json:"statusPersisted"`
	LastDispatchKind         string                `json:"lastDispatchKind,omitempty"`
	LastDispatchSuccess      *bool                 `json:"lastDispatchSuccess,omitempty"`
	LastDispatchError        string                `json:"lastDispatchError,omitempty"`
	LastDispatchAt           *time.Time            `json:"lastDispatchAt,omitempty"`
	LastRecoveredAt          *time.Time            `json:"lastRecoveredAt,omitempty"`
	LastRecoveryFailureCount int                   `json:"lastRecoveryFailureCount,omitempty"`
	CurrentFailureStreak     int                   `json:"currentFailureStreak,omitempty"`
	StatusLevel              string                `json:"statusLevel,omitempty"`
	StatusRecovery           string                `json:"statusRecovery,omitempty"`
	StatusStaleness          string                `json:"statusStaleness,omitempty"`
}

type ConsolePluginConfigBinding struct {
	StateKind     string
	ProjectConfig func(raw json.RawMessage) (map[string]any, error)
}

type ConsoleAdapterInstance struct {
	ID             string         `json:"id"`
	Adapter        string         `json:"adapter"`
	Source         string         `json:"source"`
	Config         map[string]any `json:"config,omitempty"`
	Status         string         `json:"status,omitempty"`
	Health         string         `json:"health,omitempty"`
	Online         bool           `json:"online"`
	StatusSource   string         `json:"statusSource,omitempty"`
	ConfigSource   string         `json:"configSource,omitempty"`
	StatePersisted bool           `json:"statePersisted"`
	UpdatedAt      *time.Time     `json:"updatedAt,omitempty"`
	Summary        string         `json:"summary,omitempty"`
}

type consoleJobReader interface {
	ListJobs() ([]Job, error)
}

type consoleAlertReader interface {
	ListAlerts() ([]AlertRecord, error)
}

type consoleReplayOperationReader interface {
	ListReplayOperationRecords() ([]ReplayOperationRecord, error)
}

type consoleRolloutOperationReader interface {
	ListRolloutOperationRecords() ([]RolloutOperationRecord, error)
}

type consoleRolloutHeadReader interface {
	ListRolloutHeads() ([]RolloutHeadState, error)
}

type ConsoleJob struct {
	ID                      string                  `json:"id"`
	Type                    string                  `json:"type"`
	TraceID                 string                  `json:"traceId,omitempty"`
	EventID                 string                  `json:"eventId,omitempty"`
	RunID                   string                  `json:"runId,omitempty"`
	Status                  JobStatus               `json:"status"`
	Payload                 map[string]any          `json:"payload,omitempty"`
	RetryCount              int                     `json:"retryCount"`
	MaxRetries              int                     `json:"maxRetries"`
	Timeout                 int64                   `json:"timeout"`
	LastError               string                  `json:"lastError"`
	CreatedAt               time.Time               `json:"createdAt"`
	StartedAt               *time.Time              `json:"startedAt"`
	FinishedAt              *time.Time              `json:"finishedAt"`
	NextRunAt               *time.Time              `json:"nextRunAt"`
	WorkerID                string                  `json:"workerId,omitempty"`
	LeaseAcquiredAt         *time.Time              `json:"leaseAcquiredAt,omitempty"`
	LeaseExpiresAt          *time.Time              `json:"leaseExpiresAt,omitempty"`
	HeartbeatAt             *time.Time              `json:"heartbeatAt,omitempty"`
	ReasonCode              JobReasonCode           `json:"reasonCode,omitempty"`
	DeadLetter              bool                    `json:"deadLetter"`
	Correlation             string                  `json:"correlation"`
	TargetPluginID          string                  `json:"targetPluginId,omitempty"`
	DispatchMetadataPresent bool                    `json:"dispatchMetadataPresent"`
	DispatchContractPresent bool                    `json:"dispatchContractPresent"`
	QueueContractComplete   bool                    `json:"queueContractComplete"`
	DispatchReady           bool                    `json:"dispatchReady"`
	QueueStateSummary       string                  `json:"queueStateSummary,omitempty"`
	DispatchSummary         string                  `json:"dispatchSummary,omitempty"`
	QueueContractSummary    string                  `json:"queueContractSummary,omitempty"`
	LeaseSummary            string                  `json:"leaseSummary,omitempty"`
	DispatchPermission      string                  `json:"dispatchPermission,omitempty"`
	DispatchActor           string                  `json:"dispatchActor,omitempty"`
	DispatchRBAC            *ConsoleRBACDeclaration `json:"dispatchRbac,omitempty"`
	ReplyHandlePresent      bool                    `json:"replyHandlePresent"`
	ReplyHandleCapability   string                  `json:"replyHandleCapability,omitempty"`
	ReplyContractPresent    bool                    `json:"replyContractPresent"`
	ReplySummary            string                  `json:"replySummary,omitempty"`
	SessionIDPresent        bool                    `json:"sessionIDPresent"`
	ReplyTargetPresent      bool                    `json:"replyTargetPresent"`
	RecoverySummary         string                  `json:"recoverySummary,omitempty"`
}

type ConsoleRBACDeclaration struct {
	Actor                    string   `json:"actor,omitempty"`
	Permission               string   `json:"permission,omitempty"`
	TargetPluginID           string   `json:"targetPluginId,omitempty"`
	DispatchKind             string   `json:"dispatchKind,omitempty"`
	RuntimeAuthorizerEnabled bool     `json:"runtimeAuthorizerEnabled"`
	RuntimeAuthorizerScope   string   `json:"runtimeAuthorizerScope,omitempty"`
	ManifestGateEnabled      bool     `json:"manifestGateEnabled"`
	ManifestGateScope        string   `json:"manifestGateScope,omitempty"`
	JobTargetFilterEnabled   bool     `json:"jobTargetFilterEnabled"`
	Facts                    []string `json:"facts,omitempty"`
	Summary                  string   `json:"summary,omitempty"`
}

type consoleRecoverySource interface {
	LastRecoverySnapshot() RecoverySnapshot
}

type ConsoleRecovery struct {
	RecoveredAt        *time.Time     `json:"recoveredAt,omitempty"`
	TotalJobs          int            `json:"totalJobs"`
	RecoveredJobs      int            `json:"recoveredJobs"`
	RecoveredRunning   int            `json:"recoveredRunning"`
	RetriedJobs        int            `json:"retriedJobs"`
	DeadJobs           int            `json:"deadJobs"`
	StatusCounts       map[string]int `json:"statusCounts,omitempty"`
	TotalSchedules     int            `json:"totalSchedules"`
	RecoveredSchedules int            `json:"recoveredSchedules"`
	InvalidSchedules   int            `json:"invalidSchedules"`
	ScheduleKinds      map[string]int `json:"scheduleKinds,omitempty"`
	Summary            string         `json:"summary,omitempty"`
}

type ConsoleReplayOperation struct {
	ReplayID      string     `json:"replayId"`
	SourceEventID string     `json:"sourceEventId"`
	ReplayEventID string     `json:"replayEventId"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason,omitempty"`
	OccurredAt    *time.Time `json:"occurredAt,omitempty"`
	UpdatedAt     *time.Time `json:"updatedAt,omitempty"`
	StateSource   string     `json:"stateSource,omitempty"`
	Persisted     bool       `json:"persisted"`
	Summary       string     `json:"summary,omitempty"`
}

type ConsoleRolloutOperation struct {
	OperationID      string     `json:"operationId"`
	PluginID         string     `json:"pluginId"`
	Action           string     `json:"action"`
	CurrentVersion   string     `json:"currentVersion,omitempty"`
	CandidateVersion string     `json:"candidateVersion,omitempty"`
	Status           string     `json:"status"`
	Reason           string     `json:"reason,omitempty"`
	OccurredAt       *time.Time `json:"occurredAt,omitempty"`
	UpdatedAt        *time.Time `json:"updatedAt,omitempty"`
	StateSource      string     `json:"stateSource,omitempty"`
	Persisted        bool       `json:"persisted"`
	Summary          string     `json:"summary,omitempty"`
}

type ConsoleRolloutSnapshot struct {
	Version    string `json:"version,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

type ConsoleRolloutHead struct {
	PluginID        string                  `json:"pluginId"`
	Stable          ConsoleRolloutSnapshot  `json:"stable"`
	Active          ConsoleRolloutSnapshot  `json:"active"`
	Candidate       *ConsoleRolloutSnapshot `json:"candidate,omitempty"`
	Phase           string                  `json:"phase"`
	Status          string                  `json:"status"`
	Reason          string                  `json:"reason,omitempty"`
	LastOperationID string                  `json:"lastOperationId,omitempty"`
	UpdatedAt       *time.Time              `json:"updatedAt,omitempty"`
	StateSource     string                  `json:"stateSource,omitempty"`
	Persisted       bool                    `json:"persisted"`
	Summary         string                  `json:"summary,omitempty"`
}

type ConsoleObservability struct {
	JobDispatchReady      int                 `json:"jobDispatchReady"`
	ScheduleDueReady      int                 `json:"scheduleDueReady"`
	JobStateSource        string              `json:"jobStateSource,omitempty"`
	ScheduleStateSource   string              `json:"scheduleStateSource,omitempty"`
	LogStateSource        string              `json:"logStateSource,omitempty"`
	TraceStateSource      string              `json:"traceStateSource,omitempty"`
	MetricsStateSource    string              `json:"metricsStateSource,omitempty"`
	VerificationEndpoints []string            `json:"verificationEndpoints,omitempty"`
	Alertability          ConsoleAlertability `json:"alertability"`
	Summary               string              `json:"summary,omitempty"`
}

type ConsoleAlertability struct {
	Baseline       []ConsoleAlertabilityRule    `json:"baseline,omitempty"`
	ActiveFindings []ConsoleAlertabilityFinding `json:"activeFindings,omitempty"`
	Summary        string                       `json:"summary,omitempty"`
}

type ConsoleAlertabilityRule struct {
	ID                    string   `json:"id"`
	Severity              string   `json:"severity"`
	Signal                string   `json:"signal"`
	Condition             string   `json:"condition"`
	VerificationEndpoints []string `json:"verificationEndpoints,omitempty"`
	Summary               string   `json:"summary,omitempty"`
}

type ConsoleAlertabilityFinding struct {
	RuleID   string   `json:"ruleId"`
	Severity string   `json:"severity"`
	Status   string   `json:"status"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
}

type consoleScheduleReader interface {
	ListSchedulePlans() ([]ConsoleSchedule, error)
}

type consoleWorkflowReader interface {
	ListWorkflowInstances() ([]WorkflowInstanceState, error)
}

type consoleAdapterInstanceReader interface {
	ListAdapterInstances() ([]AdapterInstanceState, error)
}

type consolePluginSnapshotReader interface {
	ListPluginStatusSnapshots() ([]PluginStatusSnapshot, error)
}

type consolePluginEnabledStateReader interface {
	ListPluginEnabledStates() ([]PluginEnabledState, error)
}

type consolePluginConfigReader interface {
	ListPluginConfigs() ([]PluginConfigState, error)
}

type ConsoleSchedule struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Source          string     `json:"source"`
	EventType       string     `json:"eventType"`
	CronExpr        string     `json:"cronExpr"`
	DelayMs         int64      `json:"delayMs"`
	ExecuteAt       *time.Time `json:"executeAt"`
	DueAt           *time.Time `json:"dueAt"`
	DueAtSource     string     `json:"dueAtSource,omitempty"`
	DueAtEvidence   string     `json:"dueAtEvidence,omitempty"`
	DueAtPersisted  bool       `json:"dueAtPersisted"`
	DueReady        bool       `json:"dueReady"`
	Overdue         bool       `json:"overdue"`
	ClaimOwner      string     `json:"claimOwner"`
	ClaimedAt       *time.Time `json:"claimedAt"`
	Claimed         bool       `json:"claimed"`
	RecoveryState   string     `json:"recoveryState"`
	DueStateSummary string     `json:"dueStateSummary,omitempty"`
	DueSummary      string     `json:"dueSummary,omitempty"`
	ScheduleSummary string     `json:"scheduleSummary,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type ConsoleWorkflow struct {
	ID             string         `json:"id"`
	PluginID       string         `json:"pluginId"`
	TraceID        string         `json:"traceId,omitempty"`
	EventID        string         `json:"eventId,omitempty"`
	RunID          string         `json:"runId,omitempty"`
	CorrelationID  string         `json:"correlationId,omitempty"`
	Status         string         `json:"status"`
	CurrentIndex   int            `json:"currentIndex"`
	WaitingFor     string         `json:"waitingFor,omitempty"`
	SleepingUntil  *time.Time     `json:"sleepingUntil,omitempty"`
	Completed      bool           `json:"completed"`
	Compensated    bool           `json:"compensated"`
	State          map[string]any `json:"state,omitempty"`
	LastEventID    string         `json:"lastEventId,omitempty"`
	LastEventType  string         `json:"lastEventType,omitempty"`
	StatusSource   string         `json:"statusSource,omitempty"`
	StatePersisted bool           `json:"statePersisted"`
	RuntimeOwner   string         `json:"runtimeOwner,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
	Summary        string         `json:"summary,omitempty"`
}

const (
	scheduleDueAtEvidencePersisted        = "persisted-schedule-due-at"
	scheduleDueAtEvidenceRecoveredStartup = "recovered-schedule-due-at"
	scheduleDueAtEvidenceRecoveredClaim   = "recovered-claimed-schedule-due-at"
)

type sqliteConsoleScheduleReader struct {
	store schedulerStore
}

type sqliteConsoleAdapterInstanceReader struct {
	store  AdapterInstanceStateStore
	source string
}

type sqliteConsolePluginSnapshotReader struct {
	store  PluginStatusSnapshotStore
	source string
}

type sqliteConsolePluginEnabledStateReader struct {
	store  PluginEnabledStateStore
	source string
}

type sqliteConsolePluginConfigReader struct {
	store  PluginConfigStateStore
	source string
}

type sqliteConsoleJobReader struct {
	store jobQueueStore
}

type sqliteConsoleWorkflowReader struct {
	store  workflowRuntimeStore
	source string
}

type sqliteConsoleAlertReader struct {
	store interface {
		ListAlerts(context.Context) ([]AlertRecord, error)
	}
}

type sqliteConsoleReplayOperationReader struct {
	store  ReplayOperationStateStore
	source string
}

type sqliteConsoleRolloutOperationReader struct {
	store interface {
		ListRolloutOperationRecords(context.Context) ([]RolloutOperationRecord, error)
	}
	source string
}

type sqliteConsoleRolloutHeadReader struct {
	store  RolloutHeadStateStore
	source string
}

type RuntimeStatus struct {
	Adapters  int `json:"adapters"`
	Plugins   int `json:"plugins"`
	Jobs      int `json:"jobs"`
	Schedules int `json:"schedules"`
}

func NewConsoleAPI(runtime *InMemoryRuntime, queue *JobQueue, config Config, logs []string, audits AuditLogReader) *ConsoleAPI {
	provider := NewCurrentRBACAuthorizerProviderFromConfig(config.RBAC)
	return &ConsoleAPI{runtime: runtime, queue: queue, config: config, logs: logs, audits: audits, meta: map[string]any{}, pluginConfigMeta: map[string]ConsolePluginConfigBinding{}, authorizerSource: provider, readAuthorizer: NewCurrentConsoleReadAuthorizer(provider)}
}

func NewSQLiteConsoleJobReader(store jobQueueStore) consoleJobReader {
	if store == nil {
		return nil
	}
	return sqliteConsoleJobReader{store: store}
}

func NewSQLiteConsoleAlertReader(store interface {
	ListAlerts(context.Context) ([]AlertRecord, error)
}) consoleAlertReader {
	if store == nil {
		return nil
	}
	return sqliteConsoleAlertReader{store: store}
}

func NewSQLiteConsoleReplayOperationReader(store ReplayOperationStateStore, source ...string) consoleReplayOperationReader {
	if store == nil {
		return nil
	}
	return sqliteConsoleReplayOperationReader{store: store, source: normalizeConsoleStateSource(firstConsoleStateSource(source...), "sqlite-replay-operation-records")}
}

func NewSQLiteConsoleRolloutOperationReader(store interface {
	ListRolloutOperationRecords(context.Context) ([]RolloutOperationRecord, error)
}, source ...string) consoleRolloutOperationReader {
	if store == nil {
		return nil
	}
	return sqliteConsoleRolloutOperationReader{store: store, source: normalizeConsoleStateSource(firstConsoleStateSource(source...), "sqlite-rollout-operation-records")}
}

func NewSQLiteConsoleRolloutHeadReader(store RolloutHeadStateStore, source ...string) consoleRolloutHeadReader {
	if store == nil {
		return nil
	}
	return sqliteConsoleRolloutHeadReader{store: store, source: normalizeConsoleStateSource(firstConsoleStateSource(source...), "sqlite-rollout-heads")}
}

func NewSQLiteConsoleScheduleReader(store schedulerStore) consoleScheduleReader {
	if store == nil {
		return nil
	}
	return sqliteConsoleScheduleReader{store: store}
}

func NewSQLiteConsoleWorkflowReader(store workflowRuntimeStore, source ...string) consoleWorkflowReader {
	if store == nil {
		return nil
	}
	return sqliteConsoleWorkflowReader{store: store, source: normalizeConsoleStateSource(firstConsoleStateSource(source...), "sqlite-workflow-instances")}
}

func NewSQLiteConsoleAdapterInstanceReader(store AdapterInstanceStateStore, source ...string) consoleAdapterInstanceReader {
	if store == nil {
		return nil
	}
	return sqliteConsoleAdapterInstanceReader{store: store, source: normalizeConsoleStateSource(firstConsoleStateSource(source...), "sqlite-adapter-instances")}
}

func NewSQLiteConsolePluginSnapshotReader(store PluginStatusSnapshotStore, source ...string) consolePluginSnapshotReader {
	if store == nil {
		return nil
	}
	return sqliteConsolePluginSnapshotReader{store: store, source: normalizeConsoleStateSource(firstConsoleStateSource(source...), "sqlite-plugin-status-snapshot")}
}

func NewSQLiteConsolePluginEnabledStateReader(store PluginEnabledStateStore, source ...string) consolePluginEnabledStateReader {
	if store == nil {
		return nil
	}
	return sqliteConsolePluginEnabledStateReader{store: store, source: normalizeConsoleStateSource(firstConsoleStateSource(source...), "sqlite-plugin-enabled-overlay")}
}

func NewSQLiteConsolePluginConfigReader(store PluginConfigStateStore, source ...string) consolePluginConfigReader {
	if store == nil {
		return nil
	}
	return sqliteConsolePluginConfigReader{store: store, source: normalizeConsoleStateSource(firstConsoleStateSource(source...), "sqlite-plugin-config")}
}

func (c *ConsoleAPI) SetScheduleReader(reader consoleScheduleReader) {
	c.schedules = reader
}

func (c *ConsoleAPI) SetWorkflowReader(reader consoleWorkflowReader) {
	c.workflows = reader
}

func (c *ConsoleAPI) SetAdapterInstanceReader(reader consoleAdapterInstanceReader) {
	c.adapterInstances = reader
}

func (c *ConsoleAPI) SetPluginSnapshotReader(reader consolePluginSnapshotReader) {
	c.pluginSnapshots = reader
}

func (c *ConsoleAPI) SetPluginEnabledStateReader(reader consolePluginEnabledStateReader) {
	c.pluginEnabled = reader
}

func (c *ConsoleAPI) SetPluginConfigReader(reader consolePluginConfigReader) {
	c.pluginConfigs = reader
}

func (c *ConsoleAPI) SetPluginConfigBindings(bindings map[string]ConsolePluginConfigBinding) {
	if len(bindings) == 0 {
		c.pluginConfigMeta = map[string]ConsolePluginConfigBinding{}
		return
	}
	cloned := make(map[string]ConsolePluginConfigBinding, len(bindings))
	for pluginID, binding := range bindings {
		cloned[pluginID] = binding
	}
	c.pluginConfigMeta = cloned
}

func (c *ConsoleAPI) SetJobReader(reader consoleJobReader) {
	c.jobs = reader
}

func (c *ConsoleAPI) SetAlertReader(reader consoleAlertReader) {
	c.alerts = reader
}

func (c *ConsoleAPI) SetReplayOperationReader(reader consoleReplayOperationReader) {
	c.replayOps = reader
}

func (c *ConsoleAPI) SetRolloutOperationReader(reader consoleRolloutOperationReader) {
	c.rolloutOps = reader
}

func (c *ConsoleAPI) SetRolloutHeadReader(reader consoleRolloutHeadReader) {
	c.rolloutHeads = reader
}

func (c *ConsoleAPI) SetRecoverySource(source consoleRecoverySource) {
	c.recovery = source
}

func (c *ConsoleAPI) SetReadAuthorizer(authorizer ConsoleReadRequestAuthorizer) {
	c.readAuthorizer = authorizer
}

func (c *ConsoleAPI) SetCurrentAuthorizerProvider(provider CurrentAuthorizerProvider) {
	c.authorizerSource = provider
	if isNilCurrentAuthorizerProvider(provider) {
		c.readAuthorizer = nil
		return
	}
	c.readAuthorizer = NewCurrentConsoleReadAuthorizer(provider)
}

func (c *ConsoleAPI) SetMeta(meta map[string]any) {
	if len(meta) == 0 {
		c.meta = map[string]any{}
		return
	}
	cloned := make(map[string]any, len(meta))
	for key, value := range meta {
		cloned[key] = value
	}
	c.meta = cloned
}

func (c *ConsoleAPI) Plugins() []ConsolePlugin {
	if c.runtime == nil || c.runtime.plugins == nil {
		return nil
	}
	manifests := c.runtime.plugins.List()
	dispatchResults := c.runtime.DispatchResults()
	lastDispatch := map[string]DispatchResult{}
	for _, result := range dispatchResults {
		lastDispatch[result.PluginID] = result
	}
	persistedSnapshots := map[string]PluginStatusSnapshot{}
	if c.pluginSnapshots != nil {
		loaded, err := c.pluginSnapshots.ListPluginStatusSnapshots()
		if err == nil {
			for _, snapshot := range loaded {
				persistedSnapshots[snapshot.PluginID] = snapshot
			}
		}
	}
	persistedEnabledStates := map[string]PluginEnabledState{}
	if c.pluginEnabled != nil {
		loaded, err := c.pluginEnabled.ListPluginEnabledStates()
		if err == nil {
			for _, state := range loaded {
				persistedEnabledStates[state.PluginID] = state
			}
		}
	}
	persistedConfigStates := map[string]PluginConfigState{}
	if c.pluginConfigs != nil {
		loaded, err := c.pluginConfigs.ListPluginConfigs()
		if err == nil {
			for _, state := range loaded {
				persistedConfigStates[state.PluginID] = state
			}
		}
	}
	items := make([]ConsolePlugin, 0, len(manifests))
	for _, manifest := range manifests {
		item := ConsolePlugin{
			ID:                 manifest.ID,
			Name:               manifest.Name,
			Version:            manifest.Version,
			APIVersion:         manifest.APIVersion,
			Mode:               string(manifest.Mode),
			Permissions:        append([]string(nil), manifest.Permissions...),
			ConfigSchema:       manifest.ConfigSchema,
			Entry:              manifest.Entry,
			Publish:            toConsolePluginPublish(manifest.Publish),
			Enabled:            true,
			EnabledStateSource: "runtime-default-enabled",
			StatusSource:       "runtime-registry",
		}
		if binding, ok := c.pluginConfigMeta[manifest.ID]; ok {
			applyPluginConfigBinding(&item, binding)
			if state, ok := persistedConfigStates[manifest.ID]; ok {
				applyPluginConfigState(&item, binding, state)
			}
		}
		if state, ok := persistedEnabledStates[manifest.ID]; ok {
			applyPluginEnabledState(&item, state)
		}
		if snapshot, ok := persistedSnapshots[manifest.ID]; ok {
			applyPluginStatusSnapshot(&item, snapshot)
		}
		if dispatch, ok := lastDispatch[manifest.ID]; ok {
			applyRuntimeDispatchOverlay(&item, dispatch, dispatchResults)
		}
		item.StatusLevel = consolePluginStatusLevel(item)
		item.StatusRecovery = consolePluginStatusRecovery(item)
		item.StatusStaleness = consolePluginStatusStaleness(item)
		item.StatusEvidence = consolePluginStatusEvidence(item)
		item.StatusSummary = consolePluginStatusSummary(item)
		items = append(items, item)
	}
	return items
}

func toConsolePluginPublish(publish *pluginsdk.PluginPublish) *ConsolePluginPublish {
	if publish == nil {
		return nil
	}
	return &ConsolePluginPublish{
		SourceType:          publish.SourceType,
		SourceURI:           publish.SourceURI,
		RuntimeVersionRange: publish.RuntimeVersionRange,
	}
}

func applyPluginEnabledState(item *ConsolePlugin, state PluginEnabledState) {
	applyPluginEnabledStateWithSource(item, state, "sqlite-plugin-enabled-overlay")
}

func applyPluginEnabledStateWithSource(item *ConsolePlugin, state PluginEnabledState, source string) {
	if item == nil {
		return
	}
	item.Enabled = state.Enabled
	item.EnabledStateSource = normalizeConsoleStateSource(source, "sqlite-plugin-enabled-overlay")
	item.EnabledStatePersisted = true
	if !state.UpdatedAt.IsZero() {
		updatedAt := state.UpdatedAt.UTC()
		item.EnabledStateUpdatedAt = &updatedAt
	}
}

func applyPluginConfigBinding(item *ConsolePlugin, binding ConsolePluginConfigBinding) {
	if item == nil {
		return
	}
	stateKind := strings.TrimSpace(binding.StateKind)
	if stateKind == "" {
		return
	}
	item.ConfigStateKind = stateKind
}

func applyPluginConfigState(item *ConsolePlugin, binding ConsolePluginConfigBinding, state PluginConfigState) {
	applyPluginConfigStateWithSource(item, binding, state, "sqlite-plugin-config")
}

func applyPluginConfigStateWithSource(item *ConsolePlugin, binding ConsolePluginConfigBinding, state PluginConfigState, source string) {
	if item == nil {
		return
	}
	stateKind := strings.TrimSpace(binding.StateKind)
	if stateKind == "" {
		return
	}
	item.ConfigStateKind = stateKind
	item.ConfigSource = normalizeConsoleStateSource(source, "sqlite-plugin-config")
	item.ConfigPersisted = true
	if config := projectConsolePluginConfig(binding, state); len(config) > 0 {
		item.Config = config
	}
	if !state.UpdatedAt.IsZero() {
		updatedAt := state.UpdatedAt.UTC()
		item.ConfigUpdatedAt = &updatedAt
	}
}

func projectConsolePluginConfig(binding ConsolePluginConfigBinding, state PluginConfigState) map[string]any {
	if len(state.RawConfig) == 0 || string(state.RawConfig) == "null" {
		return nil
	}
	if binding.ProjectConfig != nil {
		config, err := binding.ProjectConfig(state.RawConfig)
		if err != nil {
			return nil
		}
		return cloneAnyMap(config)
	}
	var config map[string]any
	if err := json.Unmarshal(state.RawConfig, &config); err != nil {
		return nil
	}
	return cloneAnyMap(config)
}

func applyPluginStatusSnapshot(item *ConsolePlugin, snapshot PluginStatusSnapshot) {
	applyPluginStatusSnapshotWithSource(item, snapshot, "sqlite-plugin-status-snapshot")
}

func applyPluginStatusSnapshotWithSource(item *ConsolePlugin, snapshot PluginStatusSnapshot, source string) {
	if item == nil {
		return
	}
	success := snapshot.LastDispatchSuccess
	item.LastDispatchKind = snapshot.LastDispatchKind
	item.LastDispatchSuccess = &success
	item.LastDispatchError = snapshot.LastDispatchError
	if !snapshot.LastDispatchAt.IsZero() {
		dispatchAt := snapshot.LastDispatchAt.UTC()
		item.LastDispatchAt = &dispatchAt
	}
	if snapshot.LastRecoveredAt != nil {
		recoveredAt := snapshot.LastRecoveredAt.UTC()
		item.LastRecoveredAt = &recoveredAt
	} else {
		item.LastRecoveredAt = nil
	}
	item.LastRecoveryFailureCount = snapshot.LastRecoveryFailureCount
	item.CurrentFailureStreak = snapshot.CurrentFailureStreak
	item.StatusSource = "runtime-registry+" + normalizeConsoleStateSource(source, "sqlite-plugin-status-snapshot")
	item.RuntimeStateLive = false
	item.StatusPersisted = true
}

func applyRuntimeDispatchOverlay(item *ConsolePlugin, dispatch DispatchResult, results []DispatchResult) {
	applyRuntimeDispatchOverlayWithSource(item, dispatch, results, "sqlite-plugin-status-snapshot")
}

func applyRuntimeDispatchOverlayWithSource(item *ConsolePlugin, dispatch DispatchResult, results []DispatchResult, source string) {
	if item == nil {
		return
	}
	persistedFailureStreak := item.CurrentFailureStreak
	persistedRecoveredAt := item.LastRecoveredAt
	persistedRecoveryFailureCount := item.LastRecoveryFailureCount
	success := dispatch.Success
	item.LastDispatchKind = dispatch.Kind
	item.LastDispatchSuccess = &success
	item.LastDispatchError = dispatch.Error
	if !dispatch.At.IsZero() {
		dispatchAt := dispatch.At.UTC()
		item.LastDispatchAt = &dispatchAt
	}
	recoveryFacts := consolePluginRecoveryFacts(*item, results)
	if !dispatch.Success {
		item.LastRecoveredAt = persistedRecoveredAt
		item.LastRecoveryFailureCount = persistedRecoveryFailureCount
		item.CurrentFailureStreak = recoveryFacts.CurrentFailureStreak
		if item.CurrentFailureStreak == 0 {
			item.CurrentFailureStreak = 1
		}
		if persistedFailureStreak > 0 {
			item.CurrentFailureStreak += persistedFailureStreak
		}
	} else if recoveryFacts.LastRecoveredAt != nil || recoveryFacts.LastRecoveryFailureCount > 0 {
		item.LastRecoveredAt = recoveryFacts.LastRecoveredAt
		item.LastRecoveryFailureCount = recoveryFacts.LastRecoveryFailureCount
		item.CurrentFailureStreak = 0
	} else if persistedFailureStreak > 0 {
		if item.LastDispatchAt != nil {
			recoveredAt := item.LastDispatchAt.UTC()
			item.LastRecoveredAt = &recoveredAt
		}
		item.LastRecoveryFailureCount = persistedFailureStreak
		item.CurrentFailureStreak = 0
	}
	item.RuntimeStateLive = true
	if item.StatusPersisted {
		item.StatusSource = "runtime-registry+" + normalizeConsoleStateSource(source, "sqlite-plugin-status-snapshot") + "+runtime-dispatch-results"
	} else {
		item.StatusSource = "runtime-registry+runtime-dispatch-results"
	}
}

func (c *ConsoleAPI) FilteredPlugins(pluginID string) []ConsolePlugin {
	items := c.Plugins()
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return items
	}
	filtered := make([]ConsolePlugin, 0, 1)
	for _, item := range items {
		if item.ID == pluginID {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (c *ConsoleAPI) AdapterInstances() ([]ConsoleAdapterInstance, error) {
	if c.adapterInstances == nil {
		return nil, nil
	}
	states, err := c.adapterInstances.ListAdapterInstances()
	if err != nil {
		return nil, fmt.Errorf("load console adapter instances: %w", err)
	}
	items := make([]ConsoleAdapterInstance, 0, len(states))
	source := consoleAdapterInstanceSource(c.adapterInstances)
	for _, state := range states {
		items = append(items, toConsoleAdapterInstanceWithSource(state, source))
	}
	return items, nil
}

func toConsoleAdapterInstance(state AdapterInstanceState) ConsoleAdapterInstance {
	return toConsoleAdapterInstanceWithSource(state, "sqlite-adapter-instances")
}

func toConsoleAdapterInstanceWithSource(state AdapterInstanceState, source string) ConsoleAdapterInstance {
	config := map[string]any{}
	if len(state.RawConfig) > 0 && string(state.RawConfig) != "null" {
		_ = json.Unmarshal(state.RawConfig, &config)
	}
	item := ConsoleAdapterInstance{
		ID:             state.InstanceID,
		Adapter:        state.Adapter,
		Source:         state.Source,
		Config:         config,
		Status:         state.Status,
		Health:         state.Health,
		Online:         state.Online,
		StatusSource:   normalizeConsoleStateSource(source, "sqlite-adapter-instances"),
		ConfigSource:   normalizeConsoleStateSource(source, "sqlite-adapter-instances"),
		StatePersisted: true,
	}
	if !state.UpdatedAt.IsZero() {
		updatedAt := state.UpdatedAt.UTC()
		item.UpdatedAt = &updatedAt
	}
	item.Summary = consoleAdapterInstanceSummary(item)
	return item
}

func consoleAdapterInstanceSummary(item ConsoleAdapterInstance) string {
	onlineState := "offline"
	if item.Online {
		onlineState = "online"
	}
	summary := fmt.Sprintf("adapter instance %s for %s/%s is %s with health=%s via %s", item.ID, item.Adapter, item.Source, item.Status, item.Health, item.StatusSource)
	if item.StatePersisted {
		summary += "; persisted state survives restart"
	}
	summary += "; online=" + onlineState
	return summary
}

func consolePluginStatusLevel(plugin ConsolePlugin) string {
	if plugin.LastDispatchSuccess == nil {
		return "registered"
	}
	if *plugin.LastDispatchSuccess {
		return "ok"
	}
	return "error"
}

func consolePluginStatusRecovery(plugin ConsolePlugin) string {
	if plugin.LastDispatchSuccess == nil {
		return "no-runtime-evidence"
	}
	if !*plugin.LastDispatchSuccess {
		return "last-dispatch-failed"
	}
	if plugin.LastRecoveredAt != nil && plugin.LastRecoveryFailureCount > 0 {
		return "recovered-after-failure"
	}
	return "last-dispatch-succeeded"
}

type consolePluginRecoveryMetadata struct {
	LastRecoveredAt          *time.Time
	LastRecoveryFailureCount int
	CurrentFailureStreak     int
}

func consolePluginRecoveryFacts(plugin ConsolePlugin, results []DispatchResult) consolePluginRecoveryMetadata {
	if plugin.LastDispatchSuccess == nil {
		return consolePluginRecoveryMetadata{}
	}
	facts := consolePluginRecoveryMetadata{}
	failureStreak := 0
	for _, result := range results {
		if result.PluginID != plugin.ID {
			continue
		}
		if result.Success {
			if failureStreak > 0 {
				facts.LastRecoveryFailureCount = failureStreak
				if !result.At.IsZero() {
					recoveredAt := result.At.UTC()
					facts.LastRecoveredAt = &recoveredAt
				} else {
					facts.LastRecoveredAt = nil
				}
			}
			failureStreak = 0
			continue
		}
		failureStreak++
	}
	if !*plugin.LastDispatchSuccess {
		facts.CurrentFailureStreak = failureStreak
	}
	return facts
}

func consolePluginStatusStaleness(plugin ConsolePlugin) string {
	if plugin.LastDispatchSuccess == nil {
		return "static-registration"
	}
	if plugin.RuntimeStateLive && plugin.StatusPersisted {
		return "persisted-snapshot+live-overlay"
	}
	if plugin.RuntimeStateLive && !plugin.StatusPersisted {
		return "process-local-volatile"
	}
	if plugin.StatusPersisted {
		return "persisted-snapshot"
	}
	return "unknown"
}

func consolePluginStatusEvidence(plugin ConsolePlugin) string {
	if plugin.LastDispatchSuccess == nil {
		return "manifest-only"
	}
	if consolePluginInstanceConfigRejected(plugin) {
		if plugin.StatusPersisted && !plugin.RuntimeStateLive {
			return "persisted-plugin-status-snapshot:instance-config-reject"
		}
		return "runtime-dispatch-result:instance-config-reject"
	}
	if plugin.StatusPersisted && plugin.RuntimeStateLive {
		return "live-dispatch-overlay+persisted-plugin-status-snapshot"
	}
	if plugin.StatusPersisted {
		return "persisted-plugin-status-snapshot"
	}
	return "runtime-dispatch-result"
}

func consolePluginInstanceConfigRejected(plugin ConsolePlugin) bool {
	if plugin.LastDispatchSuccess == nil || *plugin.LastDispatchSuccess {
		return false
	}
	if plugin.Mode != string(pluginsdk.ModeSubprocess) {
		return false
	}
	errorText := strings.ToLower(strings.TrimSpace(plugin.LastDispatchError))
	if errorText == "" {
		return false
	}
	return strings.Contains(errorText, "instance config required property") ||
		strings.Contains(errorText, "instance config property") ||
		strings.Contains(errorText, "nested instance config required property") ||
		strings.Contains(errorText, "nested instance config property")
}

func consolePluginStatusSummary(plugin ConsolePlugin) string {
	if plugin.LastDispatchSuccess == nil {
		return fmt.Sprintf("manifest registered via %s; no runtime dispatch evidence yet; status is static registration only", plugin.StatusSource)
	}
	dispatchKind := plugin.LastDispatchKind
	if strings.TrimSpace(dispatchKind) == "" {
		dispatchKind = "unknown"
	}
	recovery := plugin.StatusRecovery
	if strings.TrimSpace(recovery) == "" {
		recovery = "last-dispatch-unknown"
	}
	staleness := plugin.StatusStaleness
	if strings.TrimSpace(staleness) == "" {
		staleness = "unknown"
	}
	if consolePluginInstanceConfigRejected(plugin) {
		summary := fmt.Sprintf("last runtime %s dispatch was rejected before subprocess launch via %s; stage=instance-config; recovery=%s; evidence=%s", dispatchKind, plugin.StatusSource, recovery, staleness)
		summary += consolePluginRecoveryFactSummary(plugin)
		if plugin.LastDispatchError != "" {
			summary += ": " + plugin.LastDispatchError
		}
		return summary
	}
	status := "failed"
	if *plugin.LastDispatchSuccess {
		status = "success"
	}
	summary := fmt.Sprintf("last runtime %s dispatch %s via %s; recovery=%s; evidence=%s", dispatchKind, status, plugin.StatusSource, recovery, staleness)
	summary += consolePluginRecoveryFactSummary(plugin)
	if plugin.LastDispatchError != "" {
		summary += ": " + plugin.LastDispatchError
	}
	return summary
}

func consolePluginRecoveryFactSummary(plugin ConsolePlugin) string {
	parts := make([]string, 0, 3)
	if plugin.LastRecoveredAt != nil {
		parts = append(parts, "last_recovered_at="+plugin.LastRecoveredAt.UTC().Format(time.RFC3339))
	}
	if plugin.LastRecoveryFailureCount > 0 {
		parts = append(parts, fmt.Sprintf("last_recovery_failure_count=%d", plugin.LastRecoveryFailureCount))
	}
	if plugin.CurrentFailureStreak > 0 {
		parts = append(parts, fmt.Sprintf("current_failure_streak=%d", plugin.CurrentFailureStreak))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
}

func (r sqliteConsolePluginSnapshotReader) ListPluginStatusSnapshots() ([]PluginStatusSnapshot, error) {
	if r.store == nil {
		return nil, nil
	}
	return r.store.ListPluginStatusSnapshots(context.Background())
}

func (r sqliteConsoleAdapterInstanceReader) ListAdapterInstances() ([]AdapterInstanceState, error) {
	if r.store == nil {
		return nil, nil
	}
	return r.store.ListAdapterInstances(context.Background())
}

func (r sqliteConsolePluginEnabledStateReader) ListPluginEnabledStates() ([]PluginEnabledState, error) {
	if r.store == nil {
		return nil, nil
	}
	return r.store.ListPluginEnabledStates(context.Background())
}

func (r sqliteConsolePluginConfigReader) ListPluginConfigs() ([]PluginConfigState, error) {
	if r.store == nil {
		return nil, nil
	}
	return r.store.ListPluginConfigs(context.Background())
}

func (c *ConsoleAPI) Jobs() ([]ConsoleJob, error) {
	return c.FilteredJobs("")
}

func (c *ConsoleAPI) FilteredJobs(query string) ([]ConsoleJob, error) {
	var jobs []Job
	if c.jobs != nil {
		loaded, err := c.jobs.ListJobs()
		if err != nil {
			return nil, fmt.Errorf("load console jobs: %w", err)
		}
		jobs = loaded
	} else if c.queue != nil {
		jobs = c.queue.List()
	}
	if jobs == nil {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	filtered := make([]ConsoleJob, 0)
	if query == "" {
		for _, job := range jobs {
			filtered = append(filtered, toConsoleJob(job))
		}
		return filtered, nil
	}
	for _, job := range jobs {
		if strings.Contains(job.ID, query) || strings.Contains(job.Type, query) || strings.Contains(string(job.Status), query) || strings.Contains(job.Correlation, query) || strings.Contains(job.LastError, query) {
			filtered = append(filtered, toConsoleJob(job))
		}
	}
	return filtered, nil
}

func (c *ConsoleAPI) Logs(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return append([]string(nil), c.logs...)
	}
	filtered := make([]string, 0)
	for _, line := range c.logs {
		if strings.Contains(line, query) {
			filtered = append(filtered, line)
		}
	}
	return filtered
}

func (c *ConsoleAPI) Alerts() ([]AlertRecord, error) {
	if c.alerts == nil {
		return nil, nil
	}
	alerts, err := c.alerts.ListAlerts()
	if err != nil {
		return nil, fmt.Errorf("load console alerts: %w", err)
	}
	return alerts, nil
}

func (c *ConsoleAPI) ReplayOperations() ([]ConsoleReplayOperation, error) {
	if c.replayOps == nil {
		return nil, nil
	}
	records, err := c.replayOps.ListReplayOperationRecords()
	if err != nil {
		return nil, fmt.Errorf("load console replay operations: %w", err)
	}
	items := make([]ConsoleReplayOperation, 0, len(records))
	source := consoleReplayOperationSource(c.replayOps)
	for _, record := range records {
		item := ConsoleReplayOperation{
			ReplayID:      record.ReplayID,
			SourceEventID: record.SourceEventID,
			ReplayEventID: record.ReplayEventID,
			Status:        record.Status,
			Reason:        record.Reason,
			OccurredAt:    nullableConsoleTime(record.OccurredAt),
			UpdatedAt:     nullableConsoleTime(record.UpdatedAt),
			StateSource:   source,
			Persisted:     true,
		}
		item.Summary = consoleReplayOperationSummary(item)
		items = append(items, item)
	}
	return items, nil
}

func (c *ConsoleAPI) RolloutOperations() ([]ConsoleRolloutOperation, error) {
	if c.rolloutOps == nil {
		return nil, nil
	}
	records, err := c.rolloutOps.ListRolloutOperationRecords()
	if err != nil {
		return nil, fmt.Errorf("load console rollout operations: %w", err)
	}
	items := make([]ConsoleRolloutOperation, 0, len(records))
	source := consoleRolloutOperationSource(c.rolloutOps)
	for _, record := range records {
		item := ConsoleRolloutOperation{
			OperationID:      record.OperationID,
			PluginID:         record.PluginID,
			Action:           record.Action,
			CurrentVersion:   record.CurrentVersion,
			CandidateVersion: record.CandidateVersion,
			Status:           record.Status,
			Reason:           record.Reason,
			OccurredAt:       nullableConsoleTime(record.OccurredAt),
			UpdatedAt:        nullableConsoleTime(record.UpdatedAt),
			StateSource:      source,
			Persisted:        true,
		}
		item.Summary = consoleRolloutOperationSummary(item)
		items = append(items, item)
	}
	return items, nil
}

func (c *ConsoleAPI) RolloutHeads() ([]ConsoleRolloutHead, error) {
	if c.rolloutHeads == nil {
		return nil, nil
	}
	records, err := c.rolloutHeads.ListRolloutHeads()
	if err != nil {
		return nil, fmt.Errorf("load console rollout heads: %w", err)
	}
	items := make([]ConsoleRolloutHead, 0, len(records))
	source := consoleRolloutHeadSource(c.rolloutHeads)
	for _, record := range records {
		item := ConsoleRolloutHead{
			PluginID:        record.PluginID,
			Stable:          consoleRolloutSnapshot(record.Stable),
			Active:          consoleRolloutSnapshot(record.Active),
			Phase:           record.Phase,
			Status:          record.Status,
			Reason:          record.Reason,
			LastOperationID: record.LastOperationID,
			UpdatedAt:       nullableConsoleTime(record.UpdatedAt),
			StateSource:     source,
			Persisted:       true,
		}
		if record.Candidate != nil {
			candidate := consoleRolloutSnapshot(*record.Candidate)
			item.Candidate = &candidate
		}
		item.Summary = consoleRolloutHeadSummary(item)
		items = append(items, item)
	}
	return items, nil
}

func (c *ConsoleAPI) Audits() []pluginsdk.AuditEntry {
	if c.audits == nil {
		return nil
	}
	return c.audits.AuditEntries()
}

func (c *ConsoleAPI) currentConsoleReadPermission() string {
	if c == nil || isNilCurrentAuthorizerProvider(c.authorizerSource) {
		return ""
	}
	snapshot := c.authorizerSource.CurrentSnapshot()
	if snapshot == nil {
		return ""
	}
	return strings.TrimSpace(snapshot.ConsoleReadPermission)
}

func (c *ConsoleAPI) Config() Config {
	config := c.config
	if !isNilCurrentAuthorizerProvider(c.authorizerSource) {
		snapshot := c.authorizerSource.CurrentSnapshot()
		if snapshot == nil {
			config.RBAC = nil
			return config
		}
		config.RBAC = &RBACConfig{
			ActorRoles:            cloneActorRoles(snapshot.ActorRoles),
			Policies:              cloneAuthorizationPolicies(snapshot.Policies),
			ConsoleReadPermission: strings.TrimSpace(snapshot.ConsoleReadPermission),
		}
		return config
	}
	return config
}

func (c *ConsoleAPI) Schedules() ([]ConsoleSchedule, error) {
	if c.schedules == nil {
		return nil, nil
	}
	plans, err := c.schedules.ListSchedulePlans()
	if err != nil {
		return nil, fmt.Errorf("load console schedules: %w", err)
	}
	return plans, nil
}

func (c *ConsoleAPI) Workflows() ([]ConsoleWorkflow, error) {
	if c.workflows == nil {
		return []ConsoleWorkflow{}, nil
	}
	loaded, err := c.workflows.ListWorkflowInstances()
	if err != nil {
		return nil, fmt.Errorf(`load console workflows: %w`, err)
	}
	items := make([]ConsoleWorkflow, 0, len(loaded))
	source := consoleWorkflowSource(c.workflows)
	for _, instance := range loaded {
		items = append(items, toConsoleWorkflowWithSource(instance, source))
	}
	return items, nil
}

func (c *ConsoleAPI) Status() (RuntimeStatus, error) {
	status := RuntimeStatus{}
	if c.runtime != nil {
		status.Adapters = len(c.runtime.RegisteredAdapters())
		status.Plugins = len(c.runtime.plugins.List())
	}
	jobs, err := c.FilteredJobs("")
	if err != nil {
		return RuntimeStatus{}, err
	}
	status.Jobs = len(jobs)
	schedules, err := c.Schedules()
	if err != nil {
		return RuntimeStatus{}, err
	}
	status.Schedules = len(schedules)
	return status, nil
}

func (c *ConsoleAPI) RenderJSON() (string, error) {
	return c.renderJSONWithFilters("", "", "")
}

func (c *ConsoleAPI) renderJSONWithFilters(logQuery, jobQuery, pluginID string) (string, error) {
	adapterInstances, err := c.AdapterInstances()
	if err != nil {
		return "", err
	}
	alerts, err := c.Alerts()
	if err != nil {
		return "", err
	}
	replayOps, err := c.ReplayOperations()
	if err != nil {
		return "", err
	}
	rolloutOps, err := c.RolloutOperations()
	if err != nil {
		return "", err
	}
	rolloutHeads, err := c.RolloutHeads()
	if err != nil {
		return "", err
	}
	jobs, err := c.FilteredJobs(jobQuery)
	if err != nil {
		return "", err
	}
	schedules, err := c.Schedules()
	if err != nil {
		return "", err
	}
	workflows, err := c.Workflows()
	if err != nil {
		return "", err
	}
	status, err := c.Status()
	if err != nil {
		return "", err
	}
	plugins := c.FilteredPlugins(pluginID)
	payload := map[string]any{
		"adapters":      adapterInstances,
		"alerts":        alerts,
		"replayOps":     replayOps,
		"rolloutOps":    rolloutOps,
		"rolloutHeads":  rolloutHeads,
		"plugins":       plugins,
		"jobs":          jobs,
		"schedules":     schedules,
		"workflows":     workflows,
		"logs":          c.Logs(logQuery),
		"audits":        c.Audits(),
		"config":        c.Config(),
		"meta":          c.meta,
		"recovery":      c.Recovery(),
		"observability": c.Observability(plugins, jobs, schedules, alerts),
		"status":        status,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (r sqliteConsoleScheduleReader) ListSchedulePlans() ([]ConsoleSchedule, error) {
	stored, err := r.store.ListSchedulePlans(context.Background())
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	items := make([]ConsoleSchedule, 0, len(stored))
	for _, plan := range stored {
		dueAt, err := restoredScheduleDueAt(func() time.Time { return now }, plan)
		if err != nil {
			continue
		}
		dueReady := consoleScheduleDueReady(dueAt, now)
		overdue := consoleScheduleOverdue(dueAt, now)
		dueAtEvidence := consoleScheduleDueAtEvidence(plan)
		recoveryState := consoleScheduleRecoveryState(dueAtEvidence)
		items = append(items, ConsoleSchedule{
			ID:              plan.Plan.ID,
			Kind:            string(plan.Plan.Kind),
			Source:          plan.Plan.Source,
			EventType:       plan.Plan.EventType,
			CronExpr:        plan.Plan.CronExpr,
			DelayMs:         plan.Plan.Delay.Milliseconds(),
			ExecuteAt:       nullableConsoleTime(plan.Plan.ExecuteAt),
			DueAt:           dueAt,
			DueAtSource:     consoleScheduleDueAtSource(dueAtEvidence),
			DueAtEvidence:   dueAtEvidence,
			DueAtPersisted:  plan.DueAt != nil && !plan.DueAt.IsZero(),
			DueReady:        dueReady,
			Overdue:         overdue,
			ClaimOwner:      strings.TrimSpace(plan.ClaimOwner),
			ClaimedAt:       plan.ClaimedAt,
			Claimed:         plan.ClaimedAt != nil && !plan.ClaimedAt.IsZero() && strings.TrimSpace(plan.ClaimOwner) != "",
			RecoveryState:   recoveryState,
			DueStateSummary: consoleScheduleStateSummary(dueAt, dueReady),
			DueSummary:      consoleScheduleDueSummary(string(plan.Plan.Kind), dueAt, dueReady, dueAtEvidence),
			ScheduleSummary: consoleScheduleSummary(plan.Plan.EventType, string(plan.Plan.Kind), dueAt, dueReady, dueAtEvidence, strings.TrimSpace(plan.ClaimOwner), plan.ClaimedAt),
			CreatedAt:       plan.CreatedAt,
			UpdatedAt:       plan.UpdatedAt,
		})
	}
	return items, nil
}

func (r sqliteConsoleWorkflowReader) ListWorkflowInstances() ([]WorkflowInstanceState, error) {
	if r.store == nil {
		return nil, nil
	}
	return r.store.ListWorkflowInstances(context.Background())
}

func toConsoleWorkflow(instance WorkflowInstanceState) ConsoleWorkflow {
	return toConsoleWorkflowWithSource(instance, `sqlite-workflow-instances`)
}

func toConsoleWorkflowWithSource(instance WorkflowInstanceState, source string) ConsoleWorkflow {
	item := ConsoleWorkflow{
		ID:             instance.WorkflowID,
		PluginID:       instance.PluginID,
		TraceID:        instance.TraceID,
		EventID:        instance.EventID,
		RunID:          instance.RunID,
		CorrelationID:  instance.CorrelationID,
		Status:         string(instance.Status),
		CurrentIndex:   instance.Workflow.CurrentIndex,
		WaitingFor:     instance.Workflow.WaitingFor,
		Completed:      instance.Workflow.Completed,
		Compensated:    instance.Workflow.Compensated,
		State:          cloneWorkflowStateMap(instance.Workflow.State),
		LastEventID:    instance.LastEventID,
		LastEventType:  instance.LastEventType,
		StatusSource:   normalizeConsoleStateSource(source, `sqlite-workflow-instances`),
		StatePersisted: true,
		RuntimeOwner:   `runtime-core`,
		CreatedAt:      instance.CreatedAt,
		UpdatedAt:      instance.UpdatedAt,
	}
	if instance.Workflow.SleepingUntil != nil {
		sleepingUntil := instance.Workflow.SleepingUntil.UTC()
		item.SleepingUntil = &sleepingUntil
	}
	item.Summary = consoleWorkflowSummary(item)
	return item
}

func firstConsoleStateSource(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeConsoleStateSource(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func consoleAdapterInstanceSource(reader consoleAdapterInstanceReader) string {
	if typed, ok := reader.(sqliteConsoleAdapterInstanceReader); ok {
		return normalizeConsoleStateSource(typed.source, "sqlite-adapter-instances")
	}
	return "sqlite-adapter-instances"
}

func consoleReplayOperationSource(reader consoleReplayOperationReader) string {
	if typed, ok := reader.(sqliteConsoleReplayOperationReader); ok {
		return normalizeConsoleStateSource(typed.source, "sqlite-replay-operation-records")
	}
	return "sqlite-replay-operation-records"
}

func consoleRolloutOperationSource(reader consoleRolloutOperationReader) string {
	if typed, ok := reader.(sqliteConsoleRolloutOperationReader); ok {
		return normalizeConsoleStateSource(typed.source, "sqlite-rollout-operation-records")
	}
	return "sqlite-rollout-operation-records"
}

func consoleRolloutHeadSource(reader consoleRolloutHeadReader) string {
	if typed, ok := reader.(sqliteConsoleRolloutHeadReader); ok {
		return normalizeConsoleStateSource(typed.source, "sqlite-rollout-heads")
	}
	return "sqlite-rollout-heads"
}

func consoleWorkflowSource(reader consoleWorkflowReader) string {
	if typed, ok := reader.(sqliteConsoleWorkflowReader); ok {
		return normalizeConsoleStateSource(typed.source, "sqlite-workflow-instances")
	}
	return "sqlite-workflow-instances"
}

func consoleWorkflowSummary(item ConsoleWorkflow) string {
	summary := fmt.Sprintf(`workflow %s for %s is %s via %s`, item.ID, item.PluginID, item.Status, item.StatusSource)
	if item.WaitingFor != `` {
		summary += `; waiting_for=` + item.WaitingFor
	}
	if item.StatePersisted {
		summary += `; persisted state survives restart`
	}
	return summary
}

func consoleScheduleSummary(eventType string, kind string, dueAt *time.Time, dueReady bool, dueAtEvidence string, claimOwner string, claimedAt *time.Time) string {
	dueSummary := consoleScheduleDueSummary(kind, dueAt, dueReady, dueAtEvidence)
	claimSummary := consoleScheduleClaimSummary(claimOwner, claimedAt)
	if eventType == "" {
		return joinConsoleScheduleSummaryParts(dueSummary, claimSummary)
	}
	return joinConsoleScheduleSummaryParts(eventType, dueSummary, claimSummary)
}

func consoleScheduleDueSummary(kind string, dueAt *time.Time, dueReady bool, dueAtEvidence string) string {
	if dueAt == nil || dueAt.IsZero() {
		return ""
	}
	state := "scheduled"
	if dueReady {
		state = "due"
	}
	evidenceSuffix := consoleScheduleDueEvidenceSuffix(dueAtEvidence)
	return fmt.Sprintf("%s %s at %s%s", kind, state, dueAt.UTC().Format(time.RFC3339), evidenceSuffix)
}

func consoleScheduleDueReady(dueAt *time.Time, now time.Time) bool {
	if dueAt == nil || dueAt.IsZero() {
		return false
	}
	return !dueAt.After(now)
}

func consoleScheduleOverdue(dueAt *time.Time, now time.Time) bool {
	if dueAt == nil || dueAt.IsZero() {
		return false
	}
	return dueAt.Before(now)
}

func consoleScheduleStateSummary(dueAt *time.Time, dueReady bool) string {
	if dueAt == nil || dueAt.IsZero() {
		return ""
	}
	if dueReady {
		return "due"
	}
	return "scheduled"
}

func consoleScheduleDueAtEvidence(plan storedSchedulePlan) string {
	evidence := strings.TrimSpace(plan.DueAtEvidence)
	if evidence != "" {
		return evidence
	}
	if plan.DueAt != nil && !plan.DueAt.IsZero() {
		return scheduleDueAtEvidencePersisted
	}
	return scheduleDueAtEvidenceRecoveredStartup
}

func consoleScheduleDueAtSource(evidence string) string {
	switch strings.TrimSpace(evidence) {
	case scheduleDueAtEvidenceRecoveredStartup:
		return "startup-recovery"
	case scheduleDueAtEvidenceRecoveredClaim:
		return "startup-claimed-recovery"
	case scheduleDueAtEvidencePersisted:
		return "persisted-state"
	default:
		return ""
	}
}

func consoleScheduleDueEvidenceSuffix(evidence string) string {
	switch strings.TrimSpace(evidence) {
	case scheduleDueAtEvidenceRecoveredStartup:
		return " (startup-recovered dueAt)"
	case scheduleDueAtEvidenceRecoveredClaim:
		return " (startup-recovered abandoned claimed dueAt)"
	case scheduleDueAtEvidencePersisted:
		return " (persisted dueAt)"
	default:
		return ""
	}
}

func consoleScheduleRecoveryState(evidence string) string {
	switch strings.TrimSpace(evidence) {
	case scheduleDueAtEvidenceRecoveredClaim:
		return "startup-recovered-abandoned-claim"
	case scheduleDueAtEvidenceRecoveredStartup:
		return "startup-recovered"
	default:
		return ""
	}
}

func consoleScheduleClaimSummary(claimOwner string, claimedAt *time.Time) string {
	claimOwner = strings.TrimSpace(claimOwner)
	if claimOwner == "" || claimedAt == nil || claimedAt.IsZero() {
		return ""
	}
	return fmt.Sprintf("claimed by %s at %s", claimOwner, claimedAt.UTC().Format(time.RFC3339))
}

func joinConsoleScheduleSummaryParts(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, " | ")
}

func (r sqliteConsoleJobReader) ListJobs() ([]Job, error) {
	return r.store.ListJobs(context.Background())
}

func (r sqliteConsoleAlertReader) ListAlerts() ([]AlertRecord, error) {
	return r.store.ListAlerts(context.Background())
}

func (r sqliteConsoleReplayOperationReader) ListReplayOperationRecords() ([]ReplayOperationRecord, error) {
	return r.store.ListReplayOperationRecords(context.Background())
}

func (r sqliteConsoleRolloutOperationReader) ListRolloutOperationRecords() ([]RolloutOperationRecord, error) {
	return r.store.ListRolloutOperationRecords(context.Background())
}

func (r sqliteConsoleRolloutHeadReader) ListRolloutHeads() ([]RolloutHeadState, error) {
	return r.store.ListRolloutHeads(context.Background())
}

func nullableConsoleTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

func (c *ConsoleAPI) Recovery() ConsoleRecovery {
	if c.recovery == nil {
		return ConsoleRecovery{}
	}
	snapshot := c.recovery.LastRecoverySnapshot()
	statusCounts := make(map[string]int, len(snapshot.StatusCounts))
	for status, count := range snapshot.StatusCounts {
		statusCounts[string(status)] = count
	}
	scheduleKinds := make(map[string]int, len(snapshot.ScheduleKinds))
	for kind, count := range snapshot.ScheduleKinds {
		scheduleKinds[string(kind)] = count
	}
	return ConsoleRecovery{
		RecoveredAt:        nullableConsoleTime(snapshot.RecoveredAt),
		TotalJobs:          snapshot.TotalJobs,
		RecoveredJobs:      snapshot.RecoveredJobs,
		RecoveredRunning:   snapshot.RecoveredRunning,
		RetriedJobs:        snapshot.RetriedJobs,
		DeadJobs:           snapshot.DeadJobs,
		StatusCounts:       statusCounts,
		TotalSchedules:     snapshot.TotalSchedules,
		RecoveredSchedules: snapshot.RecoveredSchedules,
		InvalidSchedules:   snapshot.InvalidSchedules,
		ScheduleKinds:      scheduleKinds,
		Summary:            consoleRecoverySummary(snapshot),
	}
}

func (c *ConsoleAPI) Observability(plugins []ConsolePlugin, jobs []ConsoleJob, schedules []ConsoleSchedule, alerts []AlertRecord) ConsoleObservability {
	jobReady := 0
	for _, job := range jobs {
		if job.DispatchReady {
			jobReady++
		}
	}
	scheduleReady := 0
	for _, schedule := range schedules {
		if schedule.DueReady {
			scheduleReady++
		}
	}
	obs := ConsoleObservability{
		JobDispatchReady:      jobReady,
		ScheduleDueReady:      scheduleReady,
		JobStateSource:        consoleMetaString(c.meta, "job_status_source"),
		ScheduleStateSource:   consoleMetaString(c.meta, "schedule_status_source"),
		LogStateSource:        consoleMetaString(c.meta, "log_source"),
		TraceStateSource:      consoleMetaString(c.meta, "trace_source"),
		MetricsStateSource:    consoleMetaString(c.meta, "metrics_source"),
		VerificationEndpoints: consoleMetaStringSlice(c.meta, "verification_endpoints"),
	}
	obs.Alertability = consoleAlertabilityBaseline(plugins, jobs, schedules, alerts)
	obs.Summary = consoleObservabilitySummary(obs)
	return obs
}

func consoleMetaString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	value, _ := meta[key].(string)
	return value
}

func consoleMetaStringSlice(meta map[string]any, key string) []string {
	if len(meta) == 0 {
		return nil
	}
	raw, ok := meta[key].([]string)
	if ok {
		return append([]string(nil), raw...)
	}
	rawAny, ok := meta[key].([]any)
	if !ok {
		return nil
	}
	items := make([]string, 0, len(rawAny))
	for _, item := range rawAny {
		if str, ok := item.(string); ok && str != "" {
			items = append(items, str)
		}
	}
	return items
}

func consoleObservabilitySummary(obs ConsoleObservability) string {
	parts := []string{}
	if obs.JobStateSource != "" {
		parts = append(parts, fmt.Sprintf("jobs=%s ready=%d", obs.JobStateSource, obs.JobDispatchReady))
	}
	if obs.ScheduleStateSource != "" {
		parts = append(parts, fmt.Sprintf("schedules=%s due=%d", obs.ScheduleStateSource, obs.ScheduleDueReady))
	}
	if obs.MetricsStateSource != "" {
		parts = append(parts, "metrics="+obs.MetricsStateSource)
	}
	if obs.LogStateSource != "" {
		parts = append(parts, "logs="+obs.LogStateSource)
	}
	if obs.TraceStateSource != "" {
		parts = append(parts, "traces="+obs.TraceStateSource)
	}
	if obs.Alertability.Summary != "" {
		parts = append(parts, "alertability="+obs.Alertability.Summary)
	}
	return strings.Join(parts, " | ")
}

const (
	consoleAlertabilityRuleRepeatedDispatchFailures = "repeated_dispatch_failures"
	consoleAlertabilityRuleReadyBacklog             = "ready_backlog"
	consoleAlertabilityRuleSubprocessFailure        = "subprocess_failure_classified"
	consoleAlertabilityRuleDeadLetterFailurePath    = "dead_letter_failure_paths"
)

func consoleAlertabilityBaseline(plugins []ConsolePlugin, jobs []ConsoleJob, schedules []ConsoleSchedule, alerts []AlertRecord) ConsoleAlertability {
	baseline := []ConsoleAlertabilityRule{
		{
			ID:                    consoleAlertabilityRuleRepeatedDispatchFailures,
			Severity:              "error",
			Signal:                "plugins[].currentFailureStreak + plugins[].statusSummary",
			Condition:             "currentFailureStreak >= 2 means repeated dispatch failures are already visible in the repo-local console payload",
			VerificationEndpoints: []string{"GET /api/console", "GET /metrics"},
			Summary:               "operators can verify repeated dispatch failures from plugin status summaries, failure streaks, and the existing runtime/subprocess dispatch metrics families",
		},
		{
			ID:                    consoleAlertabilityRuleReadyBacklog,
			Severity:              "warn",
			Signal:                "observability.jobDispatchReady + observability.scheduleDueReady + bot_platform_queue_lag",
			Condition:             "non-zero ready job or due schedule counts expose a repo-local backlog snapshot; repeated reads show whether it is draining",
			VerificationEndpoints: []string{"GET /api/console", "GET /metrics"},
			Summary:               "operators can verify queue lag and ready backlog locally from the console snapshot and Prometheus-style metrics without an external monitoring stack",
		},
		{
			ID:                    consoleAlertabilityRuleSubprocessFailure,
			Severity:              "error",
			Signal:                "plugins[].statusSummary + plugins[].lastDispatchError + bot_platform_subprocess_failure_total",
			Condition:             "subprocess plugin failures stay locally classifiable through existing status summaries and subprocess failure metric labels",
			VerificationEndpoints: []string{"GET /api/console", "GET /metrics"},
			Summary:               "operators can verify subprocess failure stage/reason evidence from plugin status summaries and subprocess failure metric families already emitted by runtime-core",
		},
		{
			ID:                    consoleAlertabilityRuleDeadLetterFailurePath,
			Severity:              "error",
			Signal:                "alerts[].failureType=job.dead_letter + alerts[].latestReason + jobs[].replySummary/recoverySummary",
			Condition:             "dead-letter alerts preserve the latest repo-local failure reason for reply, upstream, timeout, and adjacent operator-visible failure paths",
			VerificationEndpoints: []string{"GET /api/console", "GET /demo/state/counts", "GET /demo/replies"},
			Summary:               "operators can verify dead-letter failure paths from persisted alert records and the current console job/reply surfaces without external alert routing",
		},
	}
	findings := make([]ConsoleAlertabilityFinding, 0)
	findings = append(findings, consoleRepeatedDispatchFailureFindings(plugins)...)
	findings = append(findings, consoleReadyBacklogFindings(jobs, schedules)...)
	findings = append(findings, consoleSubprocessFailureFindings(plugins)...)
	findings = append(findings, consoleDeadLetterFailureFindings(alerts)...)
	alertability := ConsoleAlertability{
		Baseline:       baseline,
		ActiveFindings: findings,
	}
	alertability.Summary = consoleAlertabilitySummary(alertability)
	return alertability
}

func consoleRepeatedDispatchFailureFindings(plugins []ConsolePlugin) []ConsoleAlertabilityFinding {
	findings := make([]ConsoleAlertabilityFinding, 0)
	for _, plugin := range plugins {
		if plugin.CurrentFailureStreak < 2 {
			continue
		}
		evidence := []string{fmt.Sprintf("plugin=%s", plugin.ID), fmt.Sprintf("current_failure_streak=%d", plugin.CurrentFailureStreak)}
		if plugin.StatusSource != "" {
			evidence = append(evidence, "status_source="+plugin.StatusSource)
		}
		if plugin.StatusSummary != "" {
			evidence = append(evidence, plugin.StatusSummary)
		}
		findings = append(findings, ConsoleAlertabilityFinding{
			RuleID:   consoleAlertabilityRuleRepeatedDispatchFailures,
			Severity: "error",
			Status:   "firing",
			Summary:  fmt.Sprintf("plugin %s has repeated dispatch failures; current_failure_streak=%d", plugin.ID, plugin.CurrentFailureStreak),
			Evidence: evidence,
		})
	}
	return findings
}

func consoleReadyBacklogFindings(jobs []ConsoleJob, schedules []ConsoleSchedule) []ConsoleAlertabilityFinding {
	jobIDs := make([]string, 0)
	for _, job := range jobs {
		if job.DispatchReady {
			jobIDs = append(jobIDs, job.ID)
		}
	}
	scheduleIDs := make([]string, 0)
	for _, schedule := range schedules {
		if schedule.DueReady {
			scheduleIDs = append(scheduleIDs, schedule.ID)
		}
	}
	if len(jobIDs) == 0 && len(scheduleIDs) == 0 {
		return nil
	}
	evidence := []string{fmt.Sprintf("job_dispatch_ready=%d", len(jobIDs)), fmt.Sprintf("schedule_due_ready=%d", len(scheduleIDs))}
	if len(jobIDs) > 0 {
		evidence = append(evidence, "ready_jobs="+strings.Join(jobIDs, ","))
	}
	if len(scheduleIDs) > 0 {
		evidence = append(evidence, "due_schedules="+strings.Join(scheduleIDs, ","))
	}
	return []ConsoleAlertabilityFinding{{
		RuleID:   consoleAlertabilityRuleReadyBacklog,
		Severity: "warn",
		Status:   "firing",
		Summary:  fmt.Sprintf("ready backlog present; job_dispatch_ready=%d schedule_due_ready=%d", len(jobIDs), len(scheduleIDs)),
		Evidence: evidence,
	}}
}

func consoleSubprocessFailureFindings(plugins []ConsolePlugin) []ConsoleAlertabilityFinding {
	findings := make([]ConsoleAlertabilityFinding, 0)
	for _, plugin := range plugins {
		if plugin.Mode != string(pluginsdk.ModeSubprocess) || plugin.LastDispatchSuccess == nil || *plugin.LastDispatchSuccess {
			continue
		}
		evidence := []string{fmt.Sprintf("plugin=%s", plugin.ID)}
		if plugin.StatusEvidence != "" {
			evidence = append(evidence, "status_evidence="+plugin.StatusEvidence)
		}
		if plugin.LastDispatchError != "" {
			evidence = append(evidence, "last_dispatch_error="+plugin.LastDispatchError)
		}
		if plugin.StatusSummary != "" {
			evidence = append(evidence, plugin.StatusSummary)
		}
		findings = append(findings, ConsoleAlertabilityFinding{
			RuleID:   consoleAlertabilityRuleSubprocessFailure,
			Severity: "error",
			Status:   "firing",
			Summary:  fmt.Sprintf("subprocess plugin %s currently reports a classified failure path", plugin.ID),
			Evidence: evidence,
		})
	}
	return findings
}

func consoleDeadLetterFailureFindings(alerts []AlertRecord) []ConsoleAlertabilityFinding {
	findings := make([]ConsoleAlertabilityFinding, 0)
	for _, alert := range alerts {
		if alert.FailureType != alertFailureTypeJobDeadLetter {
			continue
		}
		classification := consoleDeadLetterFailureClassification(alert.LatestReason)
		summary := fmt.Sprintf("dead-letter alert for job %s preserves failure reason %q", alert.ObjectID, alert.LatestReason)
		if classification != "" {
			summary = fmt.Sprintf("dead-letter alert for job %s preserves %s failure reason %q", alert.ObjectID, classification, alert.LatestReason)
		}
		evidence := []string{fmt.Sprintf("alert_id=%s", alert.ID), fmt.Sprintf("failure_type=%s", alert.FailureType)}
		if alert.Correlation != "" {
			evidence = append(evidence, "correlation="+alert.Correlation)
		}
		if alert.LatestReason != "" {
			evidence = append(evidence, "latest_reason="+alert.LatestReason)
		}
		findings = append(findings, ConsoleAlertabilityFinding{
			RuleID:   consoleAlertabilityRuleDeadLetterFailurePath,
			Severity: "error",
			Status:   "firing",
			Summary:  summary,
			Evidence: evidence,
		})
	}
	return findings
}

func consoleDeadLetterFailureClassification(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return ""
	}
	switch {
	case strings.Contains(reason, "reply") || strings.Contains(reason, "upstream") || strings.Contains(reason, "status 5"):
		return "reply-or-upstream"
	case strings.Contains(reason, "timeout"):
		return "timeout"
	default:
		return "dead-letter"
	}
}

func consoleAlertabilitySummary(alertability ConsoleAlertability) string {
	if len(alertability.Baseline) == 0 {
		return ""
	}
	return fmt.Sprintf("%d rules %d active findings via repo-local console/metrics surfaces", len(alertability.Baseline), len(alertability.ActiveFindings))
}

func consoleReplayOperationSummary(item ConsoleReplayOperation) string {
	parts := []string{fmt.Sprintf("replay %s source=%s result=%s", item.Status, item.SourceEventID, item.ReplayEventID)}
	if item.StateSource != "" {
		parts = append(parts, "via "+item.StateSource)
	}
	if item.Reason != "" {
		parts = append(parts, "reason="+item.Reason)
	}
	return strings.Join(parts, " | ")
}

func consoleRolloutOperationSummary(item ConsoleRolloutOperation) string {
	parts := []string{fmt.Sprintf("rollout %s %s for %s", item.Action, item.Status, item.PluginID)}
	if item.CurrentVersion != "" || item.CandidateVersion != "" {
		parts = append(parts, fmt.Sprintf("current=%s candidate=%s", item.CurrentVersion, item.CandidateVersion))
	}
	if item.StateSource != "" {
		parts = append(parts, "via "+item.StateSource)
	}
	if item.Reason != "" {
		parts = append(parts, "reason="+item.Reason)
	}
	return strings.Join(parts, " | ")
}

func consoleRolloutSnapshot(snapshot RolloutSnapshotState) ConsoleRolloutSnapshot {
	return ConsoleRolloutSnapshot{
		Version:    strings.TrimSpace(snapshot.Version),
		APIVersion: strings.TrimSpace(snapshot.APIVersion),
		Mode:       strings.TrimSpace(snapshot.Mode),
	}
}

func consoleRolloutHeadSummary(item ConsoleRolloutHead) string {
	parts := []string{fmt.Sprintf("rollout head %s %s for %s", item.Phase, item.Status, item.PluginID)}
	if item.Stable.Version != "" || item.Active.Version != "" {
		parts = append(parts, fmt.Sprintf("stable=%s active=%s", item.Stable.Version, item.Active.Version))
	}
	if item.Candidate != nil && item.Candidate.Version != "" {
		parts = append(parts, fmt.Sprintf("candidate=%s", item.Candidate.Version))
	}
	if item.StateSource != "" {
		parts = append(parts, "via "+item.StateSource)
	}
	if item.LastOperationID != "" {
		parts = append(parts, "last_operation="+item.LastOperationID)
	}
	if item.Reason != "" {
		parts = append(parts, "reason="+item.Reason)
	}
	return strings.Join(parts, " | ")
}

func consoleRecoverySummary(snapshot RecoverySnapshot) string {
	if snapshot.RecoveredAt.IsZero() && snapshot.TotalJobs == 0 && snapshot.RecoveredJobs == 0 && snapshot.DeadJobs == 0 && snapshot.RetriedJobs == 0 && snapshot.TotalSchedules == 0 && snapshot.RecoveredSchedules == 0 && snapshot.InvalidSchedules == 0 {
		return ""
	}
	parts := []string{fmt.Sprintf("jobs_restored=%d", snapshot.TotalJobs)}
	if snapshot.RecoveredJobs > 0 {
		parts = append(parts, fmt.Sprintf("running_recovered=%d", snapshot.RecoveredJobs))
	}
	if snapshot.RetriedJobs > 0 {
		parts = append(parts, fmt.Sprintf("retrying=%d", snapshot.RetriedJobs))
	}
	if snapshot.DeadJobs > 0 {
		parts = append(parts, fmt.Sprintf("dead=%d", snapshot.DeadJobs))
	}
	parts = append(parts, fmt.Sprintf("schedules_restored=%d", snapshot.TotalSchedules))
	if snapshot.RecoveredSchedules > 0 {
		parts = append(parts, fmt.Sprintf("schedules_recovered=%d", snapshot.RecoveredSchedules))
	}
	if snapshot.InvalidSchedules > 0 {
		parts = append(parts, fmt.Sprintf("schedules_invalid=%d", snapshot.InvalidSchedules))
	}
	if !snapshot.RecoveredAt.IsZero() {
		parts = append(parts, "at="+snapshot.RecoveredAt.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, " ")
}

func toConsoleJob(job Job) ConsoleJob {
	targetPluginID, dispatchMetadataPresent, dispatchContractPresent, dispatchPermission, dispatchActor, replyHandlePresent, replyHandleCapability, replyContractPresent, replyTarget, sessionIDPresent, replyTargetPresent := queuedDispatchConsoleFields(job.Payload)
	dispatchReady := queuedDispatchReady(job, dispatchMetadataPresent, time.Now().UTC())
	dispatchSummary := queuedDispatchSummary(dispatchActor, targetPluginID, dispatchPermission)
	replySummary := queuedReplySummary(replyHandleCapability, replyTarget)
	recoverySummary := queuedRecoverySummary(job)
	return ConsoleJob{
		ID:                      job.ID,
		Type:                    job.Type,
		TraceID:                 job.TraceID,
		EventID:                 job.EventID,
		RunID:                   job.RunID,
		Status:                  job.Status,
		Payload:                 sanitizedJobPayload(job.Payload),
		RetryCount:              job.RetryCount,
		MaxRetries:              job.MaxRetries,
		Timeout:                 job.Timeout.Nanoseconds(),
		LastError:               job.LastError,
		CreatedAt:               job.CreatedAt,
		StartedAt:               job.StartedAt,
		FinishedAt:              job.FinishedAt,
		NextRunAt:               job.NextRunAt,
		WorkerID:                job.WorkerID,
		LeaseAcquiredAt:         job.LeaseAcquiredAt,
		LeaseExpiresAt:          job.LeaseExpiresAt,
		HeartbeatAt:             job.HeartbeatAt,
		ReasonCode:              job.ReasonCode,
		DeadLetter:              job.DeadLetter,
		Correlation:             job.Correlation,
		TargetPluginID:          targetPluginID,
		DispatchMetadataPresent: dispatchMetadataPresent,
		DispatchContractPresent: dispatchContractPresent,
		QueueContractComplete:   dispatchContractPresent && replyContractPresent,
		DispatchReady:           dispatchReady,
		QueueStateSummary:       queuedStateSummary(job, dispatchMetadataPresent, dispatchContractPresent, dispatchReady),
		DispatchSummary:         dispatchSummary,
		QueueContractSummary:    queuedContractSummary(dispatchSummary, replySummary),
		LeaseSummary:            queuedLeaseSummary(job),
		DispatchPermission:      dispatchPermission,
		DispatchActor:           dispatchActor,
		DispatchRBAC:            queuedJobRBACDeclaration(dispatchActor, dispatchPermission, targetPluginID),
		ReplyHandlePresent:      replyHandlePresent,
		ReplyHandleCapability:   replyHandleCapability,
		ReplyContractPresent:    replyContractPresent,
		ReplySummary:            replySummary,
		SessionIDPresent:        sessionIDPresent,
		ReplyTargetPresent:      replyTargetPresent,
		RecoverySummary:         recoverySummary,
	}
}

func queuedJobRBACDeclaration(actor string, permission string, targetPluginID string) *ConsoleRBACDeclaration {
	if actor == "" && permission == "" && targetPluginID == "" {
		return nil
	}
	declaration := &ConsoleRBACDeclaration{
		Actor:                    actor,
		Permission:               permission,
		TargetPluginID:           targetPluginID,
		DispatchKind:             "job",
		RuntimeAuthorizerEnabled: permission != "",
		ManifestGateEnabled:      permission != "",
		JobTargetFilterEnabled:   targetPluginID != "",
	}
	if declaration.RuntimeAuthorizerEnabled {
		declaration.RuntimeAuthorizerScope = "metadata.permission -> plugin target via shared authorizer"
	}
	if declaration.ManifestGateEnabled {
		declaration.ManifestGateScope = "job handler manifest Permissions must include declared permission"
	}
	declaration.Facts = queuedJobRBACFacts(actor, permission, targetPluginID)
	declaration.Summary = queuedJobRBACSummary(*declaration)
	return declaration
}

func queuedJobRBACFacts(actor string, permission string, targetPluginID string) []string {
	parts := make([]string, 0, 3)
	if permission != "" {
		parts = append(parts, "runtime authorizer applies only when dispatch metadata.permission is set")
		parts = append(parts, "manifest permission gate applies only when a required permission is declared")
	}
	if targetPluginID != "" {
		parts = append(parts, "target_plugin_id narrows job dispatch to one plugin and is not a new RBAC target kind")
	}
	if actor != "" && permission == "" {
		parts = append(parts, "actor is visible in dispatch metadata but does not trigger runtime authorization without permission")
	}
	return parts
}

func queuedJobRBACSummary(declaration ConsoleRBACDeclaration) string {
	summary := "job dispatch metadata"
	if declaration.Actor != "" {
		summary += " actor=" + declaration.Actor
	}
	if declaration.Permission != "" {
		summary += " permission=" + declaration.Permission + " enables runtime authorizer + manifest permission gate"
	} else {
		summary += " has no permission; runtime authorizer and manifest permission gate are inactive"
	}
	if declaration.TargetPluginID != "" {
		summary += "; target_plugin_id=" + declaration.TargetPluginID + " limits dispatch routing only"
	}
	return summary
}

func queuedStateSummary(job Job, dispatchMetadataPresent bool, dispatchContractPresent bool, dispatchReady bool) string {
	if !dispatchMetadataPresent {
		return string(job.Status)
	}
	if dispatchReady {
		if dispatchContractPresent {
			return "ready"
		}
		return "ready-incomplete-contract"
	}
	switch job.Status {
	case JobStatusRetrying:
		return "waiting-retry"
	case JobStatusPending:
		return "pending"
	default:
		return string(job.Status)
	}
}

func queuedContractSummary(dispatchSummary string, replySummary string) string {
	if dispatchSummary == "" && replySummary == "" {
		return ""
	}
	if dispatchSummary == "" {
		return replySummary
	}
	if replySummary == "" {
		return dispatchSummary
	}
	return dispatchSummary + " | " + replySummary
}

func queuedDispatchSummary(actor string, targetPluginID string, permission string) string {
	if actor == "" && targetPluginID == "" && permission == "" {
		return ""
	}
	summary := actor
	if summary == "" {
		summary = "dispatch"
	}
	if targetPluginID != "" {
		summary += " -> " + targetPluginID
	}
	if permission != "" {
		summary += " [" + permission + "]"
	}
	return summary
}

func queuedReplySummary(capability string, target string) string {
	if capability == "" && target == "" {
		return ""
	}
	summary := capability
	if summary == "" {
		summary = "reply"
	}
	if target != "" {
		summary += " -> " + target
	}
	return summary
}

func queuedDispatchReady(job Job, dispatchMetadataPresent bool, now time.Time) bool {
	if !dispatchMetadataPresent {
		return false
	}
	switch job.Status {
	case JobStatusPending:
		return true
	case JobStatusRetrying:
		return job.NextRunAt == nil || !job.NextRunAt.After(now)
	default:
		return false
	}
}

func queuedLeaseSummary(job Job) string {
	parts := make([]string, 0, 4)
	if job.WorkerID != "" {
		parts = append(parts, "worker="+job.WorkerID)
	}
	if job.LeaseAcquiredAt != nil && !job.LeaseAcquiredAt.IsZero() {
		parts = append(parts, "lease_acquired_at="+job.LeaseAcquiredAt.UTC().Format(time.RFC3339))
	}
	if job.LeaseExpiresAt != nil && !job.LeaseExpiresAt.IsZero() {
		parts = append(parts, "lease_expires_at="+job.LeaseExpiresAt.UTC().Format(time.RFC3339))
	}
	if job.HeartbeatAt != nil && !job.HeartbeatAt.IsZero() {
		parts = append(parts, "heartbeat_at="+job.HeartbeatAt.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, " ")
}

func queuedRecoverySummary(job Job) string {
	if job.ReasonCode == "" && job.LastError == "" {
		return ""
	}
	parts := make([]string, 0, 3)
	if job.ReasonCode != "" {
		parts = append(parts, "reason_code="+string(job.ReasonCode))
	}
	if job.LastError != "" {
		parts = append(parts, job.LastError)
	}
	if job.WorkerID != "" {
		parts = append(parts, "worker="+job.WorkerID)
	}
	return strings.Join(parts, " | ")
}

func queuedDispatchConsoleFields(payload map[string]any) (targetPluginID string, dispatchMetadataPresent bool, dispatchContractPresent bool, dispatchPermission string, dispatchActor string, replyHandlePresent bool, replyHandleCapability string, replyContractPresent bool, replyTarget string, sessionIDPresent bool, replyTargetPresent bool) {
	dispatch, _ := payload["dispatch"].(map[string]any)
	replyHandle, _ := payload["reply_handle"].(map[string]any)
	replyTarget, _ = payload["reply_target"].(string)
	replyHandlePresent = len(replyHandle) > 0
	replyHandleCapability, _ = replyHandle["capability"].(string)
	replyTargetPresent = replyTarget != ""
	replyContractPresent = replyHandlePresent && replyTargetPresent
	dispatchMetadataPresent = len(dispatch) > 0
	if sessionID, ok := payload["session_id"].(string); ok && sessionID != "" {
		sessionIDPresent = true
	}
	dispatchContractPresent = dispatchMetadataPresent && replyHandlePresent
	if dispatch == nil {
		return "", dispatchMetadataPresent, dispatchContractPresent, "", "", replyHandlePresent, replyHandleCapability, replyContractPresent, replyTarget, sessionIDPresent, replyTargetPresent
	}
	targetPluginID, _ = dispatch["target_plugin_id"].(string)
	dispatchPermission, _ = dispatch["permission"].(string)
	dispatchActor, _ = dispatch["actor"].(string)
	return targetPluginID, dispatchMetadataPresent, dispatchContractPresent, dispatchPermission, dispatchActor, replyHandlePresent, replyHandleCapability, replyContractPresent, replyTarget, sessionIDPresent, replyTargetPresent
}

func sanitizedJobPayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return map[string]any{}
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return map[string]any{
		"redacted": true,
		"keys":     keys,
	}
}

func (c *ConsoleAPI) recordConsoleReadDenied(r *http.Request, err error) {
	recorder, ok := c.audits.(AuditRecorder)
	if !ok || recorder == nil {
		return
	}
	permission := c.currentConsoleReadPermission()
	actor := strings.TrimSpace(RequestIdentityContextFromContext(r.Context()).ActorID)
	if actor == "" {
		actor = strings.TrimSpace(r.Header.Get(ConsoleReadActorHeader))
	}
	entry := pluginsdk.AuditEntry{
		Actor:         actor,
		Permission:    permission,
		Action:        "console.read",
		Target:        consoleReadTarget,
		Allowed:       false,
		ErrorCategory: "authorization",
		ErrorCode:     authorizationDeniedAuditReason(err),
		OccurredAt:    time.Now().UTC().Format(time.RFC3339),
	}
	setAuditEntryReason(&entry, authorizationDeniedAuditReason(err))
	ApplyAuditRequestIdentity(&entry, r.Context())
	_ = recorder.RecordAudit(entry)
}

type requestIdentityConsoleReadAuthorizer struct {
	provider               CurrentAuthorizerProvider
	operatorAuthConfigured bool
}

func NewRequestIdentityConsoleReadAuthorizer(provider CurrentAuthorizerProvider, operatorAuthConfigured bool) ConsoleReadRequestAuthorizer {
	if isNilCurrentAuthorizerProvider(provider) {
		return nil
	}
	return requestIdentityConsoleReadAuthorizer{provider: provider, operatorAuthConfigured: operatorAuthConfigured}
}

func (a requestIdentityConsoleReadAuthorizer) AuthorizeConsoleRead(ctx context.Context, request *http.Request) error {
	if isNilCurrentAuthorizerProvider(a.provider) {
		return nil
	}
	actor, err := RequestActorID(ctx, a.operatorAuthConfigured, request.Header.Get(ConsoleReadActorHeader))
	if err != nil {
		return err
	}
	snapshot := a.provider.CurrentSnapshot()
	if snapshot == nil || snapshot.Authorizer == nil {
		return errors.New("permission denied")
	}
	permission := strings.TrimSpace(snapshot.ConsoleReadPermission)
	if permission == "" {
		return nil
	}
	decision := snapshot.Authorizer.Authorize(actor, permission, consoleReadTarget)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (c *ConsoleAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c.readAuthorizer != nil {
		if err := c.readAuthorizer.AuthorizeConsoleRead(r.Context(), r); err != nil {
			if errors.Is(err, ErrRequestUnauthorized) {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			c.recordConsoleReadDenied(r, err)
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}
	raw, err := c.renderJSONWithFilters(r.URL.Query().Get("log_query"), r.URL.Query().Get("job_query"), r.URL.Query().Get("plugin_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(raw))
}

func LoadConsoleConfig(path string) (Config, error) {
	if _, err := os.Stat(path); err != nil {
		return Config{}, fmt.Errorf("console config stat: %w", err)
	}
	return LoadConfig(path)
}
