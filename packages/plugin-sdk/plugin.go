package pluginsdk

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
)

const (
	ModeInProc     = "inproc"
	ModeSubprocess = "subprocess"
	ModeRemote     = "remote"

	PublishSourceTypeGit     = "git"
	PublishSourceTypeArchive = "archive"

	PluginManifestSchemaVersionV1        = "v1"
	SupportedPluginManifestSchemaVersion = PluginManifestSchemaVersionV1
)

type PluginManifest struct {
	SchemaVersion string         `json:"schemaVersion"`
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Version       string         `json:"version"`
	APIVersion    string         `json:"apiVersion"`
	Mode          string         `json:"mode"`
	Permissions   []string       `json:"permissions,omitempty"`
	ConfigSchema  map[string]any `json:"configSchema,omitempty"`
	Publish       *PluginPublish `json:"publish,omitempty"`
	Entry         PluginEntry    `json:"entry"`
}

type PluginPublish struct {
	SourceType          string `json:"sourceType"`
	SourceURI           string `json:"sourceUri"`
	RuntimeVersionRange string `json:"runtimeVersionRange"`
}

type PluginEntry struct {
	Module string `json:"module,omitempty"`
	Symbol string `json:"symbol,omitempty"`
	Binary string `json:"binary,omitempty"`
}

type EventHandler interface {
	OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error
}

type CommandHandler interface {
	OnCommand(command eventmodel.CommandInvocation, ctx eventmodel.ExecutionContext) error
}

type JobHandler interface {
	OnJob(job JobInvocation, ctx eventmodel.ExecutionContext) error
}

type ScheduleHandler interface {
	OnSchedule(trigger ScheduleTrigger, ctx eventmodel.ExecutionContext) error
}

type JobInvocation struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type JobStatus string

type JobReasonCode string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusRetrying  JobStatus = "retrying"
	JobStatusPaused    JobStatus = "paused"
	JobStatusCancelled JobStatus = "cancelled"
	JobStatusFailed    JobStatus = "failed"
	JobStatusDead      JobStatus = "dead"
	JobStatusDone      JobStatus = "done"

	JobReasonCodeRuntimeRestart  JobReasonCode = "runtime_restart"
	JobReasonCodeWorkerAbandoned JobReasonCode = "worker_abandoned"
	JobReasonCodeTimeout         JobReasonCode = "timeout"
	JobReasonCodeDispatchRetry   JobReasonCode = "dispatch_retry"
	JobReasonCodeDispatchDead    JobReasonCode = "dispatch_dead"
	JobReasonCodeExecutionRetry  JobReasonCode = "execution_retry"
	JobReasonCodeExecutionDead   JobReasonCode = "execution_dead"
)

type Job struct {
	ID              string         `json:"id"`
	Type            string         `json:"type"`
	TraceID         string         `json:"traceId,omitempty"`
	EventID         string         `json:"eventId,omitempty"`
	RunID           string         `json:"runId,omitempty"`
	Status          JobStatus      `json:"status"`
	Payload         map[string]any `json:"payload,omitempty"`
	RetryCount      int            `json:"retryCount"`
	MaxRetries      int            `json:"maxRetries"`
	Timeout         time.Duration  `json:"timeout"`
	LastError       string         `json:"lastError,omitempty"`
	ReasonCode      JobReasonCode  `json:"reasonCode,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	StartedAt       *time.Time     `json:"startedAt,omitempty"`
	FinishedAt      *time.Time     `json:"finishedAt,omitempty"`
	NextRunAt       *time.Time     `json:"nextRunAt,omitempty"`
	WorkerID        string         `json:"workerId,omitempty"`
	LeaseAcquiredAt *time.Time     `json:"leaseAcquiredAt,omitempty"`
	LeaseExpiresAt  *time.Time     `json:"leaseExpiresAt,omitempty"`
	HeartbeatAt     *time.Time     `json:"heartbeatAt,omitempty"`
	DeadLetter      bool           `json:"deadLetter"`
	Correlation     string         `json:"correlation,omitempty"`
}

func NewJob(id, jobType string, maxRetries int, timeout time.Duration) Job {
	return Job{
		ID:         id,
		Type:       jobType,
		Status:     JobStatusPending,
		MaxRetries: maxRetries,
		Timeout:    timeout,
		CreatedAt:  time.Now().UTC(),
	}
}

func (j Job) Validate() error {
	if j.ID == "" {
		return errors.New("job id is required")
	}
	if j.Type == "" {
		return errors.New("job type is required")
	}
	status := j.Status
	if status == "" {
		status = JobStatusPending
	}
	switch status {
	case JobStatusPending, JobStatusRunning, JobStatusRetrying, JobStatusPaused, JobStatusCancelled, JobStatusFailed, JobStatusDead, JobStatusDone:
	default:
		return fmt.Errorf("unsupported job status %q", j.Status)
	}
	if j.MaxRetries < 0 {
		return errors.New("max retries must be >= 0")
	}
	if j.RetryCount < 0 {
		return errors.New("retry count must be >= 0")
	}
	if j.Timeout < 0 {
		return errors.New("timeout must be >= 0")
	}
	return nil
}

func (j Job) CanTransitionTo(next JobStatus) bool {
	allowed := map[JobStatus][]JobStatus{
		JobStatusPending:   {JobStatusRunning, JobStatusPaused, JobStatusCancelled, JobStatusDead},
		JobStatusRunning:   {JobStatusRetrying, JobStatusFailed, JobStatusDead, JobStatusDone},
		JobStatusRetrying:  {JobStatusRunning, JobStatusPaused, JobStatusCancelled, JobStatusDead},
		JobStatusPaused:    {JobStatusPending, JobStatusRetrying},
		JobStatusCancelled: {},
		JobStatusFailed:    {JobStatusRetrying, JobStatusDead},
		JobStatusDead:      {},
		JobStatusDone:      {},
	}

	for _, candidate := range allowed[j.Status] {
		if candidate == next {
			return true
		}
	}
	return false
}

func (j Job) Transition(next JobStatus, at time.Time, lastError string) (Job, error) {
	if !j.CanTransitionTo(next) {
		return Job{}, fmt.Errorf("invalid job transition from %s to %s", j.Status, next)
	}

	updated := j
	updated.Status = next
	updated.LastError = lastError

	switch next {
	case JobStatusPending:
		updated.NextRunAt = nil
	case JobStatusRunning:
		updated.StartedAt = &at
		updated.NextRunAt = nil
	case JobStatusRetrying:
		if j.Status == JobStatusPaused {
			if updated.NextRunAt == nil || updated.NextRunAt.IsZero() {
				nextRunAt := at
				updated.NextRunAt = &nextRunAt
			}
			if strings.TrimSpace(lastError) == "" {
				updated.LastError = j.LastError
			}
		} else {
			updated.RetryCount++
			updated.NextRunAt = &at
		}
	case JobStatusPaused:
		if j.Status != JobStatusRetrying {
			updated.NextRunAt = nil
		}
		if j.Status == JobStatusRetrying && strings.TrimSpace(lastError) == "" {
			updated.LastError = j.LastError
		}
	case JobStatusCancelled:
		updated.NextRunAt = nil
		updated.FinishedAt = &at
	case JobStatusFailed:
		updated.FinishedAt = &at
	case JobStatusDead:
		updated.FinishedAt = &at
		updated.DeadLetter = true
	case JobStatusDone:
		updated.FinishedAt = &at
	}

	return updated, nil
}

func (j Job) ExplainStatus() string {
	switch j.Status {
	case JobStatusPending:
		return "job is waiting to be picked up"
	case JobStatusRunning:
		return "job is actively executing"
	case JobStatusRetrying:
		return fmt.Sprintf("job is waiting for retry #%d", j.RetryCount)
	case JobStatusPaused:
		return "job is paused before dispatch"
	case JobStatusCancelled:
		return "job was cancelled before execution completed"
	case JobStatusFailed:
		return fmt.Sprintf("job failed and can still retry: %s", j.LastError)
	case JobStatusDead:
		return fmt.Sprintf("job moved to dead letter: %s", j.LastError)
	case JobStatusDone:
		return "job completed successfully"
	default:
		return "job state is unknown"
	}
}

type SessionState struct {
	SessionID string         `json:"sessionId"`
	PluginID  string         `json:"pluginId"`
	State     map[string]any `json:"state,omitempty"`
}

type ScheduleTrigger struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type WorkflowStepKind string

const (
	WorkflowStepKindStep       WorkflowStepKind = "step"
	WorkflowStepKindWaitEvent  WorkflowStepKind = "wait_event"
	WorkflowStepKindSleep      WorkflowStepKind = "sleep"
	WorkflowStepKindCallJob    WorkflowStepKind = "call_job"
	WorkflowStepKindPersist    WorkflowStepKind = "persist_state"
	WorkflowStepKindCompensate WorkflowStepKind = "compensate"
)

type WorkflowStep struct {
	Kind  WorkflowStepKind `json:"kind"`
	Name  string           `json:"name"`
	Value string           `json:"value,omitempty"`
}

type WorkflowCallJobState struct {
	StepName string `json:"stepName,omitempty"`
	JobID    string `json:"jobId,omitempty"`
}

type WorkflowJobResult struct {
	StepName   string        `json:"stepName,omitempty"`
	JobID      string        `json:"jobId,omitempty"`
	Status     JobStatus     `json:"status,omitempty"`
	ReasonCode JobReasonCode `json:"reasonCode,omitempty"`
	LastError  string        `json:"lastError,omitempty"`
}

func (r WorkflowJobResult) IsTerminal() bool {
	switch r.Status {
	case JobStatusDone, JobStatusCancelled, JobStatusFailed, JobStatusDead:
		return true
	default:
		return false
	}
}

func (r WorkflowJobResult) RequiresCompensation() bool {
	switch r.Status {
	case JobStatusCancelled, JobStatusFailed, JobStatusDead:
		return true
	default:
		return false
	}
}

func (r WorkflowJobResult) StateValue() map[string]any {
	state := map[string]any{
		"job_id": string(r.JobID),
		"status": string(r.Status),
	}
	if strings.TrimSpace(r.StepName) != "" {
		state["step_name"] = r.StepName
	}
	if strings.TrimSpace(string(r.ReasonCode)) != "" {
		state["reason_code"] = string(r.ReasonCode)
	}
	if strings.TrimSpace(r.LastError) != "" {
		state["last_error"] = r.LastError
	}
	return state
}

type Workflow struct {
	ID            string                `json:"id"`
	Steps         []WorkflowStep        `json:"steps"`
	CurrentIndex  int                   `json:"currentIndex"`
	State         map[string]any        `json:"state,omitempty"`
	WaitingFor    string                `json:"waitingFor,omitempty"`
	WaitingForJob *WorkflowCallJobState `json:"waitingForJob,omitempty"`
	LastJobResult *WorkflowJobResult    `json:"lastJobResult,omitempty"`
	SleepingUntil *time.Time            `json:"sleepingUntil,omitempty"`
	Completed     bool                  `json:"completed"`
	Compensated   bool                  `json:"compensated"`
}

func NewWorkflow(id string, steps ...WorkflowStep) Workflow {
	return Workflow{ID: id, Steps: steps, State: map[string]any{}}
}

func (w Workflow) CurrentStep() (WorkflowStep, error) {
	if w.Completed || w.CurrentIndex >= len(w.Steps) {
		return WorkflowStep{}, errors.New("workflow has no current step")
	}
	return w.Steps[w.CurrentIndex], nil
}

func (w Workflow) Advance(now time.Time) (Workflow, error) {
	step, err := w.CurrentStep()
	if err != nil {
		return Workflow{}, err
	}

	next := w
	if next.State == nil {
		next.State = map[string]any{}
	}
	switch step.Kind {
	case WorkflowStepKindStep:
		next.CurrentIndex++
	case WorkflowStepKindWaitEvent:
		next.WaitingFor = step.Value
	case WorkflowStepKindSleep:
		until := now.Add(parseWorkflowDuration(step.Value))
		next.SleepingUntil = &until
	case WorkflowStepKindCallJob:
		jobID := strings.TrimSpace(step.Value)
		if jobID == "" {
			return Workflow{}, errors.New("workflow call_job step requires job id value")
		}
		next.WaitingForJob = &WorkflowCallJobState{StepName: strings.TrimSpace(step.Name), JobID: jobID}
		next.LastJobResult = nil
	case WorkflowStepKindPersist:
		next.State[step.Name] = step.Value
		next.CurrentIndex++
	case WorkflowStepKindCompensate:
		if next.LastJobResult == nil || next.LastJobResult.RequiresCompensation() {
			next.Compensated = true
		}
		next.CurrentIndex++
	default:
		return Workflow{}, fmt.Errorf("unsupported workflow step kind %q", step.Kind)
	}

	if next.CurrentIndex >= len(next.Steps) && next.WaitingFor == "" && next.WaitingForJob == nil && next.SleepingUntil == nil {
		next.Completed = true
	}
	return next, nil
}

func (w Workflow) ResumeWithEvent(eventType string) (Workflow, error) {
	step, err := w.CurrentStep()
	if err != nil {
		return Workflow{}, err
	}
	if step.Kind != WorkflowStepKindWaitEvent || w.WaitingFor == "" {
		return Workflow{}, errors.New("workflow is not waiting for event")
	}
	if eventType != w.WaitingFor {
		return Workflow{}, fmt.Errorf("unexpected event %q, waiting for %q", eventType, w.WaitingFor)
	}
	next := w
	next.WaitingFor = ""
	next.CurrentIndex++
	if next.CurrentIndex >= len(next.Steps) {
		next.Completed = true
	}
	return next, nil
}

func (w Workflow) ResumeAfterSleep(now time.Time) (Workflow, error) {
	step, err := w.CurrentStep()
	if err != nil {
		return Workflow{}, err
	}
	if step.Kind != WorkflowStepKindSleep || w.SleepingUntil == nil {
		return Workflow{}, errors.New("workflow is not sleeping")
	}
	if now.Before(*w.SleepingUntil) {
		return Workflow{}, errors.New("workflow sleep not finished")
	}
	next := w
	next.SleepingUntil = nil
	next.CurrentIndex++
	if next.CurrentIndex >= len(next.Steps) {
		next.Completed = true
	}
	return next, nil
}

func (w Workflow) ResumeWithChildJob(result WorkflowJobResult) (Workflow, error) {
	step, err := w.CurrentStep()
	if err != nil {
		return Workflow{}, err
	}
	if step.Kind != WorkflowStepKindCallJob || w.WaitingForJob == nil {
		return Workflow{}, errors.New("workflow is not waiting for child job result")
	}
	result.JobID = strings.TrimSpace(result.JobID)
	if result.JobID == "" {
		return Workflow{}, errors.New("workflow child job result requires job id")
	}
	if !result.IsTerminal() {
		return Workflow{}, fmt.Errorf("workflow child job %q is not terminal: %s", result.JobID, result.Status)
	}
	expectedJobID := strings.TrimSpace(w.WaitingForJob.JobID)
	if expectedJobID == "" {
		expectedJobID = strings.TrimSpace(step.Value)
	}
	if expectedJobID == "" {
		return Workflow{}, errors.New("workflow waiting child job id is missing")
	}
	if result.JobID != expectedJobID {
		return Workflow{}, fmt.Errorf("unexpected child job %q, waiting for %q", result.JobID, expectedJobID)
	}
	if strings.TrimSpace(result.StepName) == "" {
		result.StepName = strings.TrimSpace(step.Name)
	}
	next := w
	if next.State == nil {
		next.State = map[string]any{}
	}
	next.WaitingForJob = nil
	next.LastJobResult = &result
	if strings.TrimSpace(step.Name) != "" {
		next.State[step.Name] = result.StateValue()
	}
	next.CurrentIndex++
	if next.CurrentIndex >= len(next.Steps) {
		next.Completed = true
	}
	return next, nil
}

func parseWorkflowDuration(raw string) time.Duration {
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0
	}
	return duration
}

type ScheduleKind string

const (
	ScheduleKindCron    ScheduleKind = "cron"
	ScheduleKindDelay   ScheduleKind = "delay"
	ScheduleKindOneShot ScheduleKind = "one-shot"
)

type SchedulePlan struct {
	ID        string         `json:"id"`
	Kind      ScheduleKind   `json:"kind"`
	CronExpr  string         `json:"cronExpr,omitempty"`
	Delay     time.Duration  `json:"delay,omitempty"`
	ExecuteAt time.Time      `json:"executeAt"`
	Source    string         `json:"source"`
	EventType string         `json:"eventType"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type AuditEntry struct {
	Actor         string `json:"actor"`
	Permission    string `json:"permission,omitempty"`
	Action        string `json:"action"`
	Target        string `json:"target"`
	Allowed       bool   `json:"allowed"`
	Reason        string `json:"reason,omitempty"`
	TraceID       string `json:"trace_id,omitempty"`
	EventID       string `json:"event_id,omitempty"`
	PluginID      string `json:"plugin_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	OccurredAt    string `json:"occurred_at"`
}

type ReplyService interface {
	ReplyText(handle eventmodel.ReplyHandle, text string) error
	ReplyImage(handle eventmodel.ReplyHandle, imageURL string) error
	ReplyFile(handle eventmodel.ReplyHandle, fileURL string) error
}

type Plugin struct {
	Manifest       PluginManifest
	InstanceConfig map[string]any
	Handlers       Handlers
}

type Handlers struct {
	Event    EventHandler
	Command  CommandHandler
	Job      JobHandler
	Schedule ScheduleHandler
}

func (m PluginManifest) Validate() error {
	if strings.TrimSpace(m.SchemaVersion) == "" {
		return errors.New("schemaVersion is required")
	}
	if strings.TrimSpace(m.SchemaVersion) != SupportedPluginManifestSchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %q", m.SchemaVersion)
	}
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("id is required")
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("version is required")
	}
	if strings.TrimSpace(m.APIVersion) == "" {
		return errors.New("apiVersion is required")
	}
	switch m.Mode {
	case ModeInProc, ModeSubprocess, ModeRemote:
	default:
		return fmt.Errorf("unsupported mode %q", m.Mode)
	}
	if err := m.Entry.Validate(); err != nil {
		return fmt.Errorf("entry: %w", err)
	}
	for _, permission := range m.Permissions {
		if err := ValidatePermission(permission); err != nil {
			return fmt.Errorf("permissions: %w", err)
		}
	}
	if m.Publish != nil {
		if err := m.Publish.Validate(); err != nil {
			return fmt.Errorf("publish: %w", err)
		}
	}
	return nil
}

func (p PluginPublish) Validate() error {
	sourceType := strings.TrimSpace(p.SourceType)
	if sourceType == "" {
		return errors.New("sourceType is required")
	}
	switch sourceType {
	case PublishSourceTypeGit, PublishSourceTypeArchive:
	default:
		return fmt.Errorf("unsupported sourceType %q", p.SourceType)
	}

	sourceURI := strings.TrimSpace(p.SourceURI)
	if sourceURI == "" {
		return errors.New("sourceUri is required")
	}
	parsedSourceURI, err := url.Parse(sourceURI)
	if err != nil || !parsedSourceURI.IsAbs() || parsedSourceURI.Host == "" {
		return fmt.Errorf("sourceUri %q must be an absolute URI", p.SourceURI)
	}

	if err := ValidateRuntimeVersionRange(p.RuntimeVersionRange); err != nil {
		return err
	}

	return nil
}

func ValidateRuntimeVersionRange(requiredRange string) error {
	clauses, err := parseRuntimeVersionRangeClauses(requiredRange)
	if err != nil {
		return err
	}
	for _, clause := range clauses {
		if _, err := parseRuntimeVersion(clause.version); err != nil {
			return err
		}
	}
	return nil
}

func RuntimeVersionSatisfiesRange(currentVersion, requiredRange string) (bool, error) {
	current, err := parseRuntimeVersion(currentVersion)
	if err != nil {
		return false, err
	}
	clauses, err := parseRuntimeVersionRangeClauses(requiredRange)
	if err != nil {
		return false, err
	}
	for _, clause := range clauses {
		required, err := parseRuntimeVersion(clause.version)
		if err != nil {
			return false, err
		}
		if !runtimeVersionMatchesComparator(current, clause.comparator, required) {
			return false, nil
		}
	}
	return true, nil
}

type runtimeVersionRangeClause struct {
	comparator string
	version    string
}

func parseRuntimeVersionRangeClauses(requiredRange string) ([]runtimeVersionRangeClause, error) {
	trimmed := strings.TrimSpace(requiredRange)
	if trimmed == "" {
		return nil, errors.New("runtimeVersionRange is required")
	}
	clauses := make([]runtimeVersionRangeClause, 0, 2)
	for len(trimmed) > 0 {
		comparator, remainder, ok := trimRuntimeVersionComparator(trimmed)
		if !ok {
			return nil, fmt.Errorf("runtimeVersionRange %q must use one or two comparator clauses", requiredRange)
		}
		version, rest, ok := trimRuntimeVersionToken(remainder)
		if !ok {
			return nil, fmt.Errorf("runtimeVersionRange %q must use one or two comparator clauses", requiredRange)
		}
		clauses = append(clauses, runtimeVersionRangeClause{comparator: comparator, version: version})
		trimmed = strings.TrimSpace(rest)
	}
	if len(clauses) == 0 || len(clauses) > 2 {
		return nil, fmt.Errorf("runtimeVersionRange %q must use one or two comparator clauses", requiredRange)
	}
	return clauses, nil
}

func trimRuntimeVersionComparator(raw string) (string, string, bool) {
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	for _, comparator := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(trimmed, comparator) {
			return comparator, strings.TrimLeft(trimmed[len(comparator):], " \t\r\n"), true
		}
	}
	return "", raw, false
}

func trimRuntimeVersionToken(raw string) (string, string, bool) {
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	if trimmed == "" {
		return "", raw, false
	}
	end := 0
	for end < len(trimmed) {
		switch trimmed[end] {
		case ' ', '\t', '\r', '\n':
			return trimmed[:end], trimmed[end:], true
		default:
			end++
		}
	}
	return trimmed, "", true
}

type runtimeVersion struct {
	major int
	minor int
	patch int
}

func parseRuntimeVersion(raw string) (runtimeVersion, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return runtimeVersion{}, fmt.Errorf("runtime version %q must use major.minor.patch", raw)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return runtimeVersion{}, fmt.Errorf("runtime version %q has invalid major component", raw)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return runtimeVersion{}, fmt.Errorf("runtime version %q has invalid minor component", raw)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return runtimeVersion{}, fmt.Errorf("runtime version %q has invalid patch component", raw)
	}
	return runtimeVersion{major: major, minor: minor, patch: patch}, nil
}

func runtimeVersionMatchesComparator(current runtimeVersion, comparator string, required runtimeVersion) bool {
	comparison := compareRuntimeVersions(current, required)
	switch comparator {
	case ">":
		return comparison > 0
	case ">=":
		return comparison >= 0
	case "=":
		return comparison == 0
	case "<=":
		return comparison <= 0
	case "<":
		return comparison < 0
	default:
		return false
	}
}

func compareRuntimeVersions(left, right runtimeVersion) int {
	if left.major != right.major {
		return left.major - right.major
	}
	if left.minor != right.minor {
		return left.minor - right.minor
	}
	return left.patch - right.patch
}

func (e PluginEntry) Validate() error {
	if strings.TrimSpace(e.Module) == "" && strings.TrimSpace(e.Symbol) == "" && strings.TrimSpace(e.Binary) == "" {
		return errors.New("at least one entry target is required")
	}
	return nil
}

func (p Plugin) Validate() error {
	if err := p.Manifest.Validate(); err != nil {
		return err
	}
	if p.Handlers.Event == nil && p.Handlers.Command == nil && p.Handlers.Job == nil && p.Handlers.Schedule == nil {
		return errors.New("at least one plugin handler is required")
	}
	return nil
}
