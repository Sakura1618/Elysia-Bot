package runtimecore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type AdapterRegistration struct {
	ID      string
	Source  string
	Adapter Adapter
}

type Adapter interface {
	ID() string
	Source() string
}

type Runtime interface {
	RegisterAdapter(AdapterRegistration) error
	RegisterPlugin(pluginsdk.Plugin) error
	DispatchEvent(context.Context, eventmodel.Event) error
	DispatchCommand(context.Context, eventmodel.CommandInvocation, eventmodel.ExecutionContext) error
	DispatchJob(context.Context, pluginsdk.JobInvocation, eventmodel.ExecutionContext) error
	DispatchSchedule(context.Context, pluginsdk.ScheduleTrigger, eventmodel.ExecutionContext) error
	EnqueueJob(context.Context, pluginsdk.JobInvocation) error
	Schedule(context.Context, pluginsdk.ScheduleTrigger) error
	Persist(context.Context, Record) error
	Observe(context.Context, Observation) error
	Supervisor() Supervisor
	PluginHost() PluginHost
}

type Supervisor interface {
	EnsurePlugin(context.Context, string) error
}

type PluginHost interface {
	DispatchEvent(context.Context, pluginsdk.Plugin, eventmodel.Event, eventmodel.ExecutionContext) error
	DispatchCommand(context.Context, pluginsdk.Plugin, eventmodel.CommandInvocation, eventmodel.ExecutionContext) error
	DispatchJob(context.Context, pluginsdk.Plugin, pluginsdk.JobInvocation, eventmodel.ExecutionContext) error
	DispatchSchedule(context.Context, pluginsdk.Plugin, pluginsdk.ScheduleTrigger, eventmodel.ExecutionContext) error
}

type CommandAuthorizer interface {
	AuthorizeCommand(context.Context, eventmodel.CommandInvocation, eventmodel.ExecutionContext) error
}

type Record struct {
	Kind     string
	RefID    string
	Metadata map[string]any
}

type Observation struct {
	Name     string
	Metadata map[string]any
}

type DispatchResult struct {
	PluginID string
	Kind     string
	Success  bool
	Error    string
	At       time.Time
}

type DispatchResultRecorder interface {
	RecordDispatchResult(DispatchResult) error
}

type PluginEnabledStateSource interface {
	PluginEnabled(string) bool
}

type InMemoryRuntime struct {
	mu               sync.RWMutex
	adapters         map[string]AdapterRegistration
	plugins          *pluginsdk.Registry
	supervisor       Supervisor
	host             PluginHost
	commandAuth      CommandAuthorizer
	eventAuth        EventAuthorizer
	jobAuth          JobAuthorizer
	scheduleAuth     ScheduleAuthorizer
	audits           AuditRecorder
	dispatchRecorder DispatchResultRecorder
	logger           *Logger
	tracer           *TraceRecorder
	metrics          *MetricsRegistry
	pluginStates     PluginEnabledStateSource
	jobQueue         []pluginsdk.JobInvocation
	schedules        []pluginsdk.ScheduleTrigger
	records          []Record
	observations     []Observation
	dispatches       []DispatchResult
}

func (r *InMemoryRuntime) SetCommandAuthorizer(authorizer CommandAuthorizer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commandAuth = authorizer
}

func (r *InMemoryRuntime) SetEventAuthorizer(authorizer EventAuthorizer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventAuth = authorizer
}

func (r *InMemoryRuntime) SetJobAuthorizer(authorizer JobAuthorizer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobAuth = authorizer
}

func (r *InMemoryRuntime) SetScheduleAuthorizer(authorizer ScheduleAuthorizer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scheduleAuth = authorizer
}

func (r *InMemoryRuntime) SetAuditRecorder(recorder AuditRecorder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audits = recorder
}

func (r *InMemoryRuntime) SetDispatchRecorder(recorder DispatchResultRecorder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatchRecorder = recorder
}

func (r *InMemoryRuntime) SetPluginEnabledStateSource(source PluginEnabledStateSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pluginStates = source
}

func NewInMemoryRuntime(supervisor Supervisor, host PluginHost) *InMemoryRuntime {
	return &InMemoryRuntime{
		adapters:   make(map[string]AdapterRegistration),
		plugins:    pluginsdk.NewRegistry(),
		supervisor: supervisor,
		host:       host,
		logger:     NewLogger(io.Discard),
		tracer:     NewTraceRecorder(),
		metrics:    NewMetricsRegistry(),
	}
}

func (r *InMemoryRuntime) SetObservability(logger *Logger, tracer *TraceRecorder, metrics *MetricsRegistry) {
	if logger != nil {
		r.logger = logger
	}
	if tracer != nil {
		r.tracer = tracer
	}
	if metrics != nil {
		r.metrics = metrics
	}
}

func (r *InMemoryRuntime) RegisterAdapter(reg AdapterRegistration) error {
	if reg.Adapter == nil {
		return errors.New("adapter is required")
	}
	if reg.ID == "" {
		return errors.New("adapter id is required")
	}
	if reg.Source == "" {
		return errors.New("adapter source is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.adapters[reg.ID]; exists {
		return fmt.Errorf("adapter %q already registered", reg.ID)
	}

	r.adapters[reg.ID] = reg
	return nil
}

func (r *InMemoryRuntime) RegisterPlugin(plugin pluginsdk.Plugin) error {
	return r.plugins.Register(plugin)
}

func (r *InMemoryRuntime) DispatchEvent(ctx context.Context, event eventmodel.Event) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("invalid event: %w", err)
	}
	requiredPermission := eventRequiredPermission(event)
	baseContext := enrichExecutionContext(eventmodel.ExecutionContext{
		TraceID:       event.TraceID,
		EventID:       event.EventID,
		Reply:         event.Reply,
		CorrelationID: event.IdempotencyKey,
	})
	if requiredPermission != "" && !r.anyEventHandlerDeclaresPermission(requiredPermission) {
		pluginIDs := r.pluginsWithHandler("event")
		err := missingManifestPermissionError("event", requiredPermission, pluginIDs)
		r.log("error", "runtime event manifest permission gate failed", logContextFromExecutionContext(baseContext), manifestPermissionGateFields("event", requiredPermission, pluginIDs, map[string]any{"event_type": event.Type, "source": event.Source, "error": err.Error()}))
		return err
	}
	r.metrics.RecordEventThroughput()
	return r.dispatchToPluginsWithFilter(ctx, "event", baseContext,
		func(plugin pluginsdk.Plugin, _ eventmodel.ExecutionContext) bool {
			if plugin.Handlers.Event == nil {
				return false
			}
			if requiredPermission == "" {
				return true
			}
			return manifestDeclaresPermission(plugin.Manifest, requiredPermission)
		},
		func(plugin pluginsdk.Plugin, executionContext eventmodel.ExecutionContext) error {
			r.mu.RLock()
			eventAuth := r.eventAuth
			auditRecorder := r.audits
			r.mu.RUnlock()
			if eventAuth == nil {
				return nil
			}
			if err := eventAuth.AuthorizeEvent(ctx, event, executionContext, plugin); err != nil {
				actor, permission := eventAuthorizationFields(event)
				r.recordAuthorizationDeniedAudit(auditRecorder, actor, permission, plugin.Manifest.ID, authorizationDeniedAuditReason(err))
				return err
			}
			return nil
		},
		map[string]any{"event_type": event.Type, "source": event.Source}, func(plugin pluginsdk.Plugin, executionContext eventmodel.ExecutionContext) error {
			return r.host.DispatchEvent(ctx, plugin, event, executionContext)
		})
}

func (r *InMemoryRuntime) DispatchCommand(ctx context.Context, command eventmodel.CommandInvocation, executionContext eventmodel.ExecutionContext) error {
	if command.Name == "" {
		return errors.New("command name is required")
	}
	if err := executionContext.Validate(); err != nil {
		return fmt.Errorf("invalid execution context: %w", err)
	}
	executionContext = enrichExecutionContext(executionContext)
	r.mu.RLock()
	commandAuth := r.commandAuth
	auditRecorder := r.audits
	r.mu.RUnlock()
	if commandAuth != nil {
		if err := commandAuth.AuthorizeCommand(ctx, command, executionContext); err != nil {
			actor, action, target, permission, targetKind := commandAuthorizationFields(command)
			r.recordAuthorizationDeniedAudit(auditRecorder, actor, permission, target, authorizationDeniedAuditReason(err))
			r.log("error", "runtime command authorization failed", logContextFromExecutionContext(executionContext), FailureLogFields("runtime", "dispatch.command.authorize", err, authorizationDeniedAuditReason(err), map[string]any{"command_name": command.Name, "actor": actor, "action": action, "target": target, "permission": permission}))
			_ = targetKind
			return err
		}
	}
	_, _, _, requiredPermission, _ := commandAuthorizationFields(command)
	if requiredPermission != "" && !r.anyCommandHandlerDeclaresPermission(requiredPermission) {
		pluginIDs := r.pluginsWithHandler("command")
		err := missingManifestPermissionError("command", requiredPermission, pluginIDs)
		r.log("error", "runtime command manifest permission gate failed", logContextFromExecutionContext(executionContext), manifestPermissionGateFields("command", requiredPermission, pluginIDs, map[string]any{"command_name": command.Name, "error": err.Error()}))
		return err
	}
	actor, action, target, permission, _ := commandAuthorizationFields(command)
	fields := map[string]any{"command_name": command.Name}
	if actor != "" {
		fields["actor"] = actor
	}
	if action != "" {
		fields["action"] = action
	}
	if target != "" {
		fields["target"] = target
	}
	if permission != "" {
		fields["permission"] = permission
	}
	return r.dispatchToPluginsWithFilter(ctx, "command", executionContext,
		func(plugin pluginsdk.Plugin, _ eventmodel.ExecutionContext) bool {
			if plugin.Handlers.Command == nil {
				return false
			}
			if requiredPermission == "" {
				return true
			}
			return manifestDeclaresPermission(plugin.Manifest, requiredPermission)
		},
		nil,
		fields,
		func(plugin pluginsdk.Plugin, executionContext eventmodel.ExecutionContext) error {
			return r.host.DispatchCommand(ctx, plugin, command, executionContext)
		},
	)
}

func RecordAuthorizationDeniedAudit(recorder AuditRecorder, actor, permission, target string, err error) {
	if err == nil {
		return
	}
	recordAuthorizationDeniedAuditWithReason(recorder, actor, permission, target, authorizationDeniedAuditReason(err))
}

func (r *InMemoryRuntime) recordAuthorizationDeniedAudit(recorder AuditRecorder, actor, permission, target, reason string) {
	recordAuthorizationDeniedAuditWithReason(recorder, actor, permission, target, reason)
}

func recordAuthorizationDeniedAuditWithReason(recorder AuditRecorder, actor, permission, target, reason string) {
	if recorder == nil || permission == "" || target == "" {
		return
	}
	entry := pluginsdk.AuditEntry{
		Actor:      actor,
		Permission: permission,
		Action:     strings.ReplaceAll(permission, ":", "."),
		Target:     target,
		Allowed:    false,
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
	}
	setAuditEntryReason(&entry, reason)
	_ = recorder.RecordAudit(entry)
}
func authorizationDeniedAuditReason(err error) string {
	switch strings.TrimSpace(strings.ToLower(err.Error())) {
	case "plugin scope denied":
		return "plugin_scope_denied"
	default:
		return "permission_denied"
	}
}

func (r *InMemoryRuntime) anyCommandHandlerDeclaresPermission(permission string) bool {
	for _, manifest := range r.plugins.List() {
		plugin, err := r.plugins.Get(manifest.ID)
		if err != nil {
			continue
		}
		if plugin.Handlers.Command == nil {
			continue
		}
		if manifestDeclaresPermission(plugin.Manifest, permission) {
			return true
		}
	}
	return false
}

func (r *InMemoryRuntime) anyEventHandlerDeclaresPermission(permission string) bool {
	for _, manifest := range r.plugins.List() {
		plugin, err := r.plugins.Get(manifest.ID)
		if err != nil {
			continue
		}
		if plugin.Handlers.Event == nil {
			continue
		}
		if manifestDeclaresPermission(plugin.Manifest, permission) {
			return true
		}
	}
	return false
}

func (r *InMemoryRuntime) anyJobHandlerDeclaresPermission(permission string) bool {
	for _, manifest := range r.plugins.List() {
		plugin, err := r.plugins.Get(manifest.ID)
		if err != nil {
			continue
		}
		if plugin.Handlers.Job == nil {
			continue
		}
		if manifestDeclaresPermission(plugin.Manifest, permission) {
			return true
		}
	}
	return false
}

func (r *InMemoryRuntime) anyScheduleHandlerDeclaresPermission(permission string) bool {
	for _, manifest := range r.plugins.List() {
		plugin, err := r.plugins.Get(manifest.ID)
		if err != nil {
			continue
		}
		if plugin.Handlers.Schedule == nil {
			continue
		}
		if manifestDeclaresPermission(plugin.Manifest, permission) {
			return true
		}
	}
	return false
}

func eventRequiredPermission(event eventmodel.Event) string {
	permission, _ := event.Metadata["permission"].(string)
	return permission
}

func jobRequiredPermission(job pluginsdk.JobInvocation) string {
	permission, _ := job.Metadata["permission"].(string)
	return permission
}

func jobTargetPluginID(job pluginsdk.JobInvocation) string {
	target, _ := job.Metadata["target_plugin_id"].(string)
	return target
}

func scheduleRequiredPermission(trigger pluginsdk.ScheduleTrigger) string {
	permission, _ := trigger.Metadata["permission"].(string)
	return permission
}

func manifestDeclaresPermission(manifest pluginsdk.PluginManifest, permission string) bool {
	for _, declared := range manifest.Permissions {
		if declared == permission {
			return true
		}
	}
	return false
}

func (r *InMemoryRuntime) pluginsWithHandler(kind string) []string {
	items := make([]string, 0)
	for _, manifest := range r.plugins.List() {
		plugin, err := r.plugins.Get(manifest.ID)
		if err != nil {
			continue
		}
		switch kind {
		case "event":
			if plugin.Handlers.Event == nil {
				continue
			}
		case "command":
			if plugin.Handlers.Command == nil {
				continue
			}
		case "job":
			if plugin.Handlers.Job == nil {
				continue
			}
		case "schedule":
			if plugin.Handlers.Schedule == nil {
				continue
			}
		default:
			continue
		}
		items = append(items, manifest.ID)
	}
	sort.Strings(items)
	return items
}

func missingManifestPermissionError(kind, permission string, pluginIDs []string) error {
	if len(pluginIDs) == 0 {
		return fmt.Errorf("no %s handler plugin is registered for required permission %q; register a %s handler plugin and declare %q in manifest permissions", kind, permission, kind, permission)
	}
	return fmt.Errorf("no %s handler plugin declares required permission %q; registered %s handler plugins: %s; add %q to the plugin manifest Permissions", kind, permission, kind, strings.Join(pluginIDs, ", "), permission)
}

func manifestPermissionGateFields(dispatchKind, permission string, pluginIDs []string, fields map[string]any) map[string]any {
	result := FailureLogFields("runtime", "dispatch."+dispatchKind+".manifest_permission_gate", nil, "missing_manifest_permission", mergeFields(fields, map[string]any{
		"dispatch_kind":  dispatchKind,
		"failure_stage":  "manifest_permission_gate",
		"failure_reason": "missing_manifest_permission",
		"permission":     permission,
	}))
	if len(pluginIDs) == 0 {
		result["registered_handler_plugins"] = ""
		result["registered_handler_plugins_count"] = 0
		return result
	}
	result["registered_handler_plugins"] = strings.Join(pluginIDs, ",")
	result["registered_handler_plugins_count"] = len(pluginIDs)
	return result
}

func (r *InMemoryRuntime) DispatchJob(ctx context.Context, job pluginsdk.JobInvocation, executionContext eventmodel.ExecutionContext) error {
	if job.ID == "" {
		return errors.New("job id is required")
	}
	if job.Type == "" {
		return errors.New("job type is required")
	}
	if err := executionContext.Validate(); err != nil {
		return fmt.Errorf("invalid execution context: %w", err)
	}
	executionContext = enrichExecutionContext(executionContext)
	requiredPermission := jobRequiredPermission(job)
	targetPluginID := jobTargetPluginID(job)
	if requiredPermission != "" && !r.anyJobHandlerDeclaresPermission(requiredPermission) {
		pluginIDs := r.pluginsWithHandler("job")
		err := missingManifestPermissionError("job", requiredPermission, pluginIDs)
		r.log("error", "runtime job manifest permission gate failed", logContextFromExecutionContext(executionContext), manifestPermissionGateFields("job", requiredPermission, pluginIDs, map[string]any{"job_id": job.ID, "job_type": job.Type, "error": err.Error()}))
		return err
	}
	return r.dispatchToPluginsWithFilter(ctx, "job", executionContext,
		func(plugin pluginsdk.Plugin, _ eventmodel.ExecutionContext) bool {
			if plugin.Handlers.Job == nil {
				return false
			}
			if targetPluginID != "" && plugin.Manifest.ID != targetPluginID {
				return false
			}
			if requiredPermission == "" {
				return true
			}
			return manifestDeclaresPermission(plugin.Manifest, requiredPermission)
		},
		func(plugin pluginsdk.Plugin, executionContext eventmodel.ExecutionContext) error {
			r.mu.RLock()
			jobAuth := r.jobAuth
			auditRecorder := r.audits
			r.mu.RUnlock()
			if jobAuth == nil {
				return nil
			}
			if err := jobAuth.AuthorizeJob(ctx, job, executionContext, plugin); err != nil {
				actor, permission := metadataAuthorizationFields(job.Metadata)
				r.recordAuthorizationDeniedAudit(auditRecorder, actor, permission, plugin.Manifest.ID, authorizationDeniedAuditReason(err))
				return err
			}
			return nil
		},
		map[string]any{"job_id": job.ID, "job_type": job.Type},
		func(plugin pluginsdk.Plugin, executionContext eventmodel.ExecutionContext) error {
			return r.host.DispatchJob(ctx, plugin, job, executionContext)
		},
	)
}

type QueuedJobFailureTransitioner interface {
	Inspect(context.Context, string) (Job, error)
	MarkRunning(context.Context, string) (Job, error)
	Heartbeat(context.Context, string) (Job, error)
	Fail(context.Context, string, string) (Job, error)
	FailWithReasonCode(context.Context, string, string, JobReasonCode) (Job, error)
}

func DispatchQueuedJob(ctx context.Context, runtime *InMemoryRuntime, queue QueuedJobFailureTransitioner, job Job) error {
	if runtime == nil {
		return errors.New("runtime is required")
	}
	return runtime.DispatchQueuedJob(ctx, queue, job)
}

func (r *InMemoryRuntime) DispatchQueuedJob(ctx context.Context, queue QueuedJobFailureTransitioner, job Job) error {
	stopHeartbeat := startQueuedJobHeartbeat(ctx, queue, job)
	defer stopHeartbeat()

	invocation, executionContext, err := queuedJobDispatchInput(job)
	if err != nil {
		transitionErr := transitionQueuedJobDispatchFailure(ctx, queue, job, err)
		r.logQueuedDispatchFailure(job, err, transitionErr)
		return combineQueuedDispatchErrors(err, transitionErr)
	}
	err = r.DispatchJob(ctx, invocation, executionContext)
	if err != nil {
		transitionErr := transitionQueuedJobDispatchFailure(ctx, queue, job, err)
		r.logQueuedDispatchFailure(job, err, transitionErr)
		return combineQueuedDispatchErrors(err, transitionErr)
	}
	return err
}

func startQueuedJobHeartbeat(ctx context.Context, queue QueuedJobFailureTransitioner, job Job) func() {
	if queue == nil || job.Status != JobStatusRunning || job.Timeout <= 0 {
		return func() {}
	}
	interval := job.Timeout / 3
	if interval <= 0 {
		interval = time.Second
	}
	if interval > 5*time.Second {
		interval = 5 * time.Second
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				_, _ = queue.Heartbeat(context.Background(), job.ID)
			}
		}
	}()
	return cancel
}

func (r *InMemoryRuntime) logQueuedDispatchFailure(job Job, dispatchErr error, transitionErr error) {
	if dispatchErr == nil {
		return
	}
	fields := map[string]any{
		"job_id":   job.ID,
		"job_type": job.Type,
		"error":    dispatchErr.Error(),
	}
	if dispatch, ok := job.Payload["dispatch"].(map[string]any); ok {
		if targetPluginID, ok := dispatch["target_plugin_id"].(string); ok && targetPluginID != "" {
			fields["target_plugin_id"] = targetPluginID
		}
	}
	if transitionErr != nil {
		fields["transition_error"] = transitionErr.Error()
	}
	r.log("error", "runtime queued job dispatch failed", LogContext{
		TraceID:       job.TraceID,
		EventID:       job.EventID,
		RunID:         job.RunID,
		CorrelationID: job.Correlation,
	}, FailureLogFields("runtime", "dispatch.job.queued", dispatchErr, "queued_dispatch_failed", fields))
}

func transitionQueuedJobDispatchFailure(ctx context.Context, queue QueuedJobFailureTransitioner, original Job, dispatchErr error) error {
	if queue == nil || dispatchErr == nil {
		return nil
	}
	stored, err := queue.Inspect(ctx, original.ID)
	if err != nil {
		return err
	}
	if stored.Status == JobStatusPending && queuedJobDispatchStateUnchanged(original, stored) {
		if _, runningErr := queue.MarkRunning(ctx, original.ID); runningErr != nil {
			return fmt.Errorf("mark queued job %q running after dispatch failure: %w", original.ID, runningErr)
		}
		stored, err = queue.Inspect(ctx, original.ID)
		if err != nil {
			return err
		}
	}
	if stored.Status != JobStatusRunning {
		return nil
	}
	if original.Status == JobStatusRunning && !queuedJobDispatchOwnershipStateUnchanged(original, stored) {
		return nil
	}
	if _, failErr := queue.FailWithReasonCode(ctx, original.ID, dispatchErr.Error(), dispatchReasonCode(stored)); failErr != nil {
		return fmt.Errorf("fail queued job %q after dispatch failure: %w", original.ID, failErr)
	}
	return nil
}

func combineQueuedDispatchErrors(primary error, transitionErr error) error {
	if primary == nil {
		return transitionErr
	}
	if transitionErr == nil {
		return primary
	}
	return fmt.Errorf("%w; queued dispatch failure transition also failed: %v", primary, transitionErr)
}

func queuedJobDispatchStateUnchanged(before, after Job) bool {
	if !queuedJobDispatchOwnershipStateUnchanged(before, after) {
		return false
	}
	if !sameQueuedJobTime(before.LeaseExpiresAt, after.LeaseExpiresAt) {
		return false
	}
	if !sameQueuedJobTime(before.HeartbeatAt, after.HeartbeatAt) {
		return false
	}
	return true
}

func queuedJobDispatchOwnershipStateUnchanged(before, after Job) bool {
	if before.Status != after.Status {
		return false
	}
	if before.RetryCount != after.RetryCount {
		return false
	}
	if before.LastError != after.LastError {
		return false
	}
	if !sameQueuedJobTime(before.StartedAt, after.StartedAt) {
		return false
	}
	if !sameQueuedJobTime(before.FinishedAt, after.FinishedAt) {
		return false
	}
	if !sameQueuedJobTime(before.NextRunAt, after.NextRunAt) {
		return false
	}
	if before.ReasonCode != after.ReasonCode || before.WorkerID != after.WorkerID {
		return false
	}
	if !sameQueuedJobTime(before.LeaseAcquiredAt, after.LeaseAcquiredAt) {
		return false
	}
	return before.DeadLetter == after.DeadLetter
}

func dispatchReasonCode(job Job) JobReasonCode {
	if job.RetryCount < job.MaxRetries {
		return JobReasonCodeDispatchRetry
	}
	return JobReasonCodeDispatchDead
}

func sameQueuedJobTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func queuedJobDispatchInput(job Job) (pluginsdk.JobInvocation, eventmodel.ExecutionContext, error) {
	dispatch, _ := job.Payload["dispatch"].(map[string]any)
	replyData, _ := job.Payload["reply_handle"].(map[string]any)
	reply, err := queuedJobReplyHandle(replyData)
	if err != nil {
		return pluginsdk.JobInvocation{}, eventmodel.ExecutionContext{}, err
	}
	invocation := pluginsdk.JobInvocation{
		ID:       job.ID,
		Type:     job.Type,
		Metadata: cloneAnyMap(dispatch),
	}
	if invocation.Metadata == nil {
		invocation.Metadata = map[string]any{}
	}
	executionContext := eventmodel.ExecutionContext{
		TraceID:       job.TraceID,
		EventID:       job.EventID,
		RunID:         job.RunID,
		CorrelationID: job.Correlation,
		Reply:         &reply,
	}
	if err := executionContext.Validate(); err != nil {
		return pluginsdk.JobInvocation{}, eventmodel.ExecutionContext{}, err
	}
	return invocation, executionContext, nil
}

func queuedJobReplyHandle(payload map[string]any) (eventmodel.ReplyHandle, error) {
	if len(payload) == 0 {
		return eventmodel.ReplyHandle{}, fmt.Errorf("reply_handle payload is required")
	}
	handle := eventmodel.ReplyHandle{
		Capability: stringMapValue(payload, "capability"),
		TargetID:   stringMapValue(payload, "target_id"),
		MessageID:  stringMapValue(payload, "message_id"),
	}
	if metadata, ok := payload["metadata"].(map[string]any); ok {
		handle.Metadata = cloneAnyMap(metadata)
	}
	if err := handle.Validate(); err != nil {
		return eventmodel.ReplyHandle{}, err
	}
	return handle, nil
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func stringMapValue(input map[string]any, key string) string {
	if value, ok := input[key].(string); ok {
		return value
	}
	return ""
}

func (r *InMemoryRuntime) DispatchSchedule(ctx context.Context, trigger pluginsdk.ScheduleTrigger, executionContext eventmodel.ExecutionContext) error {
	if trigger.ID == "" {
		return errors.New("schedule trigger id is required")
	}
	if trigger.Type == "" {
		return errors.New("schedule trigger type is required")
	}
	if err := executionContext.Validate(); err != nil {
		return fmt.Errorf("invalid execution context: %w", err)
	}
	executionContext = enrichExecutionContext(executionContext)
	requiredPermission := scheduleRequiredPermission(trigger)
	if requiredPermission != "" && !r.anyScheduleHandlerDeclaresPermission(requiredPermission) {
		pluginIDs := r.pluginsWithHandler("schedule")
		err := missingManifestPermissionError("schedule", requiredPermission, pluginIDs)
		r.log("error", "runtime schedule manifest permission gate failed", logContextFromExecutionContext(executionContext), manifestPermissionGateFields("schedule", requiredPermission, pluginIDs, map[string]any{"schedule_id": trigger.ID, "schedule_type": trigger.Type, "error": err.Error()}))
		return err
	}
	return r.dispatchToPluginsWithFilter(ctx, "schedule", executionContext,
		func(plugin pluginsdk.Plugin, _ eventmodel.ExecutionContext) bool {
			if plugin.Handlers.Schedule == nil {
				return false
			}
			if requiredPermission == "" {
				return true
			}
			return manifestDeclaresPermission(plugin.Manifest, requiredPermission)
		},
		func(plugin pluginsdk.Plugin, executionContext eventmodel.ExecutionContext) error {
			r.mu.RLock()
			scheduleAuth := r.scheduleAuth
			auditRecorder := r.audits
			r.mu.RUnlock()
			if scheduleAuth == nil {
				return nil
			}
			if err := scheduleAuth.AuthorizeSchedule(ctx, trigger, executionContext, plugin); err != nil {
				actor, permission := metadataAuthorizationFields(trigger.Metadata)
				r.recordAuthorizationDeniedAudit(auditRecorder, actor, permission, plugin.Manifest.ID, authorizationDeniedAuditReason(err))
				return err
			}
			return nil
		},
		map[string]any{"schedule_id": trigger.ID, "schedule_type": trigger.Type},
		func(plugin pluginsdk.Plugin, executionContext eventmodel.ExecutionContext) error {
			return r.host.DispatchSchedule(ctx, plugin, trigger, executionContext)
		},
	)
}

func (r *InMemoryRuntime) dispatchToPlugins(
	ctx context.Context,
	dispatchKind string,
	baseContext eventmodel.ExecutionContext,
	fields map[string]any,
	dispatch func(pluginsdk.Plugin, eventmodel.ExecutionContext) error,
) error {
	return r.dispatchToPluginsWithFilter(ctx, dispatchKind, baseContext, nil, nil, fields, dispatch)
}

func (r *InMemoryRuntime) dispatchToPluginsWithFilter(
	ctx context.Context,
	dispatchKind string,
	baseContext eventmodel.ExecutionContext,
	shouldDispatch func(pluginsdk.Plugin, eventmodel.ExecutionContext) bool,
	authorize func(pluginsdk.Plugin, eventmodel.ExecutionContext) error,
	fields map[string]any,
	dispatch func(pluginsdk.Plugin, eventmodel.ExecutionContext) error,
) error {
	finishDispatchSpan := r.tracer.StartSpan(baseContext.TraceID, "runtime.dispatch", baseContext.EventID, "", baseContext.RunID, baseContext.CorrelationID, mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind}))
	defer finishDispatchSpan()
	r.log("info", "runtime dispatch started", logContextFromExecutionContext(baseContext), BaselineLogFields("runtime", "dispatch."+dispatchKind, mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind})))

	manifests := r.plugins.List()
	if len(manifests) == 0 {
		r.log("error", "runtime dispatch failed", logContextFromExecutionContext(baseContext), FailureLogFields("runtime", "dispatch."+dispatchKind, errors.New("no plugins registered for dispatch"), "dispatch_no_plugins", mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind})))
		return errors.New("no plugins registered for dispatch")
	}

	var delivered bool
	var dispatchErr error
	for _, manifest := range manifests {
		plugin, err := r.plugins.Get(manifest.ID)
		if err != nil {
			return err
		}
		if !r.pluginEnabled(manifest.ID) {
			continue
		}

		executionContext := baseContext
		executionContext.PluginID = manifest.ID
		if shouldDispatch != nil && !shouldDispatch(plugin, executionContext) {
			continue
		}
		if authorize != nil {
			if err := authorize(plugin, executionContext); err != nil {
				r.log("error", "runtime dispatch authorization failed", logContextFromExecutionContext(executionContext), FailureLogFields("runtime", "dispatch."+dispatchKind+".authorize", err, authorizationDeniedAuditReason(err), mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind})))
				r.recordDispatch(DispatchResult{PluginID: manifest.ID, Kind: dispatchKind, Success: false, Error: fmt.Sprintf("dispatch authorization %q: %v", manifest.ID, err)})
				dispatchErr = err
				continue
			}
		}

		invokeStarted := time.Now()
		finishInvokeSpan := r.tracer.StartSpan(executionContext.TraceID, "plugin.invoke", executionContext.EventID, manifest.ID, executionContext.RunID, executionContext.CorrelationID, mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind, "parent_span_name": "runtime.dispatch"}))
		r.log("info", "plugin dispatch started", logContextFromExecutionContext(executionContext), BaselineLogFields("runtime", "plugin.dispatch."+dispatchKind, mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind})))

		if err := r.supervisor.EnsurePlugin(ctx, manifest.ID); err != nil {
			finishInvokeSpan()
			r.metrics.RecordPluginError(manifest.ID)
			r.metrics.RecordHandlerLatency(manifest.ID, time.Since(invokeStarted))
			r.log("error", "plugin dispatch failed", logContextFromExecutionContext(executionContext), FailureLogFields("runtime", "plugin.dispatch."+dispatchKind, err, "plugin_supervisor_failed", mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind})))
			r.recordDispatch(DispatchResult{PluginID: manifest.ID, Kind: dispatchKind, Success: false, Error: fmt.Sprintf("supervisor ensure plugin %q: %v", manifest.ID, err)})
			dispatchErr = err
			continue
		}

		if err := dispatch(plugin, executionContext); err != nil {
			finishInvokeSpan()
			r.metrics.RecordPluginError(manifest.ID)
			r.metrics.RecordHandlerLatency(manifest.ID, time.Since(invokeStarted))
			r.log("error", "plugin dispatch failed", logContextFromExecutionContext(executionContext), FailureLogFields("runtime", "plugin.dispatch."+dispatchKind, err, "plugin_dispatch_failed", mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind})))
			r.recordDispatch(DispatchResult{PluginID: manifest.ID, Kind: dispatchKind, Success: false, Error: fmt.Sprintf("plugin host dispatch %q: %v", manifest.ID, err)})
			dispatchErr = err
			continue
		}
		finishInvokeSpan()
		r.metrics.RecordHandlerLatency(manifest.ID, time.Since(invokeStarted))
		r.log("info", "plugin dispatch completed", logContextFromExecutionContext(executionContext), BaselineLogFields("runtime", "plugin.dispatch."+dispatchKind, mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind})))

		delivered = true
		r.recordDispatch(DispatchResult{PluginID: manifest.ID, Kind: dispatchKind, Success: true})
	}

	if !delivered {
		r.log("error", "runtime dispatch failed", logContextFromExecutionContext(baseContext), FailureLogFields("runtime", "dispatch."+dispatchKind, errors.New("dispatch completed with no successful plugin deliveries"), "dispatch_no_deliveries", mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind})))
		if dispatchErr != nil {
			return fmt.Errorf("dispatch completed with no successful plugin deliveries: %w", dispatchErr)
		}
		return errors.New("dispatch completed with no successful plugin deliveries")
	}
	r.log("info", "runtime dispatch completed", logContextFromExecutionContext(baseContext), BaselineLogFields("runtime", "dispatch."+dispatchKind, mergeFields(fields, map[string]any{"dispatch_kind": dispatchKind})))

	return nil
}

func (r *InMemoryRuntime) pluginEnabled(pluginID string) bool {
	r.mu.RLock()
	source := r.pluginStates
	r.mu.RUnlock()
	if source == nil {
		return true
	}
	return source.PluginEnabled(pluginID)
}

func (r *InMemoryRuntime) EnqueueJob(_ context.Context, job pluginsdk.JobInvocation) error {
	if job.ID == "" {
		return errors.New("job id is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobQueue = append(r.jobQueue, job)
	return nil
}

func (r *InMemoryRuntime) Schedule(_ context.Context, trigger pluginsdk.ScheduleTrigger) error {
	if trigger.ID == "" {
		return errors.New("schedule trigger id is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.schedules = append(r.schedules, trigger)
	return nil
}

func (r *InMemoryRuntime) Persist(_ context.Context, record Record) error {
	if record.Kind == "" {
		return errors.New("record kind is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return nil
}

func (r *InMemoryRuntime) Observe(_ context.Context, observation Observation) error {
	if observation.Name == "" {
		return errors.New("observation name is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations = append(r.observations, observation)
	return nil
}

func (r *InMemoryRuntime) Supervisor() Supervisor {
	return r.supervisor
}

func (r *InMemoryRuntime) PluginHost() PluginHost {
	return r.host
}

func (r *InMemoryRuntime) RegisteredAdapters() []AdapterRegistration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	items := make([]AdapterRegistration, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		items = append(items, adapter)
	}
	return items
}

func (r *InMemoryRuntime) EnqueuedJobs() []pluginsdk.JobInvocation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]pluginsdk.JobInvocation(nil), r.jobQueue...)
}

func (r *InMemoryRuntime) ScheduledTriggers() []pluginsdk.ScheduleTrigger {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]pluginsdk.ScheduleTrigger(nil), r.schedules...)
}

func (r *InMemoryRuntime) PersistedRecords() []Record {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Record(nil), r.records...)
}

func (r *InMemoryRuntime) Observations() []Observation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Observation(nil), r.observations...)
}

func (r *InMemoryRuntime) DispatchResults() []DispatchResult {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]DispatchResult(nil), r.dispatches...)
}

func (r *InMemoryRuntime) recordDispatch(result DispatchResult) {
	if result.At.IsZero() {
		result.At = time.Now().UTC()
	}
	var recorder DispatchResultRecorder
	r.mu.Lock()
	r.dispatches = append(r.dispatches, result)
	recorder = r.dispatchRecorder
	r.mu.Unlock()
	if recorder == nil {
		return
	}
	if err := recorder.RecordDispatchResult(result); err != nil {
		r.log("error", "persist plugin dispatch snapshot failed", LogContext{PluginID: result.PluginID}, FailureLogFields("runtime", "dispatch.snapshot.persist", err, "dispatch_snapshot_persist_failed", map[string]any{"dispatch_kind": result.Kind}))
	}
}

func (r *InMemoryRuntime) log(level, message string, ctx LogContext, fields map[string]any) {
	if r.logger == nil {
		return
	}
	_ = r.logger.Log(level, message, ctx, fields)
}

func logContextFromExecutionContext(ctx eventmodel.ExecutionContext) LogContext {
	return LogContext{
		TraceID:       ctx.TraceID,
		EventID:       ctx.EventID,
		PluginID:      ctx.PluginID,
		RunID:         ctx.RunID,
		CorrelationID: ctx.CorrelationID,
	}
}

func mergeFields(base map[string]any, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	merged := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func enrichExecutionContext(ctx eventmodel.ExecutionContext) eventmodel.ExecutionContext {
	if ctx.RunID == "" && ctx.EventID != "" {
		ctx.RunID = "run-" + ctx.EventID
	}
	if ctx.CorrelationID == "" {
		ctx.CorrelationID = ctx.EventID
	}
	return ctx
}

type NoopSupervisor struct{}

func (NoopSupervisor) EnsurePlugin(context.Context, string) error {
	return nil
}

type DirectPluginHost struct{}

func (DirectPluginHost) DispatchEvent(_ context.Context, plugin pluginsdk.Plugin, event eventmodel.Event, executionContext eventmodel.ExecutionContext) error {
	if plugin.Handlers.Event == nil {
		return nil
	}
	return plugin.Handlers.Event.OnEvent(event, executionContext)
}

func (DirectPluginHost) DispatchCommand(_ context.Context, plugin pluginsdk.Plugin, command eventmodel.CommandInvocation, executionContext eventmodel.ExecutionContext) error {
	if plugin.Handlers.Command == nil {
		return nil
	}
	return plugin.Handlers.Command.OnCommand(command, executionContext)
}

func (DirectPluginHost) DispatchJob(_ context.Context, plugin pluginsdk.Plugin, job pluginsdk.JobInvocation, executionContext eventmodel.ExecutionContext) error {
	if plugin.Handlers.Job == nil {
		return nil
	}
	return plugin.Handlers.Job.OnJob(job, executionContext)
}

func (DirectPluginHost) DispatchSchedule(_ context.Context, plugin pluginsdk.Plugin, trigger pluginsdk.ScheduleTrigger, executionContext eventmodel.ExecutionContext) error {
	if plugin.Handlers.Schedule == nil {
		return nil
	}
	return plugin.Handlers.Schedule.OnSchedule(trigger, executionContext)
}
