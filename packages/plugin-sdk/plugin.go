package pluginsdk

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
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
)

var runtimeVersionRangePattern = regexp.MustCompile(`^(>=|>|=|<=|<)\s*v?\d+\.\d+\.\d+(?:\s+(>=|>|=|<=|<)\s*v?\d+\.\d+\.\d+)?$`)

type PluginManifest struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	APIVersion   string         `json:"apiVersion"`
	Mode         string         `json:"mode"`
	Permissions  []string       `json:"permissions,omitempty"`
	ConfigSchema map[string]any `json:"configSchema,omitempty"`
	Publish      *PluginPublish `json:"publish,omitempty"`
	Entry        PluginEntry    `json:"entry"`
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
	case JobStatusPending, JobStatusRunning, JobStatusRetrying, JobStatusCancelled, JobStatusFailed, JobStatusDead, JobStatusDone:
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
		JobStatusPending:   {JobStatusRunning, JobStatusCancelled, JobStatusDead},
		JobStatusRunning:   {JobStatusRetrying, JobStatusFailed, JobStatusDead, JobStatusDone},
		JobStatusRetrying:  {JobStatusRunning, JobStatusCancelled, JobStatusDead},
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
	case JobStatusRunning:
		updated.StartedAt = &at
		updated.NextRunAt = nil
	case JobStatusRetrying:
		updated.RetryCount++
		updated.NextRunAt = &at
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

type Workflow struct {
	ID            string         `json:"id"`
	Steps         []WorkflowStep `json:"steps"`
	CurrentIndex  int            `json:"currentIndex"`
	State         map[string]any `json:"state,omitempty"`
	WaitingFor    string         `json:"waitingFor,omitempty"`
	SleepingUntil *time.Time     `json:"sleepingUntil,omitempty"`
	Completed     bool           `json:"completed"`
	Compensated   bool           `json:"compensated"`
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
		next.State[step.Name] = step.Value
		next.CurrentIndex++
	case WorkflowStepKindPersist:
		next.State[step.Name] = step.Value
		next.CurrentIndex++
	case WorkflowStepKindCompensate:
		next.Compensated = true
		next.CurrentIndex++
	default:
		return Workflow{}, fmt.Errorf("unsupported workflow step kind %q", step.Kind)
	}

	if next.CurrentIndex >= len(next.Steps) && next.WaitingFor == "" && next.SleepingUntil == nil {
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

	runtimeVersionRange := strings.TrimSpace(p.RuntimeVersionRange)
	if runtimeVersionRange == "" {
		return errors.New("runtimeVersionRange is required")
	}
	if !runtimeVersionRangePattern.MatchString(runtimeVersionRange) {
		return fmt.Errorf("runtimeVersionRange %q must use one or two comparator clauses", p.RuntimeVersionRange)
	}

	return nil
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
