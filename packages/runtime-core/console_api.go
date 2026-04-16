package runtimecore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type ConsoleAPI struct {
	runtime         *InMemoryRuntime
	queue           *JobQueue
	jobs            consoleJobReader
	schedules       consoleScheduleReader
	pluginSnapshots consolePluginSnapshotReader
	pluginEnabled   consolePluginEnabledStateReader
	recovery        consoleRecoverySource
	config          Config
	logs            []string
	audits          AuditLogReader
	meta            map[string]any
	readAuthorizer  ConsoleReadRequestAuthorizer
}

type ConsolePlugin struct {
	ID                       string                `json:"id"`
	Name                     string                `json:"name"`
	Version                  string                `json:"version"`
	APIVersion               string                `json:"apiVersion"`
	Mode                     string                `json:"mode"`
	Permissions              []string              `json:"permissions"`
	ConfigSchema             map[string]any        `json:"configSchema,omitempty"`
	Entry                    pluginsdk.PluginEntry `json:"entry"`
	Enabled                  bool                  `json:"enabled"`
	EnabledStateSource       string                `json:"enabledStateSource,omitempty"`
	EnabledStatePersisted    bool                  `json:"enabledStatePersisted"`
	EnabledStateUpdatedAt    *time.Time            `json:"enabledStateUpdatedAt,omitempty"`
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

type consoleJobReader interface {
	ListJobs() ([]Job, error)
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

type ConsoleObservability struct {
	JobDispatchReady      int      `json:"jobDispatchReady"`
	ScheduleDueReady      int      `json:"scheduleDueReady"`
	JobStateSource        string   `json:"jobStateSource,omitempty"`
	ScheduleStateSource   string   `json:"scheduleStateSource,omitempty"`
	LogStateSource        string   `json:"logStateSource,omitempty"`
	TraceStateSource      string   `json:"traceStateSource,omitempty"`
	MetricsStateSource    string   `json:"metricsStateSource,omitempty"`
	VerificationEndpoints []string `json:"verificationEndpoints,omitempty"`
	Summary               string   `json:"summary,omitempty"`
}

type consoleScheduleReader interface {
	ListSchedulePlans() ([]ConsoleSchedule, error)
}

type consolePluginSnapshotReader interface {
	ListPluginStatusSnapshots() ([]PluginStatusSnapshot, error)
}

type consolePluginEnabledStateReader interface {
	ListPluginEnabledStates() ([]PluginEnabledState, error)
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
	DueStateSummary string     `json:"dueStateSummary,omitempty"`
	DueSummary      string     `json:"dueSummary,omitempty"`
	ScheduleSummary string     `json:"scheduleSummary,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

const (
	scheduleDueAtEvidencePersisted        = "persisted-schedule-due-at"
	scheduleDueAtEvidenceRecoveredStartup = "recovered-schedule-due-at"
)

type sqliteConsoleScheduleReader struct {
	store *SQLiteStateStore
}

type sqliteConsolePluginSnapshotReader struct {
	store *SQLiteStateStore
}

type sqliteConsolePluginEnabledStateReader struct {
	store *SQLiteStateStore
}

type sqliteConsoleJobReader struct {
	store *SQLiteStateStore
}

type RuntimeStatus struct {
	Adapters  int `json:"adapters"`
	Plugins   int `json:"plugins"`
	Jobs      int `json:"jobs"`
	Schedules int `json:"schedules"`
}

func NewConsoleAPI(runtime *InMemoryRuntime, queue *JobQueue, config Config, logs []string, audits AuditLogReader) *ConsoleAPI {
	return &ConsoleAPI{runtime: runtime, queue: queue, config: config, logs: logs, audits: audits, meta: map[string]any{}, readAuthorizer: NewConsoleReadAuthorizer(config.RBAC)}
}

func NewSQLiteConsoleJobReader(store *SQLiteStateStore) consoleJobReader {
	if store == nil {
		return nil
	}
	return sqliteConsoleJobReader{store: store}
}

func NewSQLiteConsoleScheduleReader(store *SQLiteStateStore) consoleScheduleReader {
	if store == nil {
		return nil
	}
	return sqliteConsoleScheduleReader{store: store}
}

func NewSQLiteConsolePluginSnapshotReader(store *SQLiteStateStore) consolePluginSnapshotReader {
	if store == nil {
		return nil
	}
	return sqliteConsolePluginSnapshotReader{store: store}
}

func NewSQLiteConsolePluginEnabledStateReader(store *SQLiteStateStore) consolePluginEnabledStateReader {
	if store == nil {
		return nil
	}
	return sqliteConsolePluginEnabledStateReader{store: store}
}

func (c *ConsoleAPI) SetScheduleReader(reader consoleScheduleReader) {
	c.schedules = reader
}

func (c *ConsoleAPI) SetPluginSnapshotReader(reader consolePluginSnapshotReader) {
	c.pluginSnapshots = reader
}

func (c *ConsoleAPI) SetPluginEnabledStateReader(reader consolePluginEnabledStateReader) {
	c.pluginEnabled = reader
}

func (c *ConsoleAPI) SetJobReader(reader consoleJobReader) {
	c.jobs = reader
}

func (c *ConsoleAPI) SetRecoverySource(source consoleRecoverySource) {
	c.recovery = source
}

func (c *ConsoleAPI) SetReadAuthorizer(authorizer ConsoleReadRequestAuthorizer) {
	c.readAuthorizer = authorizer
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
			Enabled:            true,
			EnabledStateSource: "runtime-default-enabled",
			StatusSource:       "runtime-registry",
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

func applyPluginEnabledState(item *ConsolePlugin, state PluginEnabledState) {
	if item == nil {
		return
	}
	item.Enabled = state.Enabled
	item.EnabledStateSource = "sqlite-plugin-enabled-overlay"
	item.EnabledStatePersisted = true
	if !state.UpdatedAt.IsZero() {
		updatedAt := state.UpdatedAt.UTC()
		item.EnabledStateUpdatedAt = &updatedAt
	}
}

func applyPluginStatusSnapshot(item *ConsolePlugin, snapshot PluginStatusSnapshot) {
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
	item.StatusSource = "runtime-registry+sqlite-plugin-status-snapshot"
	item.RuntimeStateLive = false
	item.StatusPersisted = true
}

func applyRuntimeDispatchOverlay(item *ConsolePlugin, dispatch DispatchResult, results []DispatchResult) {
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
		item.StatusSource = "runtime-registry+sqlite-plugin-status-snapshot+runtime-dispatch-results"
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

func (r sqliteConsolePluginEnabledStateReader) ListPluginEnabledStates() ([]PluginEnabledState, error) {
	if r.store == nil {
		return nil, nil
	}
	return r.store.ListPluginEnabledStates(context.Background())
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

func (c *ConsoleAPI) Audits() []pluginsdk.AuditEntry {
	if c.audits == nil {
		return nil
	}
	return c.audits.AuditEntries()
}

func (c *ConsoleAPI) Config() Config {
	return c.config
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
	jobs, err := c.FilteredJobs(jobQuery)
	if err != nil {
		return "", err
	}
	schedules, err := c.Schedules()
	if err != nil {
		return "", err
	}
	status, err := c.Status()
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"plugins":       c.FilteredPlugins(pluginID),
		"jobs":          jobs,
		"schedules":     schedules,
		"logs":          c.Logs(logQuery),
		"audits":        c.Audits(),
		"config":        c.Config(),
		"meta":          c.meta,
		"recovery":      c.Recovery(),
		"observability": c.Observability(jobs, schedules),
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
	items := make([]ConsoleSchedule, 0, len(stored))
	for _, plan := range stored {
		dueAt, err := restoredScheduleDueAt(time.Now().UTC, plan)
		if err != nil {
			continue
		}
		dueReady := consoleScheduleDueReady(dueAt, time.Now().UTC())
		dueAtEvidence := consoleScheduleDueAtEvidence(plan)
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
			DueStateSummary: consoleScheduleStateSummary(dueAt, dueReady),
			DueSummary:      consoleScheduleDueSummary(string(plan.Plan.Kind), dueAt, dueReady, dueAtEvidence),
			ScheduleSummary: consoleScheduleSummary(plan.Plan.EventType, string(plan.Plan.Kind), dueAt, dueReady, dueAtEvidence),
			CreatedAt:       plan.CreatedAt,
			UpdatedAt:       plan.UpdatedAt,
		})
	}
	return items, nil
}

func consoleScheduleSummary(eventType string, kind string, dueAt *time.Time, dueReady bool, dueAtEvidence string) string {
	dueSummary := consoleScheduleDueSummary(kind, dueAt, dueReady, dueAtEvidence)
	if eventType == "" {
		return dueSummary
	}
	if dueSummary == "" {
		return eventType
	}
	return eventType + " | " + dueSummary
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
	case scheduleDueAtEvidencePersisted:
		return " (persisted dueAt)"
	default:
		return ""
	}
}

func (r sqliteConsoleJobReader) ListJobs() ([]Job, error) {
	return r.store.ListJobs(context.Background())
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

func (c *ConsoleAPI) Observability(jobs []ConsoleJob, schedules []ConsoleSchedule) ConsoleObservability {
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
	recoverySummary := ""
	if job.LastError == recoveryReasonRuntimeRestart && (job.Status == JobStatusRetrying || job.Status == JobStatusDead) {
		recoverySummary = string(job.Status) + " after runtime restart"
	}
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
	permission := ""
	if c.config.RBAC != nil {
		permission = strings.TrimSpace(c.config.RBAC.ConsoleReadPermission)
	}
	entry := pluginsdk.AuditEntry{
		Actor:      strings.TrimSpace(r.Header.Get(ConsoleReadActorHeader)),
		Permission: permission,
		Action:     "console.read",
		Target:     consoleReadTarget,
		Allowed:    false,
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
	}
	setAuditEntryReason(&entry, authorizationDeniedAuditReason(err))
	_ = recorder.RecordAudit(entry)
}

func (c *ConsoleAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c.readAuthorizer != nil {
		if err := c.readAuthorizer.AuthorizeConsoleRead(r.Context(), r); err != nil {
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
