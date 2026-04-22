package runtimecore

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

const (
	supportedSubprocessPluginAPIVersion = "v0"
	currentRuntimeVersion               = "0.1.0"
)

const (
	defaultHandshakeTimeout = 2 * time.Second
	defaultResponseTimeout  = 2 * time.Second
)

type SubprocessPluginHost struct {
	commandFactory                func(context.Context) (*exec.Cmd, error)
	mu                            sync.Mutex
	captureMu                     sync.Mutex
	processes                     map[string]*subprocessProcess
	stdoutLines                   []string
	stderrLines                   []string
	replyTextCallback             func(eventmodel.ReplyHandle, string) error
	workflowStartOrResumeCallback func(context.Context, SubprocessWorkflowStartOrResumeRequest) (WorkflowTransition, error)
	now                           func() time.Time
	maxCaptureLines               int
	handshakeTimeout              time.Duration
	responseTimeout               time.Duration
	restartBudgetLimit            int
	restartBudgetWindow           time.Duration
	restartFailures               map[string][]time.Time
	logger                        *Logger
	tracer                        *TraceRecorder
	metrics                       *MetricsRegistry
}

type subprocessProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

type hostRequest struct {
	Type            string          `json:"type"`
	PluginID        string          `json:"plugin_id,omitempty"`
	Event           json.RawMessage `json:"event,omitempty"`
	Command         json.RawMessage `json:"command,omitempty"`
	Job             json.RawMessage `json:"job,omitempty"`
	ScheduleTrigger json.RawMessage `json:"schedule_trigger,omitempty"`
	InstanceConfig  json.RawMessage `json:"instance_config,omitempty"`
}

type hostResponse struct {
	Type                  string                                   `json:"type"`
	Status                string                                   `json:"status,omitempty"`
	Message               string                                   `json:"message,omitempty"`
	Error                 string                                   `json:"error,omitempty"`
	Callback              string                                   `json:"callback,omitempty"`
	ReplyText             *subprocessReplyTextCallback             `json:"reply_text,omitempty"`
	WorkflowStartOrResume *subprocessWorkflowStartOrResumeCallback `json:"workflow_start_or_resume,omitempty"`
}

type subprocessReplyTextCallback struct {
	Handle eventmodel.ReplyHandle `json:"handle"`
	Text   string                 `json:"text"`
}

type SubprocessWorkflowStartOrResumeRequest struct {
	WorkflowID string   `json:"workflow_id"`
	PluginID   string   `json:"plugin_id,omitempty"`
	TraceID    string   `json:"trace_id,omitempty"`
	EventType  string   `json:"event_type"`
	EventID    string   `json:"event_id,omitempty"`
	RunID      string   `json:"run_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	Initial    Workflow `json:"initial"`
}

type subprocessWorkflowStartOrResumeCallback struct {
	WorkflowID string   `json:"workflow_id"`
	PluginID   string   `json:"plugin_id,omitempty"`
	TraceID    string   `json:"trace_id,omitempty"`
	EventType  string   `json:"event_type"`
	EventID    string   `json:"event_id,omitempty"`
	RunID      string   `json:"run_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	Initial    Workflow `json:"initial"`
}

type hostCallbackResult struct {
	Type                  string              `json:"type"`
	Status                string              `json:"status,omitempty"`
	Error                 string              `json:"error,omitempty"`
	WorkflowStartOrResume *WorkflowTransition `json:"workflow_start_or_resume,omitempty"`
}

type subprocessFailureStage string

const (
	subprocessFailureStageStart     subprocessFailureStage = "start"
	subprocessFailureStageHandshake subprocessFailureStage = "handshake"
	subprocessFailureStageDispatch  subprocessFailureStage = "dispatch"
)

type subprocessFailureReason string

const (
	subprocessFailureReasonManifestModeMismatch            subprocessFailureReason = "manifest_mode_mismatch"
	subprocessFailureReasonManifestUnsupportedAPI          subprocessFailureReason = "manifest_unsupported_api_version"
	subprocessFailureReasonManifestUnsupportedRuntime      subprocessFailureReason = "manifest_unsupported_runtime_version"
	subprocessFailureReasonManifestMissingEntry            subprocessFailureReason = "manifest_missing_entry_target"
	subprocessFailureReasonManifestInvalidConfigSchema     subprocessFailureReason = "manifest_invalid_config_schema"
	subprocessFailureReasonManifestMissingRequiredConfig   subprocessFailureReason = "manifest_missing_required_config"
	subprocessFailureReasonManifestInvalidConfigProperty   subprocessFailureReason = "manifest_invalid_config_property"
	subprocessFailureReasonInstanceConfigMissingRequired   subprocessFailureReason = "instance_config_missing_required_value"
	subprocessFailureReasonInstanceConfigValueTypeMismatch subprocessFailureReason = "instance_config_value_type_mismatch"
	subprocessFailureReasonInstanceConfigEnumValueOutOfSet subprocessFailureReason = "instance_config_enum_value_out_of_set"
	subprocessFailureReasonCrashAfterHandshake             subprocessFailureReason = "crash_after_handshake"
	subprocessFailureReasonResponseTimeout                 subprocessFailureReason = "response_timeout"
	subprocessFailureReasonLauncherGuardBlocked            subprocessFailureReason = "launcher_guard_blocked"
)

type subprocessCompatibilityFailure struct {
	manifest          pluginsdk.PluginManifest
	reason            subprocessFailureReason
	compatibilityRule string
	metadata          map[string]any
	detail            string
}

type subprocessRuntimeVersion struct {
	major int
	minor int
	patch int
}

func (e *subprocessCompatibilityFailure) Error() string {
	return e.detail
}

type subprocessInstanceConfigFailure struct {
	manifest          pluginsdk.PluginManifest
	reason            subprocessFailureReason
	compatibilityRule string
	metadata          map[string]any
	detail            string
}

func (e *subprocessInstanceConfigFailure) Error() string {
	return e.detail
}

func NewSubprocessPluginHost(commandFactory func(context.Context) *exec.Cmd) *SubprocessPluginHost {
	return newSubprocessPluginHost(wrapSubprocessCommandFactory(commandFactory), 200)
}

func NewSubprocessPluginHostWithCaptureLimit(commandFactory func(context.Context) *exec.Cmd, maxCaptureLines int) (*SubprocessPluginHost, error) {
	if maxCaptureLines < 1 {
		return nil, errors.New("max capture lines must be >= 1")
	}
	return newSubprocessPluginHost(wrapSubprocessCommandFactory(commandFactory), maxCaptureLines), nil
}

func NewSubprocessPluginHostWithErrorFactory(commandFactory func(context.Context) (*exec.Cmd, error)) *SubprocessPluginHost {
	return newSubprocessPluginHost(commandFactory, 200)
}

func newSubprocessPluginHost(commandFactory func(context.Context) (*exec.Cmd, error), maxCaptureLines int) *SubprocessPluginHost {
	return &SubprocessPluginHost{commandFactory: ensureSubprocessCommandFactory(commandFactory), processes: map[string]*subprocessProcess{}, restartFailures: map[string][]time.Time{}, now: time.Now().UTC, maxCaptureLines: maxCaptureLines, handshakeTimeout: defaultHandshakeTimeout, responseTimeout: defaultResponseTimeout}
}

func wrapSubprocessCommandFactory(commandFactory func(context.Context) *exec.Cmd) func(context.Context) (*exec.Cmd, error) {
	return func(ctx context.Context) (*exec.Cmd, error) {
		if commandFactory == nil {
			return nil, errors.New("subprocess command factory is required")
		}
		return commandFactory(ctx), nil
	}
}

func ensureSubprocessCommandFactory(commandFactory func(context.Context) (*exec.Cmd, error)) func(context.Context) (*exec.Cmd, error) {
	if commandFactory != nil {
		return commandFactory
	}
	return func(context.Context) (*exec.Cmd, error) {
		return nil, errors.New("subprocess command factory is required")
	}
}

func (h *SubprocessPluginHost) SetObservability(logger *Logger, tracer *TraceRecorder, metrics *MetricsRegistry) {
	if logger != nil {
		h.logger = logger
	}
	if tracer != nil {
		h.tracer = tracer
	}
	if metrics != nil {
		h.metrics = metrics
	}
}

func (h *SubprocessPluginHost) SetReplyTextCallback(callback func(eventmodel.ReplyHandle, string) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.replyTextCallback = callback
}

func (h *SubprocessPluginHost) SetWorkflowStartOrResumeCallback(callback func(context.Context, SubprocessWorkflowStartOrResumeRequest) (WorkflowTransition, error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.workflowStartOrResumeCallback = callback
}

func (h *SubprocessPluginHost) SetRestartBudget(limit int, window time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if limit < 0 {
		limit = 0
	}
	if window < 0 {
		window = 0
	}
	h.restartBudgetLimit = limit
	h.restartBudgetWindow = window
	if h.restartFailures == nil {
		h.restartFailures = map[string][]time.Time{}
	}
}

func (h *SubprocessPluginHost) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for pluginID, process := range h.processes {
		if process == nil || process.cmd == nil || process.cmd.Process == nil {
			delete(h.processes, pluginID)
			continue
		}
		_ = process.cmd.Process.Kill()
		_, _ = process.cmd.Process.Wait()
		delete(h.processes, pluginID)
	}
	return nil
}

func (h *SubprocessPluginHost) DispatchEvent(ctx context.Context, plugin pluginsdk.Plugin, event eventmodel.Event, executionContext eventmodel.ExecutionContext) (err error) {
	started := h.now()
	defer func() {
		h.recordSubprocessDispatchMetrics(plugin.Manifest.ID, "event", started, err)
	}()
	if strings.TrimSpace(executionContext.PluginID) == "" {
		executionContext.PluginID = plugin.Manifest.ID
	}
	executionContext = enrichExecutionContext(executionContext)
	if err = validateSubprocessPlugin(plugin.Manifest); err != nil {
		h.observeCompatibilityFailure(plugin.Manifest.ID, executionContext, "event", err)
		return err
	}
	var payload []byte
	payload, err = json.Marshal(struct {
		Event eventmodel.Event            `json:"event"`
		Ctx   eventmodel.ExecutionContext `json:"ctx"`
	}{Event: event, Ctx: executionContext})
	if err != nil {
		return err
	}
	var request hostRequest
	request, err = buildSubprocessHostRequest("event", payload, plugin)
	if err != nil {
		h.observeInstanceConfigFailure(plugin.Manifest.ID, executionContext, "event", err)
		return err
	}
	return h.dispatchRequest(ctx, plugin.Manifest.ID, request, executionContext)
}

func (h *SubprocessPluginHost) DispatchCommand(ctx context.Context, plugin pluginsdk.Plugin, command eventmodel.CommandInvocation, executionContext eventmodel.ExecutionContext) (err error) {
	started := h.now()
	defer func() {
		h.recordSubprocessDispatchMetrics(plugin.Manifest.ID, "command", started, err)
	}()
	if strings.TrimSpace(executionContext.PluginID) == "" {
		executionContext.PluginID = plugin.Manifest.ID
	}
	executionContext = enrichExecutionContext(executionContext)
	if err = validateSubprocessPlugin(plugin.Manifest); err != nil {
		h.observeCompatibilityFailure(plugin.Manifest.ID, executionContext, "command", err)
		return err
	}
	var payload []byte
	payload, err = json.Marshal(struct {
		Command eventmodel.CommandInvocation `json:"command"`
		Ctx     eventmodel.ExecutionContext  `json:"ctx"`
	}{Command: command, Ctx: executionContext})
	if err != nil {
		return err
	}
	var request hostRequest
	request, err = buildSubprocessHostRequest("command", payload, plugin)
	if err != nil {
		h.observeInstanceConfigFailure(plugin.Manifest.ID, executionContext, "command", err)
		return err
	}
	return h.dispatchRequest(ctx, plugin.Manifest.ID, request, executionContext)
}

func (h *SubprocessPluginHost) DispatchJob(ctx context.Context, plugin pluginsdk.Plugin, job pluginsdk.JobInvocation, executionContext eventmodel.ExecutionContext) (err error) {
	started := h.now()
	defer func() {
		h.recordSubprocessDispatchMetrics(plugin.Manifest.ID, "job", started, err)
	}()
	if strings.TrimSpace(executionContext.PluginID) == "" {
		executionContext.PluginID = plugin.Manifest.ID
	}
	executionContext = enrichExecutionContext(executionContext)
	if err = validateSubprocessPlugin(plugin.Manifest); err != nil {
		h.observeCompatibilityFailure(plugin.Manifest.ID, executionContext, "job", err)
		return err
	}
	var payload []byte
	payload, err = json.Marshal(struct {
		Job pluginsdk.JobInvocation     `json:"job"`
		Ctx eventmodel.ExecutionContext `json:"ctx"`
	}{Job: job, Ctx: executionContext})
	if err != nil {
		return err
	}
	var request hostRequest
	request, err = buildSubprocessHostRequest("job", payload, plugin)
	if err != nil {
		h.observeInstanceConfigFailure(plugin.Manifest.ID, executionContext, "job", err)
		return err
	}
	return h.dispatchRequest(ctx, plugin.Manifest.ID, request, executionContext)
}

func (h *SubprocessPluginHost) DispatchSchedule(ctx context.Context, plugin pluginsdk.Plugin, trigger pluginsdk.ScheduleTrigger, executionContext eventmodel.ExecutionContext) (err error) {
	started := h.now()
	defer func() {
		h.recordSubprocessDispatchMetrics(plugin.Manifest.ID, "schedule", started, err)
	}()
	if strings.TrimSpace(executionContext.PluginID) == "" {
		executionContext.PluginID = plugin.Manifest.ID
	}
	executionContext = enrichExecutionContext(executionContext)
	if err = validateSubprocessPlugin(plugin.Manifest); err != nil {
		h.observeCompatibilityFailure(plugin.Manifest.ID, executionContext, "schedule", err)
		return err
	}
	var payload []byte
	payload, err = json.Marshal(struct {
		Trigger pluginsdk.ScheduleTrigger   `json:"schedule_trigger"`
		Ctx     eventmodel.ExecutionContext `json:"ctx"`
	}{Trigger: trigger, Ctx: executionContext})
	if err != nil {
		return err
	}
	var request hostRequest
	request, err = buildSubprocessHostRequest("schedule", payload, plugin)
	if err != nil {
		h.observeInstanceConfigFailure(plugin.Manifest.ID, executionContext, "schedule", err)
		return err
	}
	return h.dispatchRequest(ctx, plugin.Manifest.ID, request, executionContext)
}

func buildSubprocessHostRequest(requestType string, payload json.RawMessage, plugin pluginsdk.Plugin) (hostRequest, error) {
	request := hostRequest{Type: requestType, PluginID: strings.TrimSpace(plugin.Manifest.ID)}
	switch requestType {
	case "event":
		request.Event = payload
	case "command":
		request.Command = payload
	case "job":
		request.Job = payload
	case "schedule":
		request.ScheduleTrigger = payload
	default:
		return hostRequest{}, fmt.Errorf("unsupported host request type %q", requestType)
	}
	instanceConfig, err := marshalSubprocessInstanceConfig(plugin.Manifest, plugin.InstanceConfig)
	if err != nil {
		return hostRequest{}, err
	}
	request.InstanceConfig = instanceConfig
	return request, nil
}

func marshalSubprocessInstanceConfig(manifest pluginsdk.PluginManifest, instanceConfig map[string]any) (json.RawMessage, error) {
	mergedConfig, err := normalizeSubprocessInstanceConfig(manifest, instanceConfig)
	if err != nil {
		return nil, err
	}
	if len(mergedConfig) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(mergedConfig)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func validateSubprocessInstanceConfig(manifest pluginsdk.PluginManifest, instanceConfig map[string]any) error {
	_, err := normalizeSubprocessInstanceConfig(manifest, instanceConfig)
	return err
}

func normalizeSubprocessInstanceConfig(manifest pluginsdk.PluginManifest, instanceConfig map[string]any) (map[string]any, error) {
	mergedConfig := cloneSubprocessConfigObject(instanceConfig)
	if manifest.ConfigSchema == nil {
		return mergedConfig, nil
	}
	rawProperties, ok := manifest.ConfigSchema["properties"].(map[string]any)
	if !ok {
		return mergedConfig, nil
	}
	if err := validateSubprocessTopLevelRequiredInstanceConfig(manifest, rawProperties, mergedConfig); err != nil {
		return nil, err
	}
	if len(mergedConfig) == 0 {
		return mergedConfig, nil
	}
	for name, rawProperty := range rawProperties {
		value, exists := mergedConfig[name]
		if !exists {
			continue
		}
		propertySchema, ok := rawProperty.(map[string]any)
		if !ok {
			continue
		}
		propertyType, _ := propertySchema["type"].(string)
		propertyType = strings.TrimSpace(propertyType)
		if propertyType == "" {
			continue
		}
		if propertyType == "object" {
			if err := validateNestedSubprocessInstanceConfigValueType(manifest, name, propertySchema, value); err != nil {
				return nil, err
			}
			continue
		}
		if err := validateSubprocessTopLevelInstanceConfigValueType(manifest, name, propertyType, value); err != nil {
			return nil, err
		}
	}
	return mergedConfig, nil
}

func validateSubprocessTopLevelRequiredInstanceConfig(manifest pluginsdk.PluginManifest, rawProperties map[string]any, instanceConfig map[string]any) error {
	rawRequired, hasRequired := manifest.ConfigSchema["required"]
	if !hasRequired {
		return nil
	}
	requiredFields, ok := rawRequired.([]any)
	if !ok {
		return nil
	}
	for _, item := range requiredFields {
		name, ok := item.(string)
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := instanceConfig[name]; exists {
			continue
		}
		metadata := map[string]any{"property_name": name}
		if propertySchema, ok := rawProperties[name].(map[string]any); ok {
			if propertyType, _ := propertySchema["type"].(string); strings.TrimSpace(propertyType) != "" {
				metadata["declared_type"] = strings.TrimSpace(propertyType)
			}
		}
		return &subprocessInstanceConfigFailure{
			manifest:          manifest,
			reason:            subprocessFailureReasonInstanceConfigMissingRequired,
			compatibilityRule: "instance_config_top_level_required",
			metadata:          metadata,
			detail:            fmt.Sprintf("plugin %q instance config required property %q must be provided", manifest.ID, name),
		}
	}
	return nil
}

func validateSubprocessTopLevelInstanceConfigValueType(manifest pluginsdk.PluginManifest, propertyName string, propertyType string, value any) error {
	if subprocessConfigValueMatchesDeclaredType(propertyType, value) {
		return nil
	}
	actualType := describeSubprocessConfigValueType(value)
	return &subprocessInstanceConfigFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonInstanceConfigValueTypeMismatch,
		compatibilityRule: "instance_config_top_level_value_type",
		metadata: map[string]any{
			"property_name": propertyName,
			"declared_type": propertyType,
			"actual_type":   actualType,
		},
		detail: fmt.Sprintf("plugin %q instance config property %q value type must match declared type %q, got %q", manifest.ID, propertyName, propertyType, actualType),
	}
}

func validateNestedSubprocessInstanceConfigValueType(manifest pluginsdk.PluginManifest, propertyName string, propertySchema map[string]any, value any) error {
	objectValue, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	rawProperties, ok := propertySchema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	for childName, rawChildProperty := range rawProperties {
		childValue, exists := objectValue[childName]
		if !exists {
			continue
		}
		childSchema, ok := rawChildProperty.(map[string]any)
		if !ok {
			continue
		}
		childType, _ := childSchema["type"].(string)
		childType = strings.TrimSpace(childType)
		if childType == "" {
			continue
		}
		if childType == "object" {
			if err := validateDeeperNestedSubprocessInstanceConfigValueType(manifest, propertyName, childName, childSchema, childValue); err != nil {
				return err
			}
			continue
		}
		actualType := describeSubprocessConfigValueType(childValue)
		if subprocessConfigValueMatchesDeclaredType(childType, childValue) {
			continue
		}
		propertyPath := fmt.Sprintf("%s.%s", propertyName, childName)
		return &subprocessInstanceConfigFailure{
			manifest:          manifest,
			reason:            subprocessFailureReasonInstanceConfigValueTypeMismatch,
			compatibilityRule: "instance_config_nested_value_type",
			metadata: map[string]any{
				"property_name":        propertyPath,
				"parent_property_name": propertyName,
				"nested_property_name": childName,
				"declared_type":        childType,
				"actual_type":          actualType,
			},
			detail: fmt.Sprintf("plugin %q nested instance config property %q value type must match declared type %q, got %q", manifest.ID, propertyPath, childType, actualType),
		}
	}
	return nil
}

func validateDeeperNestedSubprocessInstanceConfigValueType(manifest pluginsdk.PluginManifest, propertyName string, childName string, childSchema map[string]any, value any) error {
	objectValue, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if err := validateDeeperNestedSubprocessInstanceConfigMissingRequired(manifest, propertyName, childName, childSchema, objectValue); err != nil {
		return err
	}
	rawProperties, ok := childSchema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	parentPropertyPath := fmt.Sprintf("%s.%s", propertyName, childName)
	for grandchildName, rawGrandchildProperty := range rawProperties {
		grandchildSchema, ok := rawGrandchildProperty.(map[string]any)
		if !ok {
			continue
		}
		grandchildType, _ := grandchildSchema["type"].(string)
		grandchildType = strings.TrimSpace(grandchildType)
		if grandchildType == "" {
			continue
		}
		grandchildValue, exists := objectValue[grandchildName]
		if !exists {
			if grandchildType != "object" {
				continue
			}
			grandchildValue = map[string]any{}
			objectValue[grandchildName] = grandchildValue
			exists = true
		}
		if !exists {
			continue
		}
		if grandchildType == "object" {
			grandchildObject, ok := grandchildValue.(map[string]any)
			if !ok {
				return newSubprocessNestedInstanceConfigValueTypeFailure(manifest, []string{propertyName, childName, grandchildName}, grandchildType, grandchildValue)
			}
			if err := mergeAndValidateSubprocessInstanceConfigObjectBranch(manifest, []string{propertyName, childName, grandchildName}, grandchildSchema, grandchildObject); err != nil {
				return err
			}
			continue
		}
		actualType := describeSubprocessConfigValueType(grandchildValue)
		if !subprocessConfigValueMatchesDeclaredType(grandchildType, grandchildValue) {
			propertyPath := fmt.Sprintf("%s.%s", parentPropertyPath, grandchildName)
			return &subprocessInstanceConfigFailure{
				manifest:          manifest,
				reason:            subprocessFailureReasonInstanceConfigValueTypeMismatch,
				compatibilityRule: "instance_config_deeper_nested_value_type",
				metadata: map[string]any{
					"property_name":              propertyPath,
					"parent_property_name":       parentPropertyPath,
					"nested_property_name":       grandchildName,
					"root_property_name":         propertyName,
					"intermediate_property_name": childName,
					"declared_type":              grandchildType,
					"actual_type":                actualType,
				},
				detail: fmt.Sprintf("plugin %q nested instance config property %q value type must match declared type %q, got %q", manifest.ID, propertyPath, grandchildType, actualType),
			}
		}
		if err := validateDeeperNestedSubprocessInstanceConfigEnumValue(manifest, propertyName, childName, grandchildName, grandchildType, grandchildSchema, grandchildValue); err != nil {
			return err
		}
	}
	return nil
}

func validateDeeperNestedSubprocessInstanceConfigEnumValue(manifest pluginsdk.PluginManifest, propertyName string, childName string, grandchildName string, grandchildType string, grandchildSchema map[string]any, grandchildValue any) error {
	rawEnumValues, hasEnum := grandchildSchema["enum"]
	if !hasEnum {
		return nil
	}
	enumValues, ok := subprocessConfigEnumValues(rawEnumValues)
	if !ok || len(enumValues) == 0 {
		return nil
	}
	for _, enumValue := range enumValues {
		if subprocessConfigValuesEqual(grandchildType, enumValue, grandchildValue) {
			return nil
		}
	}
	parentPropertyPath := fmt.Sprintf("%s.%s", propertyName, childName)
	propertyPath := fmt.Sprintf("%s.%s", parentPropertyPath, grandchildName)
	return &subprocessInstanceConfigFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonInstanceConfigEnumValueOutOfSet,
		compatibilityRule: "instance_config_deeper_nested_enum",
		metadata: map[string]any{
			"property_name":              propertyPath,
			"parent_property_name":       parentPropertyPath,
			"nested_property_name":       grandchildName,
			"root_property_name":         propertyName,
			"intermediate_property_name": childName,
			"declared_type":              grandchildType,
			"actual_value":               grandchildValue,
			"enum_values":                enumValues,
		},
		detail: fmt.Sprintf("plugin %q nested instance config property %q value %s must be declared in enum %s for declared type %q", manifest.ID, propertyPath, describeSubprocessConfigValue(grandchildValue), describeSubprocessConfigValue(enumValues), grandchildType),
	}
}

func validateDeeperNestedSubprocessInstanceConfigMissingRequired(manifest pluginsdk.PluginManifest, propertyName string, childName string, childSchema map[string]any, objectValue map[string]any) error {
	rawRequired, hasRequired := childSchema["required"]
	if !hasRequired {
		return nil
	}
	requiredFields, ok := rawRequired.([]any)
	if !ok {
		return nil
	}
	parentPropertyPath := fmt.Sprintf("%s.%s", propertyName, childName)
	rawProperties, _ := childSchema["properties"].(map[string]any)
	for _, item := range requiredFields {
		grandchildName, ok := item.(string)
		if !ok {
			continue
		}
		grandchildName = strings.TrimSpace(grandchildName)
		if grandchildName == "" {
			continue
		}
		if _, exists := objectValue[grandchildName]; exists {
			continue
		}
		propertyPath := fmt.Sprintf("%s.%s", parentPropertyPath, grandchildName)
		metadata := map[string]any{
			"property_name":              propertyPath,
			"parent_property_name":       parentPropertyPath,
			"nested_property_name":       grandchildName,
			"root_property_name":         propertyName,
			"intermediate_property_name": childName,
		}
		if propertySchema, ok := rawProperties[grandchildName].(map[string]any); ok {
			if propertyType, _ := propertySchema["type"].(string); strings.TrimSpace(propertyType) != "" {
				metadata["declared_type"] = strings.TrimSpace(propertyType)
			}
		}
		return &subprocessInstanceConfigFailure{
			manifest:          manifest,
			reason:            subprocessFailureReasonInstanceConfigMissingRequired,
			compatibilityRule: "instance_config_deeper_nested_required",
			metadata:          metadata,
			detail:            fmt.Sprintf("plugin %q nested instance config required property %q must be provided", manifest.ID, propertyPath),
		}
	}
	return nil
}

func mergeAndValidateSubprocessInstanceConfigObjectBranch(manifest pluginsdk.PluginManifest, propertyPath []string, propertySchema map[string]any, objectValue map[string]any) error {
	if err := validateSubprocessNestedInstanceConfigRequired(manifest, propertyPath, propertySchema, objectValue); err != nil {
		return err
	}
	rawProperties, ok := propertySchema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	for childName, rawChildProperty := range rawProperties {
		childValue, exists := objectValue[childName]
		if !exists {
			continue
		}
		childSchema, ok := rawChildProperty.(map[string]any)
		if !ok {
			continue
		}
		childType, _ := childSchema["type"].(string)
		childType = strings.TrimSpace(childType)
		if childType == "" {
			continue
		}
		childPath := append(append([]string(nil), propertyPath...), childName)
		if childType == "object" {
			childObject, ok := childValue.(map[string]any)
			if !ok {
				return newSubprocessNestedInstanceConfigValueTypeFailure(manifest, childPath, childType, childValue)
			}
			if err := mergeAndValidateSubprocessInstanceConfigObjectBranch(manifest, childPath, childSchema, childObject); err != nil {
				return err
			}
			continue
		}
		if err := validateSubprocessNestedInstanceConfigLeafValue(manifest, childPath, childType, childSchema, childValue); err != nil {
			return err
		}
	}
	return nil
}

func validateSubprocessNestedInstanceConfigRequired(manifest pluginsdk.PluginManifest, propertyPath []string, propertySchema map[string]any, objectValue map[string]any) error {
	rawProperties, ok := propertySchema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	requiredFields := map[string]struct{}{}
	if rawRequired, hasRequired := propertySchema["required"]; hasRequired {
		if entries, ok := rawRequired.([]any); ok {
			for _, item := range entries {
				name, ok := item.(string)
				if !ok {
					continue
				}
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				requiredFields[name] = struct{}{}
			}
		}
	}
	for childName, rawChildProperty := range rawProperties {
		if _, exists := objectValue[childName]; exists {
			continue
		}
		childSchema, ok := rawChildProperty.(map[string]any)
		if !ok {
			continue
		}
		childType, _ := childSchema["type"].(string)
		childType = strings.TrimSpace(childType)
		if childType == "" || childType == "object" {
			continue
		}
		if defaultValue, ok := subprocessConfigLeafDefaultValue(childType, childSchema); ok {
			objectValue[childName] = defaultValue
			continue
		}
		if _, required := requiredFields[childName]; required {
			return newSubprocessNestedInstanceConfigRequiredFailure(manifest, append(append([]string(nil), propertyPath...), childName), childSchema)
		}
	}
	return nil
}

func validateSubprocessNestedInstanceConfigLeafValue(manifest pluginsdk.PluginManifest, propertyPath []string, propertyType string, propertySchema map[string]any, value any) error {
	if !subprocessConfigValueMatchesDeclaredType(propertyType, value) {
		return newSubprocessNestedInstanceConfigValueTypeFailure(manifest, propertyPath, propertyType, value)
	}
	return validateSubprocessNestedInstanceConfigEnumValue(manifest, propertyPath, propertyType, propertySchema, value)
}

func validateSubprocessNestedInstanceConfigEnumValue(manifest pluginsdk.PluginManifest, propertyPath []string, propertyType string, propertySchema map[string]any, value any) error {
	rawEnumValues, hasEnum := propertySchema["enum"]
	if !hasEnum {
		return nil
	}
	enumValues, ok := subprocessConfigEnumValues(rawEnumValues)
	if !ok || len(enumValues) == 0 {
		return nil
	}
	for _, enumValue := range enumValues {
		if subprocessConfigValuesEqual(propertyType, enumValue, value) {
			return nil
		}
	}
	return newSubprocessNestedInstanceConfigEnumFailure(manifest, propertyPath, propertyType, value, enumValues)
}

func newSubprocessNestedInstanceConfigValueTypeFailure(manifest pluginsdk.PluginManifest, propertyPath []string, propertyType string, value any) error {
	metadata := subprocessNestedInstanceConfigMetadata(propertyPath)
	actualType := describeSubprocessConfigValueType(value)
	metadata["declared_type"] = propertyType
	metadata["actual_type"] = actualType
	propertyPathText := strings.Join(propertyPath, ".")
	return &subprocessInstanceConfigFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonInstanceConfigValueTypeMismatch,
		compatibilityRule: subprocessNestedInstanceConfigCompatibilityRule("value_type", propertyPath),
		metadata:          metadata,
		detail:            fmt.Sprintf("plugin %q nested instance config property %q value type must match declared type %q, got %q", manifest.ID, propertyPathText, propertyType, actualType),
	}
}

func newSubprocessNestedInstanceConfigEnumFailure(manifest pluginsdk.PluginManifest, propertyPath []string, propertyType string, value any, enumValues []any) error {
	metadata := subprocessNestedInstanceConfigMetadata(propertyPath)
	metadata["declared_type"] = propertyType
	metadata["actual_value"] = value
	metadata["enum_values"] = enumValues
	propertyPathText := strings.Join(propertyPath, ".")
	return &subprocessInstanceConfigFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonInstanceConfigEnumValueOutOfSet,
		compatibilityRule: subprocessNestedInstanceConfigCompatibilityRule("enum", propertyPath),
		metadata:          metadata,
		detail:            fmt.Sprintf("plugin %q nested instance config property %q value %s must be declared in enum %s for declared type %q", manifest.ID, propertyPathText, describeSubprocessConfigValue(value), describeSubprocessConfigValue(enumValues), propertyType),
	}
}

func newSubprocessNestedInstanceConfigRequiredFailure(manifest pluginsdk.PluginManifest, propertyPath []string, propertySchema map[string]any) error {
	metadata := subprocessNestedInstanceConfigMetadata(propertyPath)
	if propertyType, _ := propertySchema["type"].(string); strings.TrimSpace(propertyType) != "" {
		metadata["declared_type"] = strings.TrimSpace(propertyType)
	}
	propertyPathText := strings.Join(propertyPath, ".")
	return &subprocessInstanceConfigFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonInstanceConfigMissingRequired,
		compatibilityRule: subprocessNestedInstanceConfigCompatibilityRule("required", propertyPath),
		metadata:          metadata,
		detail:            fmt.Sprintf("plugin %q nested instance config required property %q must be provided", manifest.ID, propertyPathText),
	}
}

func subprocessNestedInstanceConfigMetadata(propertyPath []string) map[string]any {
	metadata := map[string]any{
		"property_name":        strings.Join(propertyPath, "."),
		"nested_property_name": propertyPath[len(propertyPath)-1],
	}
	if len(propertyPath) > 1 {
		metadata["parent_property_name"] = strings.Join(propertyPath[:len(propertyPath)-1], ".")
	}
	if len(propertyPath) > 2 {
		metadata["root_property_name"] = propertyPath[0]
		metadata["intermediate_property_name"] = strings.Join(propertyPath[1:len(propertyPath)-1], ".")
	}
	return metadata
}

func subprocessNestedInstanceConfigCompatibilityRule(suffix string, propertyPath []string) string {
	if len(propertyPath) <= 2 {
		return "instance_config_nested_" + suffix
	}
	return "instance_config_deeper_nested_" + suffix
}

func subprocessConfigLeafDefaultValue(propertyType string, propertySchema map[string]any) (any, bool) {
	propertyType = strings.TrimSpace(propertyType)
	if propertyType == "" || propertyType == "object" {
		return nil, false
	}
	defaultValue, hasDefault := propertySchema["default"]
	if !hasDefault || !subprocessConfigValueMatchesDeclaredType(propertyType, defaultValue) {
		return nil, false
	}
	if rawEnumValues, hasEnum := propertySchema["enum"]; hasEnum {
		enumValues, ok := subprocessConfigEnumValues(rawEnumValues)
		if !ok || !subprocessConfigEnumContainsDefault(propertyType, enumValues, defaultValue) {
			return nil, false
		}
	}
	return cloneSubprocessConfigValue(defaultValue), true
}

func cloneSubprocessConfigObject(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneSubprocessConfigValue(value)
	}
	return cloned
}

func cloneSubprocessConfigValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSubprocessConfigObject(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneSubprocessConfigValue(item)
		}
		return cloned
	default:
		return typed
	}
}

func (h *SubprocessPluginHost) dispatchRequest(ctx context.Context, pluginID string, request hostRequest, executionContext eventmodel.ExecutionContext) (err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	finishSpan := h.startSpan(executionContext.TraceID, "plugin_host.dispatch", executionContext.EventID, pluginID, executionContext.RunID, executionContext.CorrelationID, map[string]any{"request_type": request.Type})
	defer finishSpan()
	h.log("info", "subprocess host dispatch started", logContextFromExecutionContext(executionContext), BaselineLogFields("plugin_host", "dispatch."+request.Type, map[string]any{"plugin_id": pluginID, "request_type": request.Type}))
	defer func() {
		if err != nil {
			h.log("error", "subprocess host dispatch failed", logContextFromExecutionContext(executionContext), FailureLogFields("plugin_host", "dispatch."+request.Type, err, "plugin_dispatch_failed", map[string]any{"plugin_id": pluginID, "request_type": request.Type}))
			return
		}
		h.clearRestartFailuresLocked(pluginID)
		h.log("info", "subprocess host dispatch completed", logContextFromExecutionContext(executionContext), BaselineLogFields("plugin_host", "dispatch."+request.Type, map[string]any{"plugin_id": pluginID, "request_type": request.Type}))
	}()

	if err := h.ensureProcess(ctx, pluginID, request.Type, executionContext); err != nil {
		h.log("error", "subprocess host ensure process failed", logContextFromExecutionContext(executionContext), FailureLogFields("plugin_host", "dispatch."+request.Type+".ensure_process", err, "ensure_process_failed", map[string]any{"plugin_id": pluginID, "request_type": request.Type}))
		return err
	}

	if err := h.writeRequest(pluginID, request); err != nil {
		h.log("warning", "subprocess host restarting after write failure", logContextFromExecutionContext(executionContext), FailureLogFields("plugin_host", "dispatch."+request.Type+".restart_after_write", err, "write_failed", map[string]any{"plugin_id": pluginID, "request_type": request.Type}))
		if restartErr := h.restartProcess(ctx, pluginID, request.Type, executionContext); restartErr != nil {
			err = fmt.Errorf("restart after write failure: %w", restartErr)
			return err
		}
		if err := h.writeRequest(pluginID, request); err != nil {
			err = fmt.Errorf("replay after restart failed: %w", err)
			return err
		}
	}

	response, err := h.readTerminalResponse(ctx, pluginID, executionContext, h.responseTimeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			h.observeSubprocessFailure(executionContext, pluginID, subprocessFailureStageDispatch, subprocessFailureReasonResponseTimeout, request.Type, err, nil)
			return err
		}
		if classifiedErr, observed := h.observeDispatchFailure(executionContext, pluginID, request.Type, err); observed {
			if restartErr := h.restartProcess(ctx, pluginID, request.Type, executionContext); restartErr == nil {
				if writeErr := h.writeRequest(pluginID, request); writeErr == nil {
					if recoveredResponse, recoveredErr := h.readTerminalResponse(ctx, pluginID, executionContext, h.responseTimeout); recoveredErr == nil {
						response = recoveredResponse
						return nil
					}
				}
			}
			err = classifiedErr
			return err
		}
		h.log("warning", "subprocess host restarting after read failure", logContextFromExecutionContext(executionContext), FailureLogFields("plugin_host", "dispatch."+request.Type+".restart_after_read", err, "read_failed", map[string]any{"plugin_id": pluginID, "request_type": request.Type}))
		if restartErr := h.restartProcess(ctx, pluginID, request.Type, executionContext); restartErr != nil {
			err = fmt.Errorf("restart after read failure: %w", restartErr)
			return err
		}
		if err := h.writeRequest(pluginID, request); err != nil {
			err = fmt.Errorf("replay after restart failed: %w", err)
			return err
		}
		response, err = h.readTerminalResponse(ctx, pluginID, executionContext, h.responseTimeout)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				h.observeSubprocessFailure(executionContext, pluginID, subprocessFailureStageDispatch, subprocessFailureReasonResponseTimeout, request.Type, err, map[string]any{"after_restart": true})
			}
			return err
		}
	}
	if response.Status != "ok" {
		err = errors.New(response.Error)
		return err
	}
	h.clearRestartFailuresLocked(pluginID)
	return nil
}

func (h *SubprocessPluginHost) HealthCheck(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := h.ensureProcess(ctx, "health", "health", eventmodel.ExecutionContext{}); err != nil {
		h.log("error", "subprocess host health ensure failed", LogContext{}, FailureLogFields("plugin_host", "health.ensure_process", err, "ensure_process_failed", nil))
		return err
	}
	if err := h.writeRequest("health", hostRequest{Type: "health"}); err != nil {
		return err
	}
	response, err := h.readResponse(ctx, "health", h.responseTimeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			h.log("error", "subprocess host health timed out", LogContext{}, FailureLogFields("plugin_host", "health.read", err, "response_timeout", nil))
			_ = h.restartProcess(ctx, "health", "health", eventmodel.ExecutionContext{})
			return err
		}
		h.log("warning", "subprocess host restarting after health failure", LogContext{}, FailureLogFields("plugin_host", "health.restart", err, "health_check_failed", nil))
		_ = h.restartProcess(ctx, "health", "health", eventmodel.ExecutionContext{})
		return err
	}
	if response.Status != "ok" {
		return fmt.Errorf("health check failed: %s", response.Error)
	}
	return nil
}

func (h *SubprocessPluginHost) StdoutLines() []string {
	h.captureMu.Lock()
	defer h.captureMu.Unlock()
	return append([]string(nil), h.stdoutLines...)
}

func (h *SubprocessPluginHost) StderrLines() []string {
	h.captureMu.Lock()
	defer h.captureMu.Unlock()
	return append([]string(nil), h.stderrLines...)
}

func (h *SubprocessPluginHost) ensureProcess(ctx context.Context, pluginID string, requestType string, executionContext eventmodel.ExecutionContext) error {
	process := h.processes[pluginID]
	if process != nil {
		if process.cmd.ProcessState == nil && process.cmd.Process != nil {
			if err := process.cmd.Process.Signal(os.Signal(syscall.Signal(0))); err == nil {
				return nil
			}
		}
		delete(h.processes, pluginID)
	}
	if err := h.checkRestartBudgetLocked(pluginID); err != nil {
		return h.observeProcessFailure(executionContext, pluginID, requestType, subprocessFailureStageStart, fmt.Errorf("subprocess launch guard blocked start: %w", err))
	}

	cmd, err := h.commandFactory(ctx)
	if err != nil {
		return h.observeProcessFailure(executionContext, pluginID, requestType, subprocessFailureStageStart, err)
	}
	if cmd == nil {
		return h.observeProcessFailure(executionContext, pluginID, requestType, subprocessFailureStageStart, errors.New("subprocess command factory returned nil command"))
	}
	h.log("info", "subprocess host starting process", logContextFromExecutionContext(executionContext), BaselineLogFields("plugin_host", "process.start", map[string]any{"plugin_id": pluginID}))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return h.observeProcessFailure(executionContext, pluginID, requestType, subprocessFailureStageStart, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return h.observeProcessFailure(executionContext, pluginID, requestType, subprocessFailureStageStart, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return h.observeProcessFailure(executionContext, pluginID, requestType, subprocessFailureStageStart, err)
	}
	if err := cmd.Start(); err != nil {
		return h.observeProcessFailure(executionContext, pluginID, requestType, subprocessFailureStageStart, err)
	}

	h.processes[pluginID] = &subprocessProcess{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	h.collectStream(bufio.NewScanner(stderr), &h.stderrLines)
	if err := h.performHandshake(ctx, pluginID); err != nil {
		if h.processes[pluginID] != nil && h.processes[pluginID].cmd.Process != nil {
			_ = h.processes[pluginID].cmd.Process.Kill()
			_, _ = h.processes[pluginID].cmd.Process.Wait()
		}
		delete(h.processes, pluginID)
		return h.observeProcessFailure(executionContext, pluginID, requestType, subprocessFailureStageHandshake, err)
	}
	return nil
}

func (h *SubprocessPluginHost) performHandshake(ctx context.Context, pluginID string) error {
	response, err := h.readResponse(ctx, pluginID, h.handshakeTimeout)
	if err != nil {
		return err
	}
	if response.Type != "handshake" || response.Status != "ok" {
		return fmt.Errorf("invalid handshake response: %+v", response)
	}
	h.log("info", "subprocess host handshake completed", LogContext{PluginID: pluginID}, BaselineLogFields("plugin_host", "process.handshake", map[string]any{"plugin_id": pluginID}))
	h.captureMu.Lock()
	h.stdoutLines = appendBounded(h.stdoutLines, response.Message, h.maxCaptureLines)
	h.captureMu.Unlock()
	return nil
}

func (h *SubprocessPluginHost) restartProcess(ctx context.Context, pluginID string, requestType string, executionContext eventmodel.ExecutionContext) error {
	h.log("warning", "subprocess host restarting process", logContextFromExecutionContext(executionContext), BaselineLogFields("plugin_host", "process.restart", map[string]any{"plugin_id": pluginID}))
	if h.processes[pluginID] != nil && h.processes[pluginID].cmd.Process != nil {
		_ = h.processes[pluginID].cmd.Process.Kill()
		_, _ = h.processes[pluginID].cmd.Process.Wait()
	}
	delete(h.processes, pluginID)
	return h.ensureProcess(ctx, pluginID, requestType, executionContext)
}

func (h *SubprocessPluginHost) observeProcessFailure(executionContext eventmodel.ExecutionContext, pluginID string, requestType string, stage subprocessFailureStage, err error) error {
	reason := classifyProcessFailureReason(stage, err)
	ctx := logContextFromExecutionContext(executionContext)
	fields := map[string]any{
		"plugin_id":     pluginID,
		"request_type":  requestType,
		"failure_stage": string(stage),
		"error":         err.Error(),
	}
	if reason != "" {
		fields["failure_reason"] = string(reason)
	}
	h.observeSubprocessFailure(executionContext, pluginID, stage, reason, requestType, err, nil)
	if h.logger != nil {
		h.logger.Log("error", "subprocess host process bootstrap failed", ctx, FailureLogFields("plugin_host", "process.bootstrap", err, string(reason), fields))
	}
	if h.tracer != nil {
		finish := h.tracer.StartSpan(ctx.TraceID, "plugin_host.process_bootstrap", ctx.EventID, pluginID, ctx.RunID, ctx.CorrelationID, fields)
		finish()
	}
	if reason != subprocessFailureReasonLauncherGuardBlocked {
		h.noteRestartFailureLocked(pluginID, reason)
	}
	return fmt.Errorf("subprocess %s failed: %w", stage, err)
}

func (h *SubprocessPluginHost) observeDispatchFailure(executionContext eventmodel.ExecutionContext, pluginID, requestType string, err error) (error, bool) {
	reason, ok := classifyDispatchFailureReason(err)
	if !ok {
		return nil, false
	}
	ctx := logContextFromExecutionContext(executionContext)
	fields := map[string]any{
		"plugin_id":      pluginID,
		"request_type":   requestType,
		"failure_stage":  string(subprocessFailureStageDispatch),
		"failure_reason": string(reason),
		"error":          err.Error(),
	}
	h.observeSubprocessFailure(executionContext, pluginID, subprocessFailureStageDispatch, reason, requestType, err, nil)
	h.log("error", "subprocess host dispatch failed after handshake", ctx, FailureLogFields("plugin_host", "dispatch."+requestType+".after_handshake", err, string(reason), fields))
	finish := h.startSpan(ctx.TraceID, "plugin_host.dispatch_failure", ctx.EventID, pluginID, ctx.RunID, ctx.CorrelationID, fields)
	finish()
	h.noteRestartFailureLocked(pluginID, reason)
	return fmt.Errorf("subprocess dispatch failed after handshake: %w", err), true
}

func classifyDispatchFailureReason(err error) (subprocessFailureReason, bool) {
	if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "file already closed") {
		return subprocessFailureReasonCrashAfterHandshake, true
	}
	return "", false
}

func classifyProcessFailureReason(stage subprocessFailureStage, err error) subprocessFailureReason {
	if err == nil {
		return ""
	}
	errorText := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(errorText, "launch guard") {
		return subprocessFailureReasonLauncherGuardBlocked
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(errorText, "subprocess response timeout") {
		return subprocessFailureReasonResponseTimeout
	}
	if stage == subprocessFailureStageHandshake && strings.Contains(errorText, "invalid handshake response") {
		return subprocessFailureReasonManifestUnsupportedAPI
	}
	return ""
}

func (h *SubprocessPluginHost) log(level, message string, ctx LogContext, fields map[string]any) {
	if h.logger == nil {
		return
	}
	_ = h.logger.Log(level, message, ctx, fields)
}

func (h *SubprocessPluginHost) startSpan(traceID, spanName, eventID, pluginID, runID, correlationID string, metadata map[string]any) func() {
	if h.tracer == nil {
		return func() {}
	}
	return h.tracer.StartSpan(traceID, spanName, eventID, pluginID, runID, correlationID, metadata)
}

func (h *SubprocessPluginHost) recordSubprocessDispatchMetrics(pluginID string, operation string, started time.Time, err error) {
	if h.metrics == nil {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	h.metrics.RecordSubprocessDispatch(pluginID, operation, outcome, h.now().Sub(started))
}

func (h *SubprocessPluginHost) observeSubprocessFailure(executionContext eventmodel.ExecutionContext, pluginID string, stage subprocessFailureStage, reason subprocessFailureReason, requestType string, err error, extraFields map[string]any) {
	if reason == "" {
		return
	}
	fields := map[string]any{
		"plugin_id":      pluginID,
		"failure_stage":  string(stage),
		"failure_reason": string(reason),
	}
	if requestType != "" {
		fields["request_type"] = requestType
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	for key, value := range extraFields {
		fields[key] = value
	}
	if h.metrics != nil {
		h.metrics.RecordSubprocessFailure(pluginID, requestType, string(stage), string(reason))
	}
	if stage == subprocessFailureStageDispatch && reason == subprocessFailureReasonResponseTimeout {
		h.log("error", "subprocess host dispatch timed out", logContextFromExecutionContext(executionContext), FailureLogFields("plugin_host", "dispatch."+requestType+".timeout", err, string(reason), fields))
	}
}

func (h *SubprocessPluginHost) checkRestartBudgetLocked(pluginID string) error {
	if h.restartBudgetLimit <= 0 || h.restartBudgetWindow <= 0 {
		return nil
	}
	recent := h.trimRestartFailuresLocked(pluginID)
	if len(recent) < h.restartBudgetLimit {
		return nil
	}
	return fmt.Errorf("restart budget exhausted for plugin %q: %d failures within %s", pluginID, len(recent), h.restartBudgetWindow)
}

func (h *SubprocessPluginHost) noteRestartFailureLocked(pluginID string, reason subprocessFailureReason) {
	if pluginID == "" || h.restartBudgetLimit <= 0 || h.restartBudgetWindow <= 0 {
		return
	}
	if reason == subprocessFailureReasonLauncherGuardBlocked {
		return
	}
	recent := h.trimRestartFailuresLocked(pluginID)
	recent = append(recent, h.now())
	h.restartFailures[pluginID] = recent
}

func (h *SubprocessPluginHost) clearRestartFailuresLocked(pluginID string) {
	if pluginID == "" || len(h.restartFailures) == 0 {
		return
	}
	delete(h.restartFailures, pluginID)
}

func (h *SubprocessPluginHost) trimRestartFailuresLocked(pluginID string) []time.Time {
	if pluginID == "" {
		return nil
	}
	current := h.restartFailures[pluginID]
	if h.restartBudgetWindow <= 0 {
		return append([]time.Time(nil), current...)
	}
	cutoff := h.now().Add(-h.restartBudgetWindow)
	recent := current[:0]
	for _, at := range current {
		if at.Before(cutoff) {
			continue
		}
		recent = append(recent, at)
	}
	if len(recent) == 0 {
		delete(h.restartFailures, pluginID)
		return nil
	}
	h.restartFailures[pluginID] = recent
	return append([]time.Time(nil), recent...)
}

func (h *SubprocessPluginHost) writeRequest(pluginID string, request hostRequest) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	_, err = io.WriteString(h.processes[pluginID].stdin, string(encoded)+"\n")
	return err
}

func (h *SubprocessPluginHost) readTerminalResponse(ctx context.Context, pluginID string, executionContext eventmodel.ExecutionContext, timeout time.Duration) (hostResponse, error) {
	for {
		response, err := h.readResponse(ctx, pluginID, timeout)
		if err != nil {
			return hostResponse{}, err
		}
		if response.Type != "callback" {
			if response.Type == "callback_result" {
				continue
			}
			return response, nil
		}
		if err := h.handleCallback(ctx, pluginID, executionContext, response); err != nil {
			return hostResponse{}, err
		}
	}
}

func (h *SubprocessPluginHost) handleCallback(ctx context.Context, pluginID string, executionContext eventmodel.ExecutionContext, response hostResponse) error {
	switch response.Callback {
	case "reply_text":
		return h.handleReplyTextCallback(pluginID, response)
	case "workflow_start_or_resume":
		return h.handleWorkflowStartOrResumeCallback(ctx, pluginID, executionContext, response)
	default:
		return fmt.Errorf("unsupported subprocess callback %q", response.Callback)
	}
}

func (h *SubprocessPluginHost) handleReplyTextCallback(pluginID string, response hostResponse) error {
	if response.ReplyText == nil {
		return fmt.Errorf("subprocess callback %q missing payload", "reply_text")
	}
	var callbackErr error
	if h.replyTextCallback == nil {
		callbackErr = errors.New("subprocess reply_text callback is not configured")
	} else {
		callbackErr = h.replyTextCallback(response.ReplyText.Handle, response.ReplyText.Text)
	}
	return h.writeCallbackResult(pluginID, callbackErr, nil)
}

func (h *SubprocessPluginHost) handleWorkflowStartOrResumeCallback(ctx context.Context, pluginID string, executionContext eventmodel.ExecutionContext, response hostResponse) error {
	if response.WorkflowStartOrResume == nil {
		return fmt.Errorf("subprocess callback %q missing payload", "workflow_start_or_resume")
	}
	request := SubprocessWorkflowStartOrResumeRequest{
		WorkflowID: strings.TrimSpace(response.WorkflowStartOrResume.WorkflowID),
		PluginID:   pluginID,
		TraceID:    strings.TrimSpace(response.WorkflowStartOrResume.TraceID),
		EventType:  strings.TrimSpace(response.WorkflowStartOrResume.EventType),
		EventID:    strings.TrimSpace(response.WorkflowStartOrResume.EventID),
		RunID:      strings.TrimSpace(response.WorkflowStartOrResume.RunID),
		CorrelationID: strings.TrimSpace(response.WorkflowStartOrResume.CorrelationID),
		Initial:    response.WorkflowStartOrResume.Initial,
	}
	if request.EventID == "" {
		request.EventID = executionContext.EventID
	}
	if request.TraceID == "" {
		request.TraceID = executionContext.TraceID
	}
	if request.RunID == "" {
		request.RunID = executionContext.RunID
	}
	if request.CorrelationID == "" {
		request.CorrelationID = executionContext.CorrelationID
	}
	ctx = WithWorkflowObservabilityContext(ctx, WorkflowObservabilityContext{
		TraceID:       request.TraceID,
		EventID:       request.EventID,
		PluginID:      request.PluginID,
		RunID:         request.RunID,
		CorrelationID: request.CorrelationID,
	})
	var (
		callbackErr error
		transition  WorkflowTransition
	)
	if h.workflowStartOrResumeCallback == nil {
		callbackErr = errors.New("subprocess workflow_start_or_resume callback is not configured")
	} else {
		transition, callbackErr = h.workflowStartOrResumeCallback(ctx, request)
	}
	if callbackErr != nil {
		return h.writeCallbackResult(pluginID, callbackErr, nil)
	}
	return h.writeCallbackResult(pluginID, nil, &transition)
}

func (h *SubprocessPluginHost) writeCallbackResult(pluginID string, callbackErr error, transition *WorkflowTransition) error {
	result := hostCallbackResult{Type: "callback_result", Status: "ok"}
	if callbackErr != nil {
		result.Status = "error"
		result.Error = callbackErr.Error()
	} else if transition != nil {
		cloned := *transition
		result.WorkflowStartOrResume = &cloned
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = io.WriteString(h.processes[pluginID].stdin, string(encoded)+"\n")
	return err
}

func (h *SubprocessPluginHost) readResponse(ctx context.Context, pluginID string, timeout time.Duration) (hostResponse, error) {
	readCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		readCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	type result struct {
		line string
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		line, err := h.processes[pluginID].stdout.ReadString('\n')
		resultCh <- result{line: line, err: err}
	}()

	var line string
	select {
	case <-readCtx.Done():
		if h.processes[pluginID] != nil && h.processes[pluginID].cmd.Process != nil {
			_ = h.processes[pluginID].cmd.Process.Kill()
			_, _ = h.processes[pluginID].cmd.Process.Wait()
		}
		delete(h.processes, pluginID)
		return hostResponse{}, fmt.Errorf("subprocess response timeout: %w", readCtx.Err())
	case outcome := <-resultCh:
		if outcome.err != nil {
			return hostResponse{}, outcome.err
		}
		line = outcome.line
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return hostResponse{}, errors.New("empty subprocess response")
	}

	var response hostResponse
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		return hostResponse{}, err
	}
	h.captureMu.Lock()
	h.stdoutLines = appendBounded(h.stdoutLines, line, h.maxCaptureLines)
	h.captureMu.Unlock()
	return response, nil
}

func (h *SubprocessPluginHost) collectStream(scanner *bufio.Scanner, target *[]string) {
	go func() {
		for scanner.Scan() {
			h.captureMu.Lock()
			*target = appendBounded(*target, scanner.Text(), h.maxCaptureLines)
			h.captureMu.Unlock()
		}
	}()
}

func validateSubprocessPlugin(manifest pluginsdk.PluginManifest) error {
	if manifest.Mode != pluginsdk.ModeSubprocess {
		return &subprocessCompatibilityFailure{
			manifest: manifest,
			reason:   subprocessFailureReasonManifestModeMismatch,
			detail:   fmt.Sprintf("plugin %q is not compatible with subprocess host: mode must be %q", manifest.ID, pluginsdk.ModeSubprocess),
		}
	}
	if manifest.APIVersion != supportedSubprocessPluginAPIVersion {
		return &subprocessCompatibilityFailure{
			manifest: manifest,
			reason:   subprocessFailureReasonManifestUnsupportedAPI,
			detail:   fmt.Sprintf("plugin %q is not compatible with subprocess host: unsupported apiVersion %q", manifest.ID, manifest.APIVersion),
		}
	}
	if err := validateSubprocessRuntimeVersion(manifest); err != nil {
		return err
	}
	if strings.TrimSpace(manifest.Entry.Binary) == "" && strings.TrimSpace(manifest.Entry.Module) == "" {
		return &subprocessCompatibilityFailure{
			manifest: manifest,
			reason:   subprocessFailureReasonManifestMissingEntry,
			detail:   fmt.Sprintf("plugin %q is not compatible with subprocess host: subprocess entry target is required", manifest.ID),
		}
	}
	if err := validateSubprocessConfigSchema(manifest); err != nil {
		return err
	}
	return nil
}

func validateSubprocessRuntimeVersion(manifest pluginsdk.PluginManifest) error {
	requiredRange, ok := subprocessManifestRuntimeVersionRange(manifest)
	if !ok {
		return nil
	}
	requiredRange = strings.TrimSpace(requiredRange)
	if requiredRange == "" {
		return nil
	}
	compatible, err := subprocessRuntimeVersionSatisfiesRange(currentRuntimeVersion, requiredRange)
	if err == nil && compatible {
		return nil
	}
	detail := fmt.Sprintf("plugin %q is not compatible with subprocess host: current runtime version %q does not satisfy required runtimeVersionRange %q", manifest.ID, currentRuntimeVersion, requiredRange)
	if err != nil {
		detail = fmt.Sprintf("plugin %q is not compatible with subprocess host: current runtime version %q cannot be evaluated against required runtimeVersionRange %q", manifest.ID, currentRuntimeVersion, requiredRange)
	}
	return &subprocessCompatibilityFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonManifestUnsupportedRuntime,
		compatibilityRule: "runtime_version",
		metadata: map[string]any{
			"current_runtime_version":        currentRuntimeVersion,
			"required_runtime_version_range": requiredRange,
		},
		detail: detail,
	}
}

func subprocessManifestRuntimeVersionRange(manifest pluginsdk.PluginManifest) (string, bool) {
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		return "", false
	}
	var decoded map[string]any
	if err := json.Unmarshal(encodedManifest, &decoded); err != nil {
		return "", false
	}
	rawPublish, ok := decoded["publish"].(map[string]any)
	if !ok {
		return "", false
	}
	runtimeVersionRange, ok := rawPublish["runtimeVersionRange"].(string)
	if !ok {
		return "", false
	}
	return runtimeVersionRange, true
}

func subprocessRuntimeVersionSatisfiesRange(currentVersion, requiredRange string) (bool, error) {
	current, err := parseSubprocessRuntimeVersion(currentVersion)
	if err != nil {
		return false, err
	}
	clauses, err := parseSubprocessRuntimeVersionRangeClauses(requiredRange)
	if err != nil {
		return false, err
	}
	if len(clauses) == 0 || len(clauses) > 2 {
		return false, fmt.Errorf("runtimeVersionRange %q must use one or two comparator clauses", requiredRange)
	}
	for _, clause := range clauses {
		required, err := parseSubprocessRuntimeVersion(clause.version)
		if err != nil {
			return false, err
		}
		if !subprocessRuntimeVersionMatchesComparator(current, clause.comparator, required) {
			return false, nil
		}
	}
	return true, nil
}

type subprocessRuntimeVersionRangeClause struct {
	comparator string
	version    string
}

func parseSubprocessRuntimeVersionRangeClauses(requiredRange string) ([]subprocessRuntimeVersionRangeClause, error) {
	trimmed := strings.TrimSpace(requiredRange)
	if trimmed == "" {
		return nil, fmt.Errorf("runtimeVersionRange %q must use one or two comparator clauses", requiredRange)
	}
	clauses := make([]subprocessRuntimeVersionRangeClause, 0, 2)
	for len(trimmed) > 0 {
		comparator, remainder, ok := trimSubprocessRuntimeVersionComparator(trimmed)
		if !ok {
			return nil, fmt.Errorf("runtimeVersionRange %q must use one or two comparator clauses", requiredRange)
		}
		version, rest, ok := trimSubprocessRuntimeVersionToken(remainder)
		if !ok {
			return nil, fmt.Errorf("runtimeVersionRange %q must use one or two comparator clauses", requiredRange)
		}
		clauses = append(clauses, subprocessRuntimeVersionRangeClause{comparator: comparator, version: version})
		trimmed = strings.TrimSpace(rest)
	}
	return clauses, nil
}

func trimSubprocessRuntimeVersionComparator(raw string) (string, string, bool) {
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	for _, comparator := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(trimmed, comparator) {
			return comparator, strings.TrimLeft(trimmed[len(comparator):], " \t\r\n"), true
		}
	}
	return "", raw, false
}

func trimSubprocessRuntimeVersionToken(raw string) (string, string, bool) {
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

func parseSubprocessRuntimeVersion(raw string) (subprocessRuntimeVersion, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return subprocessRuntimeVersion{}, fmt.Errorf("runtime version %q must use major.minor.patch", raw)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return subprocessRuntimeVersion{}, fmt.Errorf("runtime version %q has invalid major component", raw)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return subprocessRuntimeVersion{}, fmt.Errorf("runtime version %q has invalid minor component", raw)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return subprocessRuntimeVersion{}, fmt.Errorf("runtime version %q has invalid patch component", raw)
	}
	return subprocessRuntimeVersion{major: major, minor: minor, patch: patch}, nil
}

func subprocessRuntimeVersionMatchesComparator(current subprocessRuntimeVersion, comparator string, required subprocessRuntimeVersion) bool {
	comparison := compareSubprocessRuntimeVersions(current, required)
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

func compareSubprocessRuntimeVersions(left, right subprocessRuntimeVersion) int {
	if left.major != right.major {
		return left.major - right.major
	}
	if left.minor != right.minor {
		return left.minor - right.minor
	}
	return left.patch - right.patch
}

func validateSubprocessConfigSchema(manifest pluginsdk.PluginManifest) error {
	if manifest.ConfigSchema == nil {
		return nil
	}
	typ, _ := manifest.ConfigSchema["type"].(string)
	if strings.TrimSpace(typ) == "" {
		return &subprocessCompatibilityFailure{
			manifest: manifest,
			reason:   subprocessFailureReasonManifestInvalidConfigSchema,
			detail:   fmt.Sprintf("plugin %q is not compatible with subprocess host: config schema must declare top-level type %q", manifest.ID, "object"),
		}
	}
	if typ != "object" {
		return &subprocessCompatibilityFailure{
			manifest: manifest,
			reason:   subprocessFailureReasonManifestInvalidConfigSchema,
			detail:   fmt.Sprintf("plugin %q is not compatible with subprocess host: config schema top-level type must be %q, got %q", manifest.ID, "object", typ),
		}
	}
	var properties map[string]any
	if rawProperties, hasProperties := manifest.ConfigSchema["properties"]; hasProperties {
		var ok bool
		properties, ok = rawProperties.(map[string]any)
		if !ok {
			return invalidSubprocessConfigSchemaPropertiesContainerShape(manifest, rawProperties)
		}
	}
	if err := validateSubprocessRequiredFields(manifest, "config schema", "", manifest.ConfigSchema, properties, ""); err != nil {
		return err
	}
	for name, rawProperty := range properties {
		propertySchema, ok := rawProperty.(map[string]any)
		if !ok {
			return invalidSubprocessConfigSchemaShape(manifest, name, rawProperty)
		}
		if err := validateSubprocessConfigPropertySchema(manifest, name, propertySchema); err != nil {
			return err
		}
	}
	return nil
}

func validateSubprocessRequiredFields(manifest pluginsdk.PluginManifest, schemaLabel, qualifiedPrefix string, schema map[string]any, properties map[string]any, nestedCompatibilityRule string) error {
	required, hasRequired := schema["required"]
	if !hasRequired {
		return nil
	}
	requiredFields, ok := required.([]any)
	if !ok {
		reason := subprocessFailureReasonManifestInvalidConfigSchema
		compatibilityRule := ""
		if nestedCompatibilityRule != "" {
			reason = subprocessFailureReasonManifestInvalidConfigProperty
			compatibilityRule = nestedCompatibilityRule
		}
		return &subprocessCompatibilityFailure{
			manifest:          manifest,
			reason:            reason,
			compatibilityRule: compatibilityRule,
			detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: %s required must be an array of property names", manifest.ID, schemaLabel),
		}
	}
	for _, item := range requiredFields {
		name, ok := item.(string)
		if !ok || strings.TrimSpace(name) == "" {
			reason := subprocessFailureReasonManifestInvalidConfigSchema
			compatibilityRule := ""
			if nestedCompatibilityRule != "" {
				reason = subprocessFailureReasonManifestInvalidConfigProperty
				compatibilityRule = nestedCompatibilityRule
			}
			return &subprocessCompatibilityFailure{
				manifest:          manifest,
				reason:            reason,
				compatibilityRule: compatibilityRule,
				detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: %s required entries must be non-empty strings", manifest.ID, schemaLabel),
			}
		}
		if properties == nil || properties[name] == nil {
			propertyPath := name
			if qualifiedPrefix != "" {
				propertyPath = qualifiedPrefix + "." + name
			}
			reason := subprocessFailureReasonManifestMissingRequiredConfig
			compatibilityRule := nestedCompatibilityRule
			if compatibilityRule == "" {
				compatibilityRule = "config_required"
			}
			if nestedCompatibilityRule != "" {
				reason = subprocessFailureReasonManifestInvalidConfigProperty
			}
			return &subprocessCompatibilityFailure{
				manifest:          manifest,
				reason:            reason,
				compatibilityRule: compatibilityRule,
				detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: %s required field %q is missing from properties", manifest.ID, schemaLabel, propertyPath),
			}
		}
	}
	return nil
}

func validateSubprocessConfigPropertySchema(manifest pluginsdk.PluginManifest, name string, propertySchema map[string]any) error {
	propertyType, _ := propertySchema["type"].(string)
	if strings.TrimSpace(propertyType) == "" {
		if _, hasProperties := propertySchema["properties"]; hasProperties {
			return &subprocessCompatibilityFailure{
				manifest:          manifest,
				reason:            subprocessFailureReasonManifestInvalidConfigProperty,
				compatibilityRule: "config_property_schema_shape",
				detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: config schema property %q declares properties but is missing object type", manifest.ID, name),
			}
		}
		return &subprocessCompatibilityFailure{
			manifest:          manifest,
			reason:            subprocessFailureReasonManifestInvalidConfigProperty,
			compatibilityRule: "config_property_type",
			detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: config schema property %q must declare a type for typed config decoding", manifest.ID, name),
		}
	}
	if defaultValue, hasDefault := propertySchema["default"]; hasDefault && !subprocessConfigValueMatchesDeclaredType(propertyType, defaultValue) {
		return &subprocessCompatibilityFailure{
			manifest:          manifest,
			reason:            subprocessFailureReasonManifestInvalidConfigProperty,
			compatibilityRule: "config_property_value_type",
			detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: config schema property %q default value type must match declared type %q, got %q", manifest.ID, name, propertyType, describeSubprocessConfigValueType(defaultValue)),
		}
	}
	if err := validateSubprocessConfigPropertyEnumValues(manifest, name, propertySchema); err != nil {
		return err
	}
	if err := validateSubprocessConfigPropertyEnumDefault(manifest, name, propertySchema); err != nil {
		return err
	}
	if propertyType != "object" {
		return nil
	}
	if rawProperties, hasProperties := propertySchema["properties"]; hasProperties {
		if _, ok := rawProperties.(map[string]any); !ok {
			return invalidSubprocessConfigPropertyPropertiesContainerShape(manifest, name, rawProperties)
		}
	}
	return validateSubprocessNestedConfigPropertySchema(manifest, name, propertySchema)
}

func validateSubprocessNestedConfigPropertySchema(manifest pluginsdk.PluginManifest, parentName string, propertySchema map[string]any) error {
	nestedProperties, _ := propertySchema["properties"].(map[string]any)
	if err := validateSubprocessRequiredFields(manifest, "nested config schema", parentName, propertySchema, nestedProperties, "config_nested_required"); err != nil {
		return err
	}
	for childName, rawNestedProperty := range nestedProperties {
		qualifiedName := parentName + "." + childName
		nestedSchema, ok := rawNestedProperty.(map[string]any)
		if !ok {
			return invalidSubprocessNestedConfigSchemaShape(manifest, qualifiedName, rawNestedProperty)
		}
		if nestedType, _ := nestedSchema["type"].(string); strings.TrimSpace(nestedType) == "" {
			if _, hasProperties := nestedSchema["properties"]; hasProperties {
				return &subprocessCompatibilityFailure{
					manifest:          manifest,
					reason:            subprocessFailureReasonManifestInvalidConfigProperty,
					compatibilityRule: "config_nested_schema_shape",
					detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: nested config schema property %q declares properties but is missing object type", manifest.ID, qualifiedName),
				}
			}
		}
		nestedType, _ := nestedSchema["type"].(string)
		if strings.TrimSpace(nestedType) == "" {
			return &subprocessCompatibilityFailure{
				manifest:          manifest,
				reason:            subprocessFailureReasonManifestInvalidConfigProperty,
				compatibilityRule: "config_nested_property_type",
				detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: nested config schema property %q must declare a type for typed config decoding", manifest.ID, qualifiedName),
			}
		}
		if defaultValue, hasDefault := nestedSchema["default"]; hasDefault && !subprocessConfigValueMatchesDeclaredType(nestedType, defaultValue) {
			return &subprocessCompatibilityFailure{
				manifest:          manifest,
				reason:            subprocessFailureReasonManifestInvalidConfigProperty,
				compatibilityRule: "config_nested_value_type",
				detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: nested config schema property %q default value type must match declared type %q, got %q", manifest.ID, qualifiedName, nestedType, describeSubprocessConfigValueType(defaultValue)),
			}
		}
		if err := validateSubprocessNestedConfigPropertyEnumValues(manifest, qualifiedName, nestedType, nestedSchema); err != nil {
			return err
		}
		if err := validateSubprocessNestedConfigPropertyEnumDefault(manifest, qualifiedName, nestedType, nestedSchema); err != nil {
			return err
		}
		if nestedType != "object" {
			continue
		}
		if rawProperties, hasProperties := nestedSchema["properties"]; hasProperties {
			if _, ok := rawProperties.(map[string]any); !ok {
				return invalidSubprocessNestedConfigPropertyPropertiesContainerShape(manifest, qualifiedName, rawProperties)
			}
		}
		if err := validateSubprocessNestedConfigPropertySchema(manifest, qualifiedName, nestedSchema); err != nil {
			return err
		}
	}
	return nil
}

func invalidSubprocessConfigSchemaShape(manifest pluginsdk.PluginManifest, name string, rawProperty any) error {
	return &subprocessCompatibilityFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonManifestInvalidConfigProperty,
		compatibilityRule: "config_property_schema_shape",
		detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: config schema property %q must be an object schema, got %q", manifest.ID, name, normalizeSubprocessConfigSchemaShapeType(rawProperty)),
	}
}

func invalidSubprocessConfigSchemaPropertiesContainerShape(manifest pluginsdk.PluginManifest, rawProperties any) error {
	return &subprocessCompatibilityFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonManifestInvalidConfigSchema,
		compatibilityRule: "config_schema",
		detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: config schema properties must be an object map, got %q", manifest.ID, normalizeSubprocessConfigSchemaShapeType(rawProperties)),
	}
}

func invalidSubprocessConfigPropertyPropertiesContainerShape(manifest pluginsdk.PluginManifest, name string, rawProperties any) error {
	return &subprocessCompatibilityFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonManifestInvalidConfigProperty,
		compatibilityRule: "config_property_schema_shape",
		detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: config schema property %q object properties must be an object map, got %q", manifest.ID, name, normalizeSubprocessConfigSchemaShapeType(rawProperties)),
	}
}

func invalidSubprocessNestedConfigSchemaShape(manifest pluginsdk.PluginManifest, qualifiedName string, rawNestedProperty any) error {
	return &subprocessCompatibilityFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonManifestInvalidConfigProperty,
		compatibilityRule: "config_nested_schema_shape",
		detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: nested config schema property %q must be an object schema, got %q", manifest.ID, qualifiedName, normalizeSubprocessConfigSchemaShapeType(rawNestedProperty)),
	}
}

func invalidSubprocessNestedConfigPropertyPropertiesContainerShape(manifest pluginsdk.PluginManifest, qualifiedName string, rawProperties any) error {
	return &subprocessCompatibilityFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonManifestInvalidConfigProperty,
		compatibilityRule: "config_nested_schema_shape",
		detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: nested config schema property %q object properties must be an object map, got %q", manifest.ID, qualifiedName, normalizeSubprocessConfigSchemaShapeType(rawProperties)),
	}
}

func normalizeSubprocessConfigSchemaShapeType(rawNestedProperty any) string {
	describedType := describeSubprocessConfigValueType(rawNestedProperty)
	if describedType == "number" {
		return "integer"
	}
	return describedType
}

func validateSubprocessNestedConfigPropertyEnumValues(manifest pluginsdk.PluginManifest, qualifiedName, propertyType string, propertySchema map[string]any) error {
	err := validateSubprocessConfigPropertyEnumValues(manifest, qualifiedName, propertySchema)
	if err == nil {
		return nil
	}
	compatibilityErr, ok := err.(*subprocessCompatibilityFailure)
	if !ok || compatibilityErr.compatibilityRule != "config_property_enum_value_type" {
		return err
	}
	metadata, enumValueDescription := normalizeSubprocessEnumCompatibilityMetadata(compatibilityErr.metadata)
	return nestedSubprocessConfigPropertyCompatibilityFailure(
		manifest,
		"config_nested_enum_value_type",
		qualifiedName,
		metadata,
		"enum value %s must match declared type %q",
		enumValueDescription,
		propertyType,
	)
}

func normalizeSubprocessEnumCompatibilityMetadata(metadata map[string]any) (map[string]any, string) {
	enumValueDescription := normalizeSubprocessEnumValueMetadata(metadata)
	if enumValueDescription == "" {
		enumValueDescription = normalizeSubprocessEnumValueDescription(nil)
	}
	return map[string]any{"enum_value": enumValueDescription}, enumValueDescription
}

func normalizeSubprocessEnumValueMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	enumValue, hasEnumValue := metadata["enum_value"]
	if !hasEnumValue {
		return ""
	}
	return normalizeSubprocessEnumValueDescription(enumValue)
}

func normalizeSubprocessEnumValueDescription(enumValue any) string {
	if metadata, ok := enumValue.(map[string]any); ok {
		if nestedDescription := normalizeSubprocessEnumValueMetadata(metadata); nestedDescription != "" {
			return nestedDescription
		}
	}
	enumValueDescription, _ := enumValue.(string)
	if strings.TrimSpace(enumValueDescription) != "" {
		return enumValueDescription
	}
	return describeSubprocessConfigValue(enumValue)
}

func nestedSubprocessConfigPropertyCompatibilityFailure(manifest pluginsdk.PluginManifest, compatibilityRule, qualifiedName string, metadata map[string]any, detailFormat string, detailArgs ...any) error {
	detailArgs = append([]any{qualifiedName}, detailArgs...)
	return &subprocessCompatibilityFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonManifestInvalidConfigProperty,
		compatibilityRule: compatibilityRule,
		metadata:          metadata,
		detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: nested config schema property %q "+detailFormat, append([]any{manifest.ID}, detailArgs...)...),
	}
}

func validateSubprocessNestedConfigPropertyEnumDefault(manifest pluginsdk.PluginManifest, qualifiedName, propertyType string, propertySchema map[string]any) error {
	err := validateSubprocessConfigPropertyEnumDefault(manifest, qualifiedName, propertySchema)
	if err == nil {
		return nil
	}
	compatibilityErr, ok := err.(*subprocessCompatibilityFailure)
	if !ok || compatibilityErr.compatibilityRule != "config_property_enum_default" {
		return err
	}
	return nestedSubprocessConfigPropertyCompatibilityFailure(manifest, "config_nested_enum_default", qualifiedName, nil, "default value %s must be declared in enum for declared type %q", describeSubprocessConfigValue(propertySchema["default"]), propertyType)
}

func validateSubprocessConfigPropertyEnumValues(manifest pluginsdk.PluginManifest, name string, propertySchema map[string]any) error {
	propertyType, _ := propertySchema["type"].(string)
	if strings.TrimSpace(propertyType) == "" {
		return nil
	}
	rawEnumValues, hasEnum := propertySchema["enum"]
	if !hasEnum {
		return nil
	}
	enumValues, ok := subprocessConfigEnumValues(rawEnumValues)
	if !ok {
		return nil
	}
	for _, enumValue := range enumValues {
		if subprocessConfigValueMatchesDeclaredType(propertyType, enumValue) {
			continue
		}
		enumValueDescription := describeSubprocessConfigValue(enumValue)
		return &subprocessCompatibilityFailure{
			manifest:          manifest,
			reason:            subprocessFailureReasonManifestInvalidConfigProperty,
			compatibilityRule: "config_property_enum_value_type",
			metadata: map[string]any{
				"enum_value": enumValueDescription,
			},
			detail: fmt.Sprintf("plugin %q is not compatible with subprocess host: config schema property %q enum value %s must match declared type %q", manifest.ID, name, enumValueDescription, propertyType),
		}
	}
	return nil
}

func validateSubprocessConfigPropertyEnumDefault(manifest pluginsdk.PluginManifest, name string, propertySchema map[string]any) error {
	propertyType, _ := propertySchema["type"].(string)
	if strings.TrimSpace(propertyType) == "" {
		return nil
	}
	defaultValue, hasDefault := propertySchema["default"]
	if !hasDefault || !subprocessConfigValueMatchesDeclaredType(propertyType, defaultValue) {
		return nil
	}
	rawEnumValues, hasEnum := propertySchema["enum"]
	if !hasEnum {
		return nil
	}
	enumValues, ok := subprocessConfigEnumValues(rawEnumValues)
	if !ok {
		return nil
	}
	if subprocessConfigEnumContainsDefault(propertyType, enumValues, defaultValue) {
		return nil
	}
	return &subprocessCompatibilityFailure{
		manifest:          manifest,
		reason:            subprocessFailureReasonManifestInvalidConfigProperty,
		compatibilityRule: "config_property_enum_default",
		detail:            fmt.Sprintf("plugin %q is not compatible with subprocess host: config schema property %q default value %s must be declared in enum for type %q", manifest.ID, name, describeSubprocessConfigValue(defaultValue), propertyType),
	}
}

func subprocessConfigEnumValues(rawEnumValues any) ([]any, bool) {
	if rawEnumValues == nil {
		return nil, false
	}
	if enumValues, ok := rawEnumValues.([]any); ok {
		return enumValues, true
	}
	value := reflect.ValueOf(rawEnumValues)
	if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
		return nil, false
	}
	enumValues := make([]any, value.Len())
	for i := 0; i < value.Len(); i++ {
		enumValues[i] = value.Index(i).Interface()
	}
	return enumValues, true
}

func subprocessConfigEnumContainsDefault(propertyType string, enumValues []any, defaultValue any) bool {
	for _, enumValue := range enumValues {
		if subprocessConfigValuesEqual(propertyType, enumValue, defaultValue) {
			return true
		}
	}
	return false
}

func subprocessConfigValuesEqual(propertyType string, left any, right any) bool {
	switch propertyType {
	case "string":
		leftValue, leftOK := left.(string)
		rightValue, rightOK := right.(string)
		return leftOK && rightOK && leftValue == rightValue
	case "boolean":
		leftValue, leftOK := left.(bool)
		rightValue, rightOK := right.(bool)
		return leftOK && rightOK && leftValue == rightValue
	case "integer":
		leftValue, leftOK := normalizeSubprocessConfigInteger(left)
		rightValue, rightOK := normalizeSubprocessConfigInteger(right)
		return leftOK && rightOK && leftValue == rightValue
	case "number":
		leftValue, leftOK := normalizeSubprocessConfigNumber(left)
		rightValue, rightOK := normalizeSubprocessConfigNumber(right)
		return leftOK && rightOK && leftValue == rightValue
	default:
		return reflect.DeepEqual(left, right)
	}
}

func normalizeSubprocessConfigInteger(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint:
		return int64(v), true
	case uint8:
		return int64(v), true
	case uint16:
		return int64(v), true
	case uint32:
		return int64(v), true
	case uint64:
		return int64(v), true
	case float32:
		converted := int64(v)
		return converted, float32(converted) == v
	case float64:
		converted := int64(v)
		return converted, float64(converted) == v
	case json.Number:
		text := v.String()
		if strings.ContainsAny(text, ".eE") {
			return 0, false
		}
		converted, err := v.Int64()
		return converted, err == nil
	default:
		return 0, false
	}
}

func normalizeSubprocessConfigNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case json.Number:
		converted, err := v.Float64()
		return converted, err == nil
	default:
		return 0, false
	}
}

func subprocessConfigValueMatchesDeclaredType(propertyType string, value any) bool {
	switch propertyType {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		switch value.(type) {
		case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
			return true
		default:
			return false
		}
	case "integer":
		switch v := value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		case float32:
			return float32(int64(v)) == v
		case float64:
			return float64(int64(v)) == v
		case json.Number:
			text := v.String()
			if strings.ContainsAny(text, ".eE") {
				return false
			}
			_, err := v.Int64()
			return err == nil
		default:
			return false
		}
	default:
		return true
	}
}

func describeSubprocessConfigValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

func describeSubprocessConfigValueType(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float32, float64:
		return "number"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "integer"
	case json.Number:
		if strings.ContainsAny(v.String(), ".eE") {
			return "number"
		}
		return "integer"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func (h *SubprocessPluginHost) observeInstanceConfigFailure(pluginID string, executionContext eventmodel.ExecutionContext, requestType string, err error) {
	ctx := logContextFromExecutionContext(executionContext)
	reason := "instance_config_invalid"
	compatibilityRule := "unknown"
	manifestMode := ""
	manifestAPIVersion := ""
	entryBinaryPresent := false
	entryModulePresent := false
	configMetadata := map[string]any{}
	var configErr *subprocessInstanceConfigFailure
	if errors.As(err, &configErr) {
		reason = string(configErr.reason)
		compatibilityRule = configErr.compatibilityRule
		manifestMode = configErr.manifest.Mode
		manifestAPIVersion = configErr.manifest.APIVersion
		entryBinaryPresent = strings.TrimSpace(configErr.manifest.Entry.Binary) != ""
		entryModulePresent = strings.TrimSpace(configErr.manifest.Entry.Module) != ""
		for key, value := range configErr.metadata {
			configMetadata[key] = value
		}
	}
	logFields := map[string]any{
		"plugin_id":             pluginID,
		"request_type":          requestType,
		"failure_stage":         "instance_config",
		"failure_reason":        reason,
		"compatibility_rule":    compatibilityRule,
		"error":                 err.Error(),
		"supported_api_version": supportedSubprocessPluginAPIVersion,
		"manifest_mode":         manifestMode,
		"manifest_api_version":  manifestAPIVersion,
		"entry_binary_present":  entryBinaryPresent,
		"entry_module_present":  entryModulePresent,
	}
	for key, value := range configMetadata {
		logFields[key] = value
	}
	if h.logger != nil {
		h.logger.Log("error", "subprocess host instance config rejected", ctx, FailureLogFields("plugin_host", "instance_config."+requestType, err, reason, logFields))
	}
	if h.tracer != nil {
		traceMetadata := map[string]any{
			"request_type":          requestType,
			"failure_stage":         "instance_config",
			"failure_reason":        reason,
			"compatibility_rule":    compatibilityRule,
			"error":                 err.Error(),
			"supported_api_version": supportedSubprocessPluginAPIVersion,
			"manifest_mode":         manifestMode,
			"manifest_api_version":  manifestAPIVersion,
			"entry_binary_present":  entryBinaryPresent,
			"entry_module_present":  entryModulePresent,
		}
		for key, value := range configMetadata {
			traceMetadata[key] = value
		}
		finish := h.tracer.StartSpan(ctx.TraceID, "plugin_host.instance_config", ctx.EventID, pluginID, ctx.RunID, ctx.CorrelationID, traceMetadata)
		finish()
	}
	if h.metrics != nil {
		h.metrics.RecordSubprocessFailure(pluginID, requestType, "instance_config", reason)
	}
}

func (h *SubprocessPluginHost) observeCompatibilityFailure(pluginID string, executionContext eventmodel.ExecutionContext, requestType string, err error) {
	ctx := logContextFromExecutionContext(executionContext)
	reason := "manifest_incompatible"
	compatibilityRule := "unknown"
	manifestMode := ""
	manifestAPIVersion := ""
	entryBinaryPresent := false
	entryModulePresent := false
	compatibilityMetadata := map[string]any{}
	var compatibilityErr *subprocessCompatibilityFailure
	if errors.As(err, &compatibilityErr) {
		reason = string(compatibilityErr.reason)
		manifestMode = compatibilityErr.manifest.Mode
		manifestAPIVersion = compatibilityErr.manifest.APIVersion
		entryBinaryPresent = strings.TrimSpace(compatibilityErr.manifest.Entry.Binary) != ""
		entryModulePresent = strings.TrimSpace(compatibilityErr.manifest.Entry.Module) != ""
		for key, value := range compatibilityErr.metadata {
			compatibilityMetadata[key] = value
		}
		if strings.TrimSpace(compatibilityErr.compatibilityRule) != "" {
			compatibilityRule = compatibilityErr.compatibilityRule
		} else {
			switch compatibilityErr.reason {
			case subprocessFailureReasonManifestModeMismatch:
				compatibilityRule = "mode"
			case subprocessFailureReasonManifestUnsupportedAPI:
				compatibilityRule = "api_version"
			case subprocessFailureReasonManifestUnsupportedRuntime:
				compatibilityRule = "runtime_version"
			case subprocessFailureReasonManifestMissingEntry:
				compatibilityRule = "entry_target"
			case subprocessFailureReasonManifestInvalidConfigSchema:
				compatibilityRule = "config_schema"
			case subprocessFailureReasonManifestMissingRequiredConfig:
				compatibilityRule = "config_required"
			case subprocessFailureReasonManifestInvalidConfigProperty:
				compatibilityRule = "config_property_type"
			}
		}
	}
	logFields := map[string]any{
		"plugin_id":             pluginID,
		"request_type":          requestType,
		"failure_stage":         "compatibility",
		"failure_reason":        reason,
		"compatibility_rule":    compatibilityRule,
		"error":                 err.Error(),
		"supported_api_version": supportedSubprocessPluginAPIVersion,
		"manifest_mode":         manifestMode,
		"manifest_api_version":  manifestAPIVersion,
		"entry_binary_present":  entryBinaryPresent,
		"entry_module_present":  entryModulePresent,
	}
	for key, value := range compatibilityMetadata {
		logFields[key] = value
	}
	if h.logger != nil {
		h.logger.Log("error", "subprocess host compatibility check failed", ctx, FailureLogFields("plugin_host", "compatibility."+requestType, err, reason, logFields))
	}
	if h.tracer != nil {
		traceMetadata := map[string]any{
			"request_type":          requestType,
			"failure_stage":         "compatibility",
			"failure_reason":        reason,
			"compatibility_rule":    compatibilityRule,
			"error":                 err.Error(),
			"supported_api_version": supportedSubprocessPluginAPIVersion,
			"manifest_mode":         manifestMode,
			"manifest_api_version":  manifestAPIVersion,
			"entry_binary_present":  entryBinaryPresent,
			"entry_module_present":  entryModulePresent,
		}
		for key, value := range compatibilityMetadata {
			traceMetadata[key] = value
		}
		finish := h.tracer.StartSpan(ctx.TraceID, "plugin_host.compatibility", ctx.EventID, pluginID, ctx.RunID, ctx.CorrelationID, traceMetadata)
		finish()
	}
	if h.metrics != nil {
		h.metrics.RecordSubprocessFailure(pluginID, requestType, "compatibility", reason)
	}
}

func appendBounded(lines []string, line string, max int) []string {
	lines = append(lines, line)
	if max > 0 && len(lines) > max {
		return append([]string(nil), lines[len(lines)-max:]...)
	}
	return lines
}
