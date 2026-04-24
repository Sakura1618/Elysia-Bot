package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
	pluginadmin "github.com/ohmyopencode/bot-platform/plugins/plugin-admin"
	pluginaichat "github.com/ohmyopencode/bot-platform/plugins/plugin-ai-chat"
	pluginecho "github.com/ohmyopencode/bot-platform/plugins/plugin-echo"
	pluginworkflowdemo "github.com/ohmyopencode/bot-platform/plugins/plugin-workflow-demo"
)

type runtimeAppBuildOptions struct {
	pluginHostFactory func(*replyBuffer, *runtimecore.WorkflowRuntime) runtimecore.PluginHost
	traceExporter     runtimecore.TraceExporter
}

type runtimeSubprocessLauncherConfig struct {
	workingDirectory string
	envAllowlist     []string
	restartBudget    int
	restartWindow    time.Duration
}

var runtimeSubprocessExecutablePathResolver = canonicalRuntimeSubprocessExecutablePath

type routedPluginHost struct {
	direct      runtimecore.PluginHost
	subprocess  runtimecore.PluginHost
	routes      map[string]runtimecore.PluginHost
	defaultHost runtimecore.PluginHost
	eventScopes map[string]map[string]struct{}
}

type runtimePluginHostCloser interface {
	Close() error
}

type runtimePluginHostObservabilitySetter interface {
	SetObservability(logger *runtimecore.Logger, tracer *runtimecore.TraceRecorder, metrics *runtimecore.MetricsRegistry)
}

func newRoutedPluginHost(direct runtimecore.PluginHost, subprocess runtimecore.PluginHost, subprocessPluginIDs []string) runtimecore.PluginHost {
	routes := make(map[string]runtimecore.PluginHost, len(subprocessPluginIDs))
	for _, pluginID := range subprocessPluginIDs {
		pluginID = strings.TrimSpace(pluginID)
		if pluginID == "" || subprocess == nil {
			continue
		}
		routes[pluginID] = subprocess
	}
	return newRoutedPluginHostWithRouteMapAndEventScopes(direct, subprocess, routes, nil)
}

func newRoutedPluginHostWithEventScopes(direct runtimecore.PluginHost, subprocess runtimecore.PluginHost, subprocessPluginIDs []string, eventScopes map[string]map[string]struct{}) runtimecore.PluginHost {
	routes := make(map[string]runtimecore.PluginHost, len(subprocessPluginIDs))
	for _, pluginID := range subprocessPluginIDs {
		pluginID = strings.TrimSpace(pluginID)
		if pluginID == "" || subprocess == nil {
			continue
		}
		routes[pluginID] = subprocess
	}
	return newRoutedPluginHostWithRouteMapAndEventScopes(direct, subprocess, routes, eventScopes)
}

func newRoutedPluginHostWithRouteMapAndEventScopes(direct runtimecore.PluginHost, subprocess runtimecore.PluginHost, routes map[string]runtimecore.PluginHost, eventScopes map[string]map[string]struct{}) runtimecore.PluginHost {
	normalizedRoutes := make(map[string]runtimecore.PluginHost, len(routes))
	for pluginID, host := range routes {
		pluginID = strings.TrimSpace(pluginID)
		if pluginID == "" || host == nil {
			continue
		}
		normalizedRoutes[pluginID] = host
	}
	return routedPluginHost{direct: direct, subprocess: subprocess, routes: normalizedRoutes, defaultHost: direct, eventScopes: clonePluginEventScopes(eventScopes)}
}

func (h routedPluginHost) hostForPlugin(pluginID string) runtimecore.PluginHost {
	pluginID = strings.TrimSpace(pluginID)
	if host, ok := h.routes[pluginID]; ok && host != nil {
		return host
	}
	if h.defaultHost != nil {
		return h.defaultHost
	}
	if h.direct != nil {
		return h.direct
	}
	return h.subprocess
}

func (h routedPluginHost) DispatchEvent(ctx context.Context, plugin pluginsdk.Plugin, event eventmodel.Event, executionContext eventmodel.ExecutionContext) error {
	if !h.eventAllowed(plugin.Manifest.ID, event.Source) {
		return nil
	}
	host := h.hostForPlugin(plugin.Manifest.ID)
	if host == nil {
		return fmt.Errorf("plugin host route for %q is not configured", plugin.Manifest.ID)
	}
	return host.DispatchEvent(ctx, plugin, event, executionContext)
}

func (h routedPluginHost) eventAllowed(pluginID string, source string) bool {
	if len(h.eventScopes) == 0 {
		return true
	}
	allowed, ok := h.eventScopes[strings.TrimSpace(pluginID)]
	if !ok || len(allowed) == 0 {
		return true
	}
	_, ok = allowed[source]
	return ok
}

func clonePluginEventScopes(scopes map[string]map[string]struct{}) map[string]map[string]struct{} {
	if len(scopes) == 0 {
		return nil
	}
	cloned := make(map[string]map[string]struct{}, len(scopes))
	for pluginID, allowed := range scopes {
		pluginID = strings.TrimSpace(pluginID)
		if pluginID == "" || len(allowed) == 0 {
			continue
		}
		clonedAllowed := make(map[string]struct{}, len(allowed))
		for source := range allowed {
			clonedAllowed[source] = struct{}{}
		}
		cloned[pluginID] = clonedAllowed
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func (h routedPluginHost) DispatchCommand(ctx context.Context, plugin pluginsdk.Plugin, command eventmodel.CommandInvocation, executionContext eventmodel.ExecutionContext) error {
	host := h.hostForPlugin(plugin.Manifest.ID)
	if host == nil {
		return fmt.Errorf("plugin host route for %q is not configured", plugin.Manifest.ID)
	}
	return host.DispatchCommand(ctx, plugin, command, executionContext)
}

func (h routedPluginHost) DispatchJob(ctx context.Context, plugin pluginsdk.Plugin, job pluginsdk.JobInvocation, executionContext eventmodel.ExecutionContext) error {
	host := h.hostForPlugin(plugin.Manifest.ID)
	if host == nil {
		return fmt.Errorf("plugin host route for %q is not configured", plugin.Manifest.ID)
	}
	return host.DispatchJob(ctx, plugin, job, executionContext)
}

func (h routedPluginHost) DispatchSchedule(ctx context.Context, plugin pluginsdk.Plugin, trigger pluginsdk.ScheduleTrigger, executionContext eventmodel.ExecutionContext) error {
	host := h.hostForPlugin(plugin.Manifest.ID)
	if host == nil {
		return fmt.Errorf("plugin host route for %q is not configured", plugin.Manifest.ID)
	}
	return host.DispatchSchedule(ctx, plugin, trigger, executionContext)
}

func (h routedPluginHost) uniqueHosts() []runtimecore.PluginHost {
	seen := map[runtimecore.PluginHost]struct{}{}
	hosts := make([]runtimecore.PluginHost, 0, len(h.routes)+3)
	appendHost := func(candidate runtimecore.PluginHost) {
		if candidate == nil {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		seen[candidate] = struct{}{}
		hosts = append(hosts, candidate)
	}
	appendHost(h.defaultHost)
	appendHost(h.direct)
	appendHost(h.subprocess)
	for _, candidate := range h.routes {
		appendHost(candidate)
	}
	return hosts
}

func (h routedPluginHost) Close() error {
	var errs []error
	for _, candidate := range h.uniqueHosts() {
		closer, ok := candidate.(runtimePluginHostCloser)
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errorsJoin(errs)
	}
	return nil
}

func (h routedPluginHost) SetObservability(logger *runtimecore.Logger, tracer *runtimecore.TraceRecorder, metrics *runtimecore.MetricsRegistry) {
	for _, candidate := range h.uniqueHosts() {
		setter, ok := candidate.(runtimePluginHostObservabilitySetter)
		if !ok {
			continue
		}
		setter.SetObservability(logger, tracer, metrics)
	}
}

func errorsJoin(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	result := errs[0]
	for _, err := range errs[1:] {
		result = fmt.Errorf("%w; %v", result, err)
	}
	return result
}

func newRuntimePluginHost(replies *replyBuffer, workflowRuntime *runtimecore.WorkflowRuntime, subprocessPluginIDs []string) runtimecore.PluginHost {
	return newRuntimePluginHostWithEventScopes(replies, workflowRuntime, subprocessPluginIDs, nil)
}

func newRuntimeSubprocessPluginHost(replies *replyBuffer, workflowRuntime *runtimecore.WorkflowRuntime, launcherConfig runtimeSubprocessLauncherConfig) *runtimecore.SubprocessPluginHost {
	subprocess := runtimecore.NewSubprocessPluginHostWithErrorFactory(runtimePluginProcessFactory(launcherConfig))
	subprocess.SetReplyTextCallback(replies.ReplyText)
	if workflowRuntime != nil {
		subprocess.SetWorkflowStartOrResumeCallback(func(ctx context.Context, request runtimecore.SubprocessWorkflowStartOrResumeRequest) (runtimecore.WorkflowTransition, error) {
			return workflowRuntime.StartOrResume(ctx, request.WorkflowID, request.PluginID, request.EventType, request.EventID, request.Initial)
		})
	}
	subprocess.SetRestartBudget(launcherConfig.restartBudget, launcherConfig.restartWindow)
	return subprocess
}

func newRuntimePluginHostWithEventScopes(replies *replyBuffer, workflowRuntime *runtimecore.WorkflowRuntime, subprocessPluginIDs []string, eventScopes map[string]map[string]struct{}) runtimecore.PluginHost {
	direct := runtimecore.DirectPluginHost{}
	launcherConfig := runtimeDefaultSubprocessLauncherConfig()
	subprocess := newRuntimeSubprocessPluginHost(replies, workflowRuntime, launcherConfig)
	return newRoutedPluginHostWithEventScopes(direct, subprocess, subprocessPluginIDs, eventScopes)
}

func runtimePluginProcessFactory(config runtimeSubprocessLauncherConfig) func(context.Context) (*exec.Cmd, error) {
	return func(ctx context.Context) (*exec.Cmd, error) {
		workingDirectory, err := boundedRuntimeSubprocessWorkingDirectory(config.workingDirectory)
		if err != nil {
			return nil, fmt.Errorf("subprocess launch guard blocked start: %w", err)
		}
		executablePath, err := runtimeSubprocessExecutablePathResolver()
		if err != nil {
			return nil, fmt.Errorf("subprocess launch guard blocked start: %w", err)
		}
		binaryName := strings.ToLower(executablePath)
		if strings.HasSuffix(binaryName, ".test") || strings.HasSuffix(binaryName, ".test.exe") {
			cmd := exec.CommandContext(ctx, executablePath, "-test.run=TestHelperRuntimePluginEchoSubprocess", "--", "-runtime-plugin-echo-subprocess")
			cmd.Dir = workingDirectory
			cmd.Env = append(runtimeAllowlistedEnv(os.Environ(), config.envAllowlist), "GO_WANT_RUNTIME_PLUGIN_ECHO_SUBPROCESS=1")
			return cmd, nil
		}
		cmd := exec.CommandContext(ctx, executablePath, "-runtime-plugin-echo-subprocess")
		cmd.Dir = workingDirectory
		cmd.Env = runtimeAllowlistedEnv(os.Environ(), config.envAllowlist)
		return cmd, nil
	}
}

func canonicalRuntimeSubprocessExecutablePath() (string, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable path: %w", err)
	}
	executablePath = strings.TrimSpace(executablePath)
	if executablePath == "" {
		return "", fmt.Errorf("resolve current executable path: empty path")
	}
	executablePath, err = filepath.Abs(filepath.Clean(executablePath))
	if err != nil {
		return "", fmt.Errorf("resolve absolute executable path: %w", err)
	}
	executablePath, err = filepath.EvalSymlinks(executablePath)
	if err != nil {
		return "", fmt.Errorf("resolve canonical executable path: %w", err)
	}
	executablePath = strings.TrimSpace(executablePath)
	if executablePath == "" {
		return "", fmt.Errorf("resolve canonical executable path: empty path")
	}
	if !filepath.IsAbs(executablePath) {
		return "", fmt.Errorf("resolve canonical executable path: %q is not absolute", executablePath)
	}
	return executablePath, nil
}

func runtimeDefaultSubprocessLauncherConfig() runtimeSubprocessLauncherConfig {
	workingDirectory, err := os.Getwd()
	if err != nil {
		workingDirectory = "."
	}
	return runtimeSubprocessLauncherConfig{
		workingDirectory: workingDirectory,
		envAllowlist: []string{
			"PATH",
			"SYSTEMROOT",
			"WINDIR",
			"COMSPEC",
			"PATHEXT",
			"TMP",
			"TEMP",
			"HOME",
			"USERPROFILE",
			"LOCALAPPDATA",
			"APPDATA",
		},
		restartBudget: 3,
		restartWindow: 2 * time.Second,
	}
}

func boundedRuntimeSubprocessWorkingDirectory(candidate string) (string, error) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "", fmt.Errorf("launch guard working directory is required")
	}
	clean, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", fmt.Errorf("launch guard resolve working directory: %w", err)
	}
	root, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("launch guard resolve workspace root: %w", err)
	}
	root, err = filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("launch guard resolve absolute workspace root: %w", err)
	}
	relative, err := filepath.Rel(root, clean)
	if err != nil {
		return "", fmt.Errorf("launch guard compare working directory: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("launch guard working directory %q escapes workspace root %q", clean, root)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("launch guard stat working directory %q: %w", clean, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("launch guard working directory %q must be a directory", clean)
	}
	return clean, nil
}

func runtimeAllowlistedEnv(env []string, allowlist []string) []string {
	if len(allowlist) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(allowlist))
	for _, key := range allowlist {
		key = strings.ToUpper(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		allowed[key] = struct{}{}
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := allowed[strings.ToUpper(strings.TrimSpace(name))]; !exists {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

const (
	runtimeSubprocessCrashOncePluginEnv = "BOT_PLATFORM_RUNTIME_SUBPROCESS_CRASH_ONCE_PLUGIN"
	runtimeSubprocessCrashOnceMarkerEnv = "BOT_PLATFORM_RUNTIME_SUBPROCESS_CRASH_ONCE_MARKER"
)

func maybeCrashRuntimeSubprocessOnce(pluginID string) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return
	}
	if strings.TrimSpace(os.Getenv(runtimeSubprocessCrashOncePluginEnv)) != pluginID {
		return
	}
	marker := strings.TrimSpace(os.Getenv(runtimeSubprocessCrashOnceMarkerEnv))
	if marker != "" {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		_ = os.WriteFile(marker, []byte("crashed"), 0o644)
	}
	os.Exit(2)
}

func runRuntimePluginEchoSubprocess(stdout *os.File, stderr *os.File) int {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = stderr.WriteString("runtime-plugin-echo-subprocess-online\n")
	_, _ = stdout.WriteString("{\"type\":\"handshake\",\"status\":\"ok\",\"message\":\"handshake-ready\"}\n")
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var request hostRequestEnvelope
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			_, _ = stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to decode host request envelope\"}\n")
			continue
		}
		switch request.Type {
		case "health":
			_, _ = stdout.WriteString("{\"type\":\"health\",\"status\":\"ok\",\"message\":\"healthy\"}\n")
		case "event":
			maybeCrashRuntimeSubprocessOnce(request.PluginID)
			if err := handleRuntimeSubprocessEvent(reader, stdout, request.PluginID, request.Event, request.InstanceConfig); err != nil {
				encodedErr, _ := json.Marshal(map[string]any{"type": "event", "status": "error", "error": err.Error()})
				_, _ = stdout.WriteString(string(encodedErr) + "\n")
			}
		case "command":
			if err := runRuntimeSubprocessCommandBridge(reader, stdout, request.PluginID, request.Command, request.InstanceConfig); err != nil {
				encodedErr, _ := json.Marshal(map[string]any{"type": "command", "status": "error", "error": err.Error()})
				_, _ = stdout.WriteString(string(encodedErr) + "\n")
				continue
			}
			_, _ = stdout.WriteString("{\"type\":\"command\",\"status\":\"ok\",\"message\":\"command-ok\"}\n")
		case "job":
			if err := runRuntimeSubprocessJobBridge(reader, stdout, request.PluginID, request.Job, request.InstanceConfig); err != nil {
				encodedErr, _ := json.Marshal(map[string]any{"type": "job", "status": "error", "error": err.Error()})
				_, _ = stdout.WriteString(string(encodedErr) + "\n")
				continue
			}
			_, _ = stdout.WriteString("{\"type\":\"job\",\"status\":\"ok\",\"message\":\"job-ok\"}\n")
		case "schedule":
			_, _ = stdout.WriteString("{\"type\":\"schedule\",\"status\":\"ok\",\"message\":\"schedule-ok\"}\n")
		default:
			encodedErr, _ := json.Marshal(map[string]any{"type": request.Type, "status": "error", "error": fmt.Sprintf("unsupported request type %q", request.Type)})
			_, _ = stdout.WriteString(string(encodedErr) + "\n")
		}
	}
}

type hostRequestEnvelope struct {
	Type           string          `json:"type"`
	PluginID       string          `json:"plugin_id,omitempty"`
	Event          json.RawMessage `json:"event,omitempty"`
	Command        json.RawMessage `json:"command,omitempty"`
	Job            json.RawMessage `json:"job,omitempty"`
	InstanceConfig json.RawMessage `json:"instance_config,omitempty"`
}

type runtimePluginEchoEventPayload struct {
	Event eventmodel.Event            `json:"event"`
	Ctx   eventmodel.ExecutionContext `json:"ctx"`
}

type runtimePluginCommandPayload struct {
	Command eventmodel.CommandInvocation `json:"command"`
	Ctx     eventmodel.ExecutionContext  `json:"ctx"`
}

type runtimePluginJobPayload struct {
	Job pluginsdk.JobInvocation     `json:"job"`
	Ctx eventmodel.ExecutionContext `json:"ctx"`
}

type runtimePluginEchoCallbackEnvelope struct {
	Type                  string                                          `json:"type"`
	Callback              string                                          `json:"callback,omitempty"`
	ReplyText             *runtimePluginEchoReplyTextCallback             `json:"reply_text,omitempty"`
	AdminRequest          *runtimecore.SubprocessAdminRequest             `json:"admin_request,omitempty"`
	AIChatQueue           *runtimecore.SubprocessAIChatQueueRequest       `json:"ai_chat_queue,omitempty"`
	AIChatSession         *runtimecore.SubprocessAIChatSessionRequest     `json:"ai_chat_session,omitempty"`
	AIChatProvider        *runtimecore.SubprocessAIChatProviderRequest    `json:"ai_chat_provider,omitempty"`
	WorkflowLoad          *runtimecore.SubprocessWorkflowLoadRequest      `json:"workflow_load,omitempty"`
	WorkflowStartOrResume *runtimePluginEchoWorkflowStartOrResumeCallback `json:"workflow_start_or_resume,omitempty"`
}

type runtimePluginEchoReplyTextCallback struct {
	Handle eventmodel.ReplyHandle `json:"handle"`
	Text   string                 `json:"text"`
}

type runtimePluginEchoCallbackResult struct {
	Type                  string                                      `json:"type"`
	Status                string                                      `json:"status,omitempty"`
	Error                 string                                      `json:"error,omitempty"`
	AdminRequest          *runtimecore.SubprocessAdminResult          `json:"admin_request,omitempty"`
	AIChatQueue           *runtimecore.SubprocessAIChatQueueResult    `json:"ai_chat_queue,omitempty"`
	AIChatProvider        *runtimecore.SubprocessAIChatProviderResult `json:"ai_chat_provider,omitempty"`
	WorkflowLoad          *runtimecore.WorkflowInstanceState          `json:"workflow_load,omitempty"`
	WorkflowStartOrResume json.RawMessage                             `json:"workflow_start_or_resume,omitempty"`
}

type runtimeWorkflowObservability struct {
	TraceID       string
	EventID       string
	PluginID      string
	RunID         string
	CorrelationID string
}

type runtimePluginEchoWorkflowTransitionSnapshot struct {
	Started  bool                                      `json:"Started"`
	Resumed  bool                                      `json:"Resumed"`
	Instance runtimePluginEchoWorkflowInstanceSnapshot `json:"Instance"`
}

type runtimePluginEchoWorkflowInstanceSnapshot struct {
	TraceID       string `json:"TraceID,omitempty"`
	EventID       string `json:"EventID,omitempty"`
	PluginID      string `json:"PluginID,omitempty"`
	RunID         string `json:"RunID,omitempty"`
	CorrelationID string `json:"CorrelationID,omitempty"`
}

type runtimePluginEchoReplyBridge struct {
	reader *bufio.Reader
	stdout *os.File
}

type runtimePluginEchoWorkflowStartOrResumeCallback struct {
	WorkflowID    string               `json:"workflow_id"`
	PluginID      string               `json:"plugin_id,omitempty"`
	TraceID       string               `json:"trace_id,omitempty"`
	EventType     string               `json:"event_type"`
	EventID       string               `json:"event_id,omitempty"`
	RunID         string               `json:"run_id,omitempty"`
	CorrelationID string               `json:"correlation_id,omitempty"`
	Initial       runtimecore.Workflow `json:"initial"`
}

type runtimeSubprocessAdminAuthorizationBridge struct {
	bridge runtimePluginEchoReplyBridge
}

type runtimeSubprocessAdminLifecycleBridge struct {
	bridge runtimePluginEchoReplyBridge
}

type runtimeSubprocessAdminRolloutBridge struct {
	bridge runtimePluginEchoReplyBridge
}

type runtimeSubprocessAdminReplayBridge struct {
	bridge runtimePluginEchoReplyBridge
}

type runtimeSubprocessAdminAuditBridge struct {
	bridge runtimePluginEchoReplyBridge
}

type runtimeSubprocessAIChatQueueBridge struct {
	bridge runtimePluginEchoReplyBridge
}

type runtimeSubprocessAIChatSessionBridge struct {
	bridge runtimePluginEchoReplyBridge
}

type runtimeSubprocessAIChatProviderBridge struct {
	bridge runtimePluginEchoReplyBridge
}

func (b runtimePluginEchoReplyBridge) ReplyText(handle eventmodel.ReplyHandle, text string) error {
	result, err := b.requestCallback(runtimePluginEchoCallbackEnvelope{
		Type:     "callback",
		Callback: "reply_text",
		ReplyText: &runtimePluginEchoReplyTextCallback{
			Handle: handle,
			Text:   text,
		},
	})
	if err != nil {
		return fmt.Errorf("reply_text callback: %w", err)
	}
	if result.Type != "callback_result" {
		return fmt.Errorf("unexpected reply_text callback result type %q", result.Type)
	}
	if result.Status != "ok" {
		if strings.TrimSpace(result.Error) == "" {
			return fmt.Errorf("reply_text callback failed")
		}
		return fmt.Errorf("reply_text callback failed: %s", result.Error)
	}
	return nil
}

func (runtimePluginEchoReplyBridge) ReplyImage(handle eventmodel.ReplyHandle, imageURL string) error {
	return fmt.Errorf("reply image not supported in Wave 1: %s %s", handle.TargetID, imageURL)
}

func (runtimePluginEchoReplyBridge) ReplyFile(handle eventmodel.ReplyHandle, fileURL string) error {
	return fmt.Errorf("reply file not supported in Wave 1: %s %s", handle.TargetID, fileURL)
}

func (b runtimePluginEchoReplyBridge) AdminRequest(request runtimecore.SubprocessAdminRequest) (runtimecore.SubprocessAdminResult, error) {
	result, err := b.requestCallback(runtimePluginEchoCallbackEnvelope{Type: "callback", Callback: "admin_request", AdminRequest: &request})
	if err != nil {
		return runtimecore.SubprocessAdminResult{}, fmt.Errorf("admin_request callback: %w", err)
	}
	if result.Type != "callback_result" {
		return runtimecore.SubprocessAdminResult{}, fmt.Errorf("unexpected admin_request callback result type %q", result.Type)
	}
	if result.Status != "ok" {
		if strings.TrimSpace(result.Error) == "" {
			return runtimecore.SubprocessAdminResult{}, fmt.Errorf("admin_request callback failed")
		}
		return runtimecore.SubprocessAdminResult{}, fmt.Errorf("admin_request callback failed: %s", result.Error)
	}
	if result.AdminRequest == nil {
		return runtimecore.SubprocessAdminResult{}, fmt.Errorf("admin_request callback missing result")
	}
	return *result.AdminRequest, nil
}

func (b runtimePluginEchoReplyBridge) AIChatQueue(request runtimecore.SubprocessAIChatQueueRequest) (runtimecore.SubprocessAIChatQueueResult, error) {
	result, err := b.requestCallback(runtimePluginEchoCallbackEnvelope{Type: "callback", Callback: "ai_chat_queue", AIChatQueue: &request})
	if err != nil {
		return runtimecore.SubprocessAIChatQueueResult{}, fmt.Errorf("ai_chat_queue callback: %w", err)
	}
	if result.Type != "callback_result" {
		return runtimecore.SubprocessAIChatQueueResult{}, fmt.Errorf("unexpected ai_chat_queue callback result type %q", result.Type)
	}
	if result.Status != "ok" {
		if strings.TrimSpace(result.Error) == "" {
			return runtimecore.SubprocessAIChatQueueResult{}, fmt.Errorf("ai_chat_queue callback failed")
		}
		return runtimecore.SubprocessAIChatQueueResult{}, fmt.Errorf("ai_chat_queue callback failed: %s", result.Error)
	}
	if result.AIChatQueue == nil {
		return runtimecore.SubprocessAIChatQueueResult{}, fmt.Errorf("ai_chat_queue callback missing result")
	}
	return *result.AIChatQueue, nil
}

func (b runtimePluginEchoReplyBridge) AIChatSession(request runtimecore.SubprocessAIChatSessionRequest) error {
	result, err := b.requestCallback(runtimePluginEchoCallbackEnvelope{Type: "callback", Callback: "ai_chat_session", AIChatSession: &request})
	if err != nil {
		return fmt.Errorf("ai_chat_session callback: %w", err)
	}
	if result.Type != "callback_result" {
		return fmt.Errorf("unexpected ai_chat_session callback result type %q", result.Type)
	}
	if result.Status != "ok" {
		if strings.TrimSpace(result.Error) == "" {
			return fmt.Errorf("ai_chat_session callback failed")
		}
		return fmt.Errorf("ai_chat_session callback failed: %s", result.Error)
	}
	return nil
}

func (b runtimePluginEchoReplyBridge) AIChatProvider(request runtimecore.SubprocessAIChatProviderRequest) (runtimecore.SubprocessAIChatProviderResult, error) {
	result, err := b.requestCallback(runtimePluginEchoCallbackEnvelope{Type: "callback", Callback: "ai_chat_provider", AIChatProvider: &request})
	if err != nil {
		return runtimecore.SubprocessAIChatProviderResult{}, fmt.Errorf("ai_chat_provider callback: %w", err)
	}
	if result.Type != "callback_result" {
		return runtimecore.SubprocessAIChatProviderResult{}, fmt.Errorf("unexpected ai_chat_provider callback result type %q", result.Type)
	}
	if result.Status != "ok" {
		if strings.TrimSpace(result.Error) == "" {
			return runtimecore.SubprocessAIChatProviderResult{}, fmt.Errorf("ai_chat_provider callback failed")
		}
		return runtimecore.SubprocessAIChatProviderResult{}, errors.New(strings.TrimSpace(result.Error))
	}
	if result.AIChatProvider == nil {
		return runtimecore.SubprocessAIChatProviderResult{}, fmt.Errorf("ai_chat_provider callback missing result")
	}
	return *result.AIChatProvider, nil
}

func (b runtimePluginEchoReplyBridge) WorkflowLoad(request runtimecore.SubprocessWorkflowLoadRequest) (runtimecore.WorkflowInstanceState, error) {
	result, err := b.requestCallback(runtimePluginEchoCallbackEnvelope{Type: "callback", Callback: "workflow_load", WorkflowLoad: &request})
	if err != nil {
		return runtimecore.WorkflowInstanceState{}, fmt.Errorf("workflow_load callback: %w", err)
	}
	if result.Type != "callback_result" {
		return runtimecore.WorkflowInstanceState{}, fmt.Errorf("unexpected workflow_load callback result type %q", result.Type)
	}
	if result.Status != "ok" {
		if strings.TrimSpace(result.Error) == "" {
			return runtimecore.WorkflowInstanceState{}, fmt.Errorf("workflow_load callback failed")
		}
		return runtimecore.WorkflowInstanceState{}, fmt.Errorf("workflow_load callback failed: %s", result.Error)
	}
	if result.WorkflowLoad == nil {
		return runtimecore.WorkflowInstanceState{}, fmt.Errorf("workflow_load callback missing result")
	}
	return *result.WorkflowLoad, nil
}

func (b runtimePluginEchoReplyBridge) requestCallback(envelope runtimePluginEchoCallbackEnvelope) (runtimePluginEchoCallbackResult, error) {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return runtimePluginEchoCallbackResult{}, fmt.Errorf("encode %s callback: %w", envelope.Callback, err)
	}
	if _, err := b.stdout.WriteString(string(encoded) + "\n"); err != nil {
		return runtimePluginEchoCallbackResult{}, fmt.Errorf("write %s callback: %w", envelope.Callback, err)
	}
	line, err := b.reader.ReadString('\n')
	if err != nil {
		return runtimePluginEchoCallbackResult{}, fmt.Errorf("read %s callback result: %w", envelope.Callback, err)
	}
	var result runtimePluginEchoCallbackResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &result); err != nil {
		return runtimePluginEchoCallbackResult{}, fmt.Errorf("decode %s callback result: %w", envelope.Callback, err)
	}
	return result, nil
}

func (b runtimePluginEchoReplyBridge) WorkflowStartOrResumeDetailed(workflowID, pluginID, traceID, eventType, eventID, runID, correlationID string, initial runtimecore.Workflow) (runtimecore.WorkflowTransition, runtimeWorkflowObservability, error) {
	result, err := b.requestCallback(runtimePluginEchoCallbackEnvelope{
		Type:     "callback",
		Callback: "workflow_start_or_resume",
		WorkflowStartOrResume: &runtimePluginEchoWorkflowStartOrResumeCallback{
			WorkflowID:    workflowID,
			PluginID:      pluginID,
			TraceID:       traceID,
			EventType:     eventType,
			EventID:       eventID,
			RunID:         runID,
			CorrelationID: correlationID,
			Initial:       initial,
		},
	})
	if err != nil {
		return runtimecore.WorkflowTransition{}, runtimeWorkflowObservability{}, fmt.Errorf("workflow_start_or_resume callback: %w", err)
	}
	if result.Type != "callback_result" {
		return runtimecore.WorkflowTransition{}, runtimeWorkflowObservability{}, fmt.Errorf("unexpected workflow_start_or_resume callback result type %q", result.Type)
	}
	if result.Status != "ok" {
		if strings.TrimSpace(result.Error) == "" {
			return runtimecore.WorkflowTransition{}, runtimeWorkflowObservability{}, fmt.Errorf("workflow_start_or_resume callback failed")
		}
		return runtimecore.WorkflowTransition{}, runtimeWorkflowObservability{}, fmt.Errorf("workflow_start_or_resume callback failed: %s", result.Error)
	}
	if len(result.WorkflowStartOrResume) == 0 {
		return runtimecore.WorkflowTransition{}, runtimeWorkflowObservability{}, fmt.Errorf("workflow_start_or_resume callback missing transition result")
	}
	var transition runtimecore.WorkflowTransition
	if err := json.Unmarshal(result.WorkflowStartOrResume, &transition); err != nil {
		return runtimecore.WorkflowTransition{}, runtimeWorkflowObservability{}, fmt.Errorf("decode workflow_start_or_resume transition: %w", err)
	}
	var snapshot runtimePluginEchoWorkflowTransitionSnapshot
	if err := json.Unmarshal(result.WorkflowStartOrResume, &snapshot); err != nil {
		return runtimecore.WorkflowTransition{}, runtimeWorkflowObservability{}, fmt.Errorf("decode workflow_start_or_resume observability snapshot: %w", err)
	}
	return transition, runtimeWorkflowObservability{
		TraceID:       strings.TrimSpace(snapshot.Instance.TraceID),
		EventID:       strings.TrimSpace(snapshot.Instance.EventID),
		PluginID:      strings.TrimSpace(snapshot.Instance.PluginID),
		RunID:         strings.TrimSpace(snapshot.Instance.RunID),
		CorrelationID: strings.TrimSpace(snapshot.Instance.CorrelationID),
	}, nil
}

func (b runtimePluginEchoReplyBridge) WorkflowStartOrResume(workflowID, pluginID, traceID, eventType, eventID, runID, correlationID string, initial runtimecore.Workflow) (runtimecore.WorkflowTransition, error) {
	transition, _, err := b.WorkflowStartOrResumeDetailed(workflowID, pluginID, traceID, eventType, eventID, runID, correlationID, initial)
	return transition, err
}

func (b runtimeSubprocessAdminAuthorizationBridge) Authorize(actor, permission, target string) pluginsdk.AuthorizationDecision {
	return b.AuthorizeTarget(actor, permission, pluginsdk.AuthorizationTargetPlugin, target)
}

func (b runtimeSubprocessAdminAuthorizationBridge) AuthorizeTarget(actor, permission string, kind pluginsdk.AuthorizationTargetKind, target string) pluginsdk.AuthorizationDecision {
	result, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{
		Action:     "authorize",
		Actor:      strings.TrimSpace(actor),
		Permission: strings.TrimSpace(permission),
		Target:     strings.TrimSpace(target),
		TargetKind: kind,
	})
	if err != nil {
		return pluginsdk.AuthorizationDecision{Allowed: false, Permission: strings.TrimSpace(permission), Reason: err.Error()}
	}
	if result.Authorization == nil {
		return pluginsdk.AuthorizationDecision{Allowed: false, Permission: strings.TrimSpace(permission), Reason: "authorization callback missing result"}
	}
	return *result.Authorization
}

func (b runtimeSubprocessAdminLifecycleBridge) Enable(pluginID string) error {
	_, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "enable_plugin", TargetPluginID: strings.TrimSpace(pluginID)})
	return err
}

func (b runtimeSubprocessAdminLifecycleBridge) Disable(pluginID string) error {
	_, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "disable_plugin", TargetPluginID: strings.TrimSpace(pluginID)})
	return err
}

func (b runtimeSubprocessAdminRolloutBridge) Prepare(pluginID string) (pluginsdk.RolloutRecord, error) {
	result, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "prepare_rollout", TargetPluginID: strings.TrimSpace(pluginID)})
	if err != nil {
		return pluginsdk.RolloutRecord{}, err
	}
	if result.RolloutRecord == nil {
		return pluginsdk.RolloutRecord{}, fmt.Errorf("prepare_rollout callback missing result")
	}
	return *result.RolloutRecord, nil
}

func (b runtimeSubprocessAdminRolloutBridge) Canary(pluginID string) (pluginsdk.RolloutRecord, error) {
	result, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "canary_rollout", TargetPluginID: strings.TrimSpace(pluginID)})
	if err != nil {
		return pluginsdk.RolloutRecord{}, err
	}
	if result.RolloutRecord == nil {
		return pluginsdk.RolloutRecord{}, fmt.Errorf("canary_rollout callback missing result")
	}
	return *result.RolloutRecord, nil
}

func (b runtimeSubprocessAdminRolloutBridge) CanActivate(pluginID string) (pluginsdk.RolloutRecord, error) {
	result, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "can_activate_rollout", TargetPluginID: strings.TrimSpace(pluginID)})
	if err != nil {
		return pluginsdk.RolloutRecord{}, err
	}
	if result.RolloutRecord == nil {
		return pluginsdk.RolloutRecord{}, fmt.Errorf("can_activate_rollout callback missing result")
	}
	return *result.RolloutRecord, nil
}

func (b runtimeSubprocessAdminRolloutBridge) Activate(pluginID string) (pluginsdk.RolloutRecord, error) {
	result, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "activate_rollout", TargetPluginID: strings.TrimSpace(pluginID)})
	if err != nil {
		return pluginsdk.RolloutRecord{}, err
	}
	if result.RolloutRecord == nil {
		return pluginsdk.RolloutRecord{}, fmt.Errorf("activate_rollout callback missing result")
	}
	return *result.RolloutRecord, nil
}

func (b runtimeSubprocessAdminRolloutBridge) CanRollback(pluginID string) (pluginsdk.RolloutRecord, error) {
	result, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "can_rollback_rollout", TargetPluginID: strings.TrimSpace(pluginID)})
	if err != nil {
		return pluginsdk.RolloutRecord{}, err
	}
	if result.RolloutRecord == nil {
		return pluginsdk.RolloutRecord{}, fmt.Errorf("can_rollback_rollout callback missing result")
	}
	return *result.RolloutRecord, nil
}

func (b runtimeSubprocessAdminRolloutBridge) Rollback(pluginID string) (pluginsdk.RolloutRecord, error) {
	result, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "rollback_rollout", TargetPluginID: strings.TrimSpace(pluginID)})
	if err != nil {
		return pluginsdk.RolloutRecord{}, err
	}
	if result.RolloutRecord == nil {
		return pluginsdk.RolloutRecord{}, fmt.Errorf("rollback_rollout callback missing result")
	}
	return *result.RolloutRecord, nil
}

func (b runtimeSubprocessAdminRolloutBridge) Record(pluginID string) (pluginsdk.RolloutRecord, bool) {
	result, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "load_rollout_record", TargetPluginID: strings.TrimSpace(pluginID)})
	if err != nil || result.RolloutRecord == nil {
		return pluginsdk.RolloutRecord{}, false
	}
	return *result.RolloutRecord, true
}

func (b runtimeSubprocessAdminReplayBridge) ReplayEvent(eventID string) (eventmodel.Event, error) {
	result, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "replay_event", EventID: strings.TrimSpace(eventID)})
	if err != nil {
		return eventmodel.Event{}, err
	}
	if result.Event == nil {
		return eventmodel.Event{}, fmt.Errorf("replay_event callback missing result")
	}
	return *result.Event, nil
}

func (b runtimeSubprocessAdminAuditBridge) RecordAudit(entry pluginsdk.AuditEntry) error {
	_, err := b.bridge.AdminRequest(runtimecore.SubprocessAdminRequest{Action: "record_audit", AuditEntry: &entry})
	return err
}

func (b runtimeSubprocessAIChatQueueBridge) Enqueue(_ context.Context, job pluginsdk.Job) error {
	_, err := b.bridge.AIChatQueue(runtimecore.SubprocessAIChatQueueRequest{Action: "enqueue", Job: &job})
	return err
}

func (b runtimeSubprocessAIChatQueueBridge) MarkRunning(_ context.Context, jobID string) (pluginsdk.Job, error) {
	return b.queueAction("mark_running", jobID, "")
}

func (b runtimeSubprocessAIChatQueueBridge) Complete(_ context.Context, jobID string) (pluginsdk.Job, error) {
	return b.queueAction("complete", jobID, "")
}

func (b runtimeSubprocessAIChatQueueBridge) Fail(_ context.Context, jobID string, reason string) (pluginsdk.Job, error) {
	return b.queueAction("fail", jobID, reason)
}

func (b runtimeSubprocessAIChatQueueBridge) Inspect(_ context.Context, jobID string) (pluginsdk.Job, error) {
	return b.queueAction("inspect", jobID, "")
}

func (b runtimeSubprocessAIChatQueueBridge) Cancel(_ context.Context, jobID string) (pluginsdk.Job, error) {
	return b.queueAction("cancel", jobID, "")
}

func (b runtimeSubprocessAIChatQueueBridge) queueAction(action string, jobID string, reason string) (pluginsdk.Job, error) {
	result, err := b.bridge.AIChatQueue(runtimecore.SubprocessAIChatQueueRequest{Action: action, JobID: strings.TrimSpace(jobID), Reason: strings.TrimSpace(reason)})
	if err != nil {
		return pluginsdk.Job{}, err
	}
	if result.Job == nil {
		return pluginsdk.Job{}, fmt.Errorf("ai chat queue %s callback missing job result", action)
	}
	return *result.Job, nil
}

func (b runtimeSubprocessAIChatSessionBridge) SaveSession(_ context.Context, session pluginsdk.SessionState) error {
	return b.bridge.AIChatSession(runtimecore.SubprocessAIChatSessionRequest{Session: session})
}

func (b runtimeSubprocessAIChatProviderBridge) Generate(_ context.Context, prompt string) (string, error) {
	result, err := b.bridge.AIChatProvider(runtimecore.SubprocessAIChatProviderRequest{Prompt: prompt})
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

func handleRuntimeSubprocessEvent(reader *bufio.Reader, stdout *os.File, pluginID string, rawPayload json.RawMessage, rawInstanceConfig json.RawMessage) error {
	switch strings.TrimSpace(pluginID) {
	case "", "plugin-echo":
		return handleRuntimePluginEchoEvent(reader, stdout, rawPayload, rawInstanceConfig)
	case "plugin-ai-chat":
		return handleRuntimePluginAIChatEvent(reader, stdout, rawPayload)
	case "plugin-workflow-demo":
		return handleRuntimePluginWorkflowDemoEvent(reader, stdout, rawPayload)
	default:
		return fmt.Errorf("unsupported runtime subprocess plugin %q", pluginID)
	}
}

func handleRuntimePluginEchoEvent(reader *bufio.Reader, stdout *os.File, rawPayload json.RawMessage, rawInstanceConfig json.RawMessage) error {
	var payload runtimePluginEchoEventPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("decode event payload: %w", err)
	}
	config := pluginecho.Config{}
	if len(rawInstanceConfig) > 0 {
		if err := json.Unmarshal(rawInstanceConfig, &config); err != nil {
			return fmt.Errorf("decode instance config: %w", err)
		}
	}
	plugin := pluginecho.New(runtimePluginEchoReplyBridge{reader: reader, stdout: stdout}, config)
	if err := plugin.OnEvent(payload.Event, payload.Ctx); err != nil {
		return err
	}
	_, _ = stdout.WriteString("{\"type\":\"event\",\"status\":\"ok\",\"message\":\"event-ok\"}\n")
	return nil
}

func handleRuntimePluginWorkflowDemoEvent(reader *bufio.Reader, stdout *os.File, rawPayload json.RawMessage) error {
	var payload runtimePluginEchoEventPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("decode event payload: %w", err)
	}
	if payload.Event.Type != "message.received" || payload.Event.Message == nil {
		_, _ = stdout.WriteString("{\"type\":\"event\",\"status\":\"ok\",\"message\":\"event-ok\"}\n")
		return nil
	}
	bridge := runtimePluginEchoReplyBridge{reader: reader, stdout: stdout}
	plugin := pluginworkflowdemo.New(bridge, runtimeSubprocessAIChatQueueBridge{bridge: bridge}, runtimeSubprocessWorkflowServiceBridge{bridge: bridge})
	if err := plugin.OnEvent(payload.Event, payload.Ctx); err != nil {
		return err
	}
	_, _ = stdout.WriteString("{\"type\":\"event\",\"status\":\"ok\",\"message\":\"event-ok\"}\n")
	return nil
}

type runtimeSubprocessWorkflowServiceBridge struct {
	bridge runtimePluginEchoReplyBridge
}

func (b runtimeSubprocessWorkflowServiceBridge) StartOrResume(ctx context.Context, workflowID string, pluginID string, eventType string, eventID string, initial runtimecore.Workflow) (runtimecore.WorkflowTransition, error) {
	if loader, ok := any(b).(interface {
		Load(context.Context, string) (runtimecore.WorkflowInstanceState, error)
	}); ok {
		_ = loader
	}
	observability := runtimecore.WorkflowObservabilityContextFromContext(ctx)
	transition, _, err := b.bridge.WorkflowStartOrResumeDetailed(workflowID, pluginID, observability.TraceID, eventType, eventID, observability.RunID, observability.CorrelationID, initial)
	return transition, err
}

func (b runtimeSubprocessWorkflowServiceBridge) ResumeFromChildJob(ctx context.Context, workflowID string, pluginID string, child runtimecore.WorkflowChildJobResume) (runtimecore.WorkflowTransition, error) {
	return runtimecore.WorkflowTransition{}, fmt.Errorf("subprocess workflow child-job resume is runtime-owned and must be delivered from queued job terminal outcomes")
}

func (b runtimeSubprocessWorkflowServiceBridge) Load(ctx context.Context, workflowID string) (runtimecore.WorkflowInstanceState, error) {
	return b.bridge.WorkflowLoad(runtimecore.SubprocessWorkflowLoadRequest{WorkflowID: strings.TrimSpace(workflowID)})
}

func handleRuntimePluginAIChatEvent(reader *bufio.Reader, stdout *os.File, rawPayload json.RawMessage) error {
	var payload runtimePluginEchoEventPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("decode event payload: %w", err)
	}
	bridge := runtimePluginEchoReplyBridge{reader: reader, stdout: stdout}
	plugin := pluginaichat.New(
		runtimeSubprocessAIChatQueueBridge{bridge: bridge},
		runtimeSubprocessAIChatProviderBridge{bridge: bridge},
		runtimeSubprocessAIChatSessionBridge{bridge: bridge},
		bridge,
	)
	if err := plugin.OnEvent(payload.Event, payload.Ctx); err != nil {
		return err
	}
	_, _ = stdout.WriteString("{\"type\":\"event\",\"status\":\"ok\",\"message\":\"event-ok\"}\n")
	return nil
}

func handleRuntimeSubprocessCommand(pluginID string, rawPayload json.RawMessage, _ json.RawMessage) error {
	pluginID = strings.TrimSpace(pluginID)
	if len(rawPayload) == 0 {
		return fmt.Errorf("missing command payload")
	}
	var payload runtimePluginCommandPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("decode command payload: %w", err)
	}
	if strings.TrimSpace(payload.Command.Name) == "" {
		return fmt.Errorf("command payload missing command name")
	}
	if err := payload.Ctx.Validate(); err != nil {
		return fmt.Errorf("validate command execution context: %w", err)
	}
	_ = pluginID
	return nil
}

func runRuntimeSubprocessCommandBridge(reader *bufio.Reader, stdout *os.File, pluginID string, rawPayload json.RawMessage, rawInstanceConfig json.RawMessage) error {
	if err := handleRuntimeSubprocessCommand(pluginID, rawPayload, rawInstanceConfig); err != nil {
		return err
	}
	switch strings.TrimSpace(pluginID) {
	case "plugin-admin":
		return handleRuntimePluginAdminCommand(reader, stdout, rawPayload)
	default:
		return nil
	}
}

func runRuntimeSubprocessJobBridge(reader *bufio.Reader, stdout *os.File, pluginID string, rawPayload json.RawMessage, rawInstanceConfig json.RawMessage) error {
	if err := handleRuntimeSubprocessJob(pluginID, rawPayload, rawInstanceConfig); err != nil {
		return err
	}
	switch strings.TrimSpace(pluginID) {
	case "plugin-ai-chat":
		return handleRuntimePluginAIChatJob(reader, stdout, rawPayload)
	default:
		return nil
	}
}

func handleRuntimePluginAdminCommand(reader *bufio.Reader, stdout *os.File, rawPayload json.RawMessage) error {
	var payload runtimePluginCommandPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("decode command payload: %w", err)
	}
	bridge := runtimePluginEchoReplyBridge{reader: reader, stdout: stdout}
	plugin := pluginadmin.New(
		runtimeSubprocessAdminLifecycleBridge{bridge: bridge},
		runtimeSubprocessAdminRolloutBridge{bridge: bridge},
		runtimeSubprocessAdminReplayBridge{bridge: bridge},
		nil,
		nil,
		nil,
		runtimeSubprocessAdminAuditBridge{bridge: bridge},
	)
	method := reflect.ValueOf(plugin).MethodByName("SetDecisionAuthorizer")
	if !method.IsValid() {
		return fmt.Errorf("plugin-admin decision authorizer setter is unavailable")
	}
	method.Call([]reflect.Value{reflect.ValueOf(runtimeSubprocessAdminAuthorizationBridge{bridge: bridge})})
	plugin.CurrentTime = time.Now().UTC
	return plugin.OnCommand(payload.Command, payload.Ctx)
}

func handleRuntimePluginAIChatJob(reader *bufio.Reader, stdout *os.File, rawPayload json.RawMessage) error {
	var payload runtimePluginJobPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("decode job payload: %w", err)
	}
	bridge := runtimePluginEchoReplyBridge{reader: reader, stdout: stdout}
	plugin := pluginaichat.New(
		runtimeSubprocessAIChatQueueBridge{bridge: bridge},
		runtimeSubprocessAIChatProviderBridge{bridge: bridge},
		runtimeSubprocessAIChatSessionBridge{bridge: bridge},
		bridge,
	)
	return plugin.OnJob(payload.Job, payload.Ctx)
}

func handleRuntimeSubprocessJob(pluginID string, rawPayload json.RawMessage, _ json.RawMessage) error {
	pluginID = strings.TrimSpace(pluginID)
	if len(rawPayload) == 0 {
		return fmt.Errorf("missing job payload")
	}
	var payload runtimePluginJobPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("decode job payload: %w", err)
	}
	if strings.TrimSpace(payload.Job.ID) == "" {
		return fmt.Errorf("job payload missing job id")
	}
	if strings.TrimSpace(payload.Job.Type) == "" {
		return fmt.Errorf("job payload missing job type")
	}
	if err := payload.Ctx.Validate(); err != nil {
		return fmt.Errorf("validate job execution context: %w", err)
	}
	if pluginID == "plugin-ai-chat" && payload.Ctx.Reply == nil {
		return fmt.Errorf("validate job execution context: reply handle is required for ai-chat job dispatch")
	}
	_ = pluginID
	return nil
}

func withRuntimeReplyObservabilityMetadata(handle eventmodel.ReplyHandle, observability runtimeWorkflowObservability) eventmodel.ReplyHandle {
	if handle.Metadata == nil {
		handle.Metadata = map[string]any{}
	} else {
		metadata := make(map[string]any, len(handle.Metadata)+5)
		for key, value := range handle.Metadata {
			metadata[key] = value
		}
		handle.Metadata = metadata
	}
	setRuntimeReplyObservability(handle.Metadata, `trace_id`, observability.TraceID)
	setRuntimeReplyObservability(handle.Metadata, `event_id`, observability.EventID)
	setRuntimeReplyObservability(handle.Metadata, `plugin_id`, observability.PluginID)
	setRuntimeReplyObservability(handle.Metadata, `run_id`, observability.RunID)
	setRuntimeReplyObservability(handle.Metadata, `correlation_id`, observability.CorrelationID)
	return handle
}

func setRuntimeReplyObservability(metadata map[string]any, key string, value string) {
	value = strings.TrimSpace(value)
	if metadata == nil || value == `` {
		return
	}
	metadata[key] = value
}
