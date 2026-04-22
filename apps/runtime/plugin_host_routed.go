package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
	pluginecho "github.com/ohmyopencode/bot-platform/plugins/plugin-echo"
)

type runtimeAppBuildOptions struct {
	pluginHostFactory func(*replyBuffer, *runtimecore.WorkflowRuntime) runtimecore.PluginHost
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
	return routedPluginHost{direct: direct, subprocess: subprocess, routes: routes, defaultHost: direct}
}

func newRoutedPluginHostWithEventScopes(direct runtimecore.PluginHost, subprocess runtimecore.PluginHost, subprocessPluginIDs []string, eventScopes map[string]map[string]struct{}) runtimecore.PluginHost {
	host, _ := newRoutedPluginHost(direct, subprocess, subprocessPluginIDs).(routedPluginHost)
	host.eventScopes = clonePluginEventScopes(eventScopes)
	return host
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

func (h routedPluginHost) Close() error {
	closed := map[runtimecore.PluginHost]struct{}{}
	var errs []error
	for _, candidate := range []runtimecore.PluginHost{h.defaultHost, h.direct, h.subprocess} {
		if candidate == nil {
			continue
		}
		if _, seen := closed[candidate]; seen {
			continue
		}
		closed[candidate] = struct{}{}
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
	configured := map[runtimecore.PluginHost]struct{}{}
	for _, candidate := range []runtimecore.PluginHost{h.defaultHost, h.direct, h.subprocess} {
		if candidate == nil {
			continue
		}
		if _, seen := configured[candidate]; seen {
			continue
		}
		configured[candidate] = struct{}{}
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

func newRuntimePluginHostWithEventScopes(replies *replyBuffer, workflowRuntime *runtimecore.WorkflowRuntime, subprocessPluginIDs []string, eventScopes map[string]map[string]struct{}) runtimecore.PluginHost {
	direct := runtimecore.DirectPluginHost{}
	launcherConfig := runtimeDefaultSubprocessLauncherConfig()
	subprocess := runtimecore.NewSubprocessPluginHostWithErrorFactory(runtimePluginProcessFactory(launcherConfig))
	subprocess.SetReplyTextCallback(replies.ReplyText)
	if workflowRuntime != nil {
		subprocess.SetWorkflowStartOrResumeCallback(func(ctx context.Context, request runtimecore.SubprocessWorkflowStartOrResumeRequest) (runtimecore.WorkflowTransition, error) {
			return workflowRuntime.StartOrResume(ctx, request.WorkflowID, request.PluginID, request.EventType, request.EventID, request.Initial)
		})
	}
	subprocess.SetRestartBudget(launcherConfig.restartBudget, launcherConfig.restartWindow)
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
			_, _ = stdout.WriteString("{\"type\":\"command\",\"status\":\"ok\",\"message\":\"command-ok\"}\n")
		case "job":
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
	InstanceConfig json.RawMessage `json:"instance_config,omitempty"`
}

type runtimePluginEchoEventPayload struct {
	Event eventmodel.Event            `json:"event"`
	Ctx   eventmodel.ExecutionContext `json:"ctx"`
}

type runtimePluginEchoCallbackEnvelope struct {
	Type                  string                                          `json:"type"`
	Callback              string                                          `json:"callback,omitempty"`
	ReplyText             *runtimePluginEchoReplyTextCallback             `json:"reply_text,omitempty"`
	WorkflowStartOrResume *runtimePluginEchoWorkflowStartOrResumeCallback `json:"workflow_start_or_resume,omitempty"`
}

type runtimePluginEchoReplyTextCallback struct {
	Handle eventmodel.ReplyHandle `json:"handle"`
	Text   string                 `json:"text"`
}

type runtimePluginEchoCallbackResult struct {
	Type                  string                          `json:"type"`
	Status                string                          `json:"status,omitempty"`
	Error                 string                          `json:"error,omitempty"`
	WorkflowStartOrResume *runtimecore.WorkflowTransition `json:"workflow_start_or_resume,omitempty"`
}

type runtimePluginEchoReplyBridge struct {
	reader *bufio.Reader
	stdout *os.File
}

type runtimePluginEchoWorkflowStartOrResumeCallback struct {
	WorkflowID string               `json:"workflow_id"`
	PluginID   string               `json:"plugin_id,omitempty"`
	EventType  string               `json:"event_type"`
	EventID    string               `json:"event_id,omitempty"`
	Initial    runtimecore.Workflow `json:"initial"`
}

func (b runtimePluginEchoReplyBridge) ReplyText(handle eventmodel.ReplyHandle, text string) error {
	encoded, err := json.Marshal(runtimePluginEchoCallbackEnvelope{
		Type:     "callback",
		Callback: "reply_text",
		ReplyText: &runtimePluginEchoReplyTextCallback{
			Handle: handle,
			Text:   text,
		},
	})
	if err != nil {
		return fmt.Errorf("encode reply_text callback: %w", err)
	}
	if _, err := b.stdout.WriteString(string(encoded) + "\n"); err != nil {
		return fmt.Errorf("write reply_text callback: %w", err)
	}
	line, err := b.reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read reply_text callback result: %w", err)
	}
	var result runtimePluginEchoCallbackResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &result); err != nil {
		return fmt.Errorf("decode reply_text callback result: %w", err)
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

func (b runtimePluginEchoReplyBridge) WorkflowStartOrResume(workflowID, pluginID, eventType, eventID string, initial runtimecore.Workflow) (runtimecore.WorkflowTransition, error) {
	encoded, err := json.Marshal(runtimePluginEchoCallbackEnvelope{
		Type:     "callback",
		Callback: "workflow_start_or_resume",
		WorkflowStartOrResume: &runtimePluginEchoWorkflowStartOrResumeCallback{
			WorkflowID: workflowID,
			PluginID:   pluginID,
			EventType:  eventType,
			EventID:    eventID,
			Initial:    initial,
		},
	})
	if err != nil {
		return runtimecore.WorkflowTransition{}, fmt.Errorf("encode workflow_start_or_resume callback: %w", err)
	}
	if _, err := b.stdout.WriteString(string(encoded) + "\n"); err != nil {
		return runtimecore.WorkflowTransition{}, fmt.Errorf("write workflow_start_or_resume callback: %w", err)
	}
	line, err := b.reader.ReadString('\n')
	if err != nil {
		return runtimecore.WorkflowTransition{}, fmt.Errorf("read workflow_start_or_resume callback result: %w", err)
	}
	var result runtimePluginEchoCallbackResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &result); err != nil {
		return runtimecore.WorkflowTransition{}, fmt.Errorf("decode workflow_start_or_resume callback result: %w", err)
	}
	if result.Type != "callback_result" {
		return runtimecore.WorkflowTransition{}, fmt.Errorf("unexpected workflow_start_or_resume callback result type %q", result.Type)
	}
	if result.Status != "ok" {
		if strings.TrimSpace(result.Error) == "" {
			return runtimecore.WorkflowTransition{}, fmt.Errorf("workflow_start_or_resume callback failed")
		}
		return runtimecore.WorkflowTransition{}, fmt.Errorf("workflow_start_or_resume callback failed: %s", result.Error)
	}
	if result.WorkflowStartOrResume == nil {
		return runtimecore.WorkflowTransition{}, fmt.Errorf("workflow_start_or_resume callback missing transition result")
	}
	return *result.WorkflowStartOrResume, nil
}

func handleRuntimeSubprocessEvent(reader *bufio.Reader, stdout *os.File, pluginID string, rawPayload json.RawMessage, rawInstanceConfig json.RawMessage) error {
	switch strings.TrimSpace(pluginID) {
	case "", "plugin-echo":
		return handleRuntimePluginEchoEvent(reader, stdout, rawPayload, rawInstanceConfig)
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
	workflowID := "workflow-anon"
	if payload.Event.Actor != nil && strings.TrimSpace(payload.Event.Actor.ID) != "" {
		workflowID = "workflow-" + payload.Event.Actor.ID
	}
	transition, err := bridge.WorkflowStartOrResume(
		workflowID,
		"plugin-workflow-demo",
		payload.Event.Type,
		payload.Event.EventID,
		runtimecore.NewWorkflow(
			workflowID,
			runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindPersist, Name: "greeting", Value: payload.Event.Message.Text},
			runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindWaitEvent, Name: "wait-confirm", Value: "message.received"},
			runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindCompensate, Name: "complete"},
		),
	)
	if err != nil {
		return err
	}
	if !transition.Started && !transition.Resumed {
		return fmt.Errorf("workflow_start_or_resume callback returned empty transition")
	}
	replyText := "workflow started, please send another message to continue"
	if transition.Resumed {
		replyText = "workflow resumed and completed"
	}
	if payload.Ctx.Reply != nil {
		if err := bridge.ReplyText(*payload.Ctx.Reply, replyText); err != nil {
			return err
		}
	}
	_, _ = stdout.WriteString("{\"type\":\"event\",\"status\":\"ok\",\"message\":\"event-ok\"}\n")
	return nil
}
