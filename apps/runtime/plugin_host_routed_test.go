package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

type helperCallbackCapture struct {
	stdout string
	err    error
}

func TestRuntimePluginProcessFactoryUsesResolvedAbsoluteExecutablePath(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	executablePath := filepath.Join(workingDirectory, "runtime-launcher.test.exe")
	previousResolver := runtimeSubprocessExecutablePathResolver
	runtimeSubprocessExecutablePathResolver = func() (string, error) {
		return executablePath, nil
	}
	t.Cleanup(func() {
		runtimeSubprocessExecutablePathResolver = previousResolver
	})

	cmd, err := runtimePluginProcessFactory(runtimeSubprocessLauncherConfig{
		workingDirectory: workingDirectory,
		envAllowlist:     []string{"PATH"},
	})(context.Background())
	if err != nil {
		t.Fatalf("build subprocess command: %v", err)
	}
	if cmd.Path != executablePath {
		t.Fatalf("expected resolved executable path %q, got %q", executablePath, cmd.Path)
	}
	if !filepath.IsAbs(cmd.Path) {
		t.Fatalf("expected launcher path to be absolute, got %q", cmd.Path)
	}
	if cmd.Args[0] != executablePath {
		t.Fatalf("expected resolved executable to be used as argv[0], got %+v", cmd.Args)
	}
	if len(cmd.Args) != 4 || cmd.Args[1] != "-test.run=TestHelperRuntimePluginEchoSubprocess" || cmd.Args[2] != "--" || cmd.Args[3] != "-runtime-plugin-echo-subprocess" {
		t.Fatalf("expected helper-process args built from resolved executable path, got %+v", cmd.Args)
	}
	if cmd.Dir != workingDirectory {
		t.Fatalf("expected guarded working directory %q, got %q", workingDirectory, cmd.Dir)
	}
	foundHelperEnv := false
	for _, entry := range cmd.Env {
		if entry == "GO_WANT_RUNTIME_PLUGIN_ECHO_SUBPROCESS=1" {
			foundHelperEnv = true
			break
		}
	}
	if !foundHelperEnv {
		t.Fatalf("expected subprocess helper env marker, got %+v", cmd.Env)
	}
}

func TestRuntimePluginProcessFactoryFailsClosedWhenExecutableResolutionFails(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	previousResolver := runtimeSubprocessExecutablePathResolver
	runtimeSubprocessExecutablePathResolver = func() (string, error) {
		return "", errors.New("resolver exploded")
	}
	t.Cleanup(func() {
		runtimeSubprocessExecutablePathResolver = previousResolver
	})

	cmd, err := runtimePluginProcessFactory(runtimeSubprocessLauncherConfig{
		workingDirectory: workingDirectory,
		envAllowlist:     []string{"PATH"},
	})(context.Background())
	if err == nil {
		t.Fatal("expected executable resolution failure to block subprocess launch")
	}
	if cmd != nil {
		t.Fatalf("expected no subprocess command on resolution failure, got %+v", cmd)
	}
	if !strings.Contains(err.Error(), "subprocess launch guard blocked start:") || !strings.Contains(err.Error(), "resolver exploded") {
		t.Fatalf("expected fail-closed launch error, got %v", err)
	}
}

func TestCanonicalRuntimeSubprocessExecutablePathReturnsCanonicalAbsolutePath(t *testing.T) {
	t.Parallel()

	resolved, err := canonicalRuntimeSubprocessExecutablePath()
	if err != nil {
		t.Fatalf("resolve canonical executable path: %v", err)
	}
	if !filepath.IsAbs(resolved) {
		t.Fatalf("expected canonical executable path to be absolute, got %q", resolved)
	}
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve current executable path: %v", err)
	}
	executablePath, err = filepath.Abs(filepath.Clean(executablePath))
	if err != nil {
		t.Fatalf("resolve absolute executable path: %v", err)
	}
	expected, err := filepath.EvalSymlinks(executablePath)
	if err != nil {
		t.Fatalf("resolve expected canonical executable path: %v", err)
	}
	if !reflect.DeepEqual(filepath.Clean(resolved), filepath.Clean(expected)) {
		t.Fatalf("expected canonical executable path %q, got %q", expected, resolved)
	}
}

func TestRuntimePluginProcessFactoryPreservesWorkingDirectoryGuard(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	previousResolver := runtimeSubprocessExecutablePathResolver
	runtimeSubprocessExecutablePathResolver = func() (string, error) {
		return filepath.Join(workingDirectory, "runtime-launcher.test.exe"), nil
	}
	t.Cleanup(func() {
		runtimeSubprocessExecutablePathResolver = previousResolver
	})

	cmd, err := runtimePluginProcessFactory(runtimeSubprocessLauncherConfig{
		workingDirectory: filepath.Dir(workingDirectory),
		envAllowlist:     []string{"PATH"},
	})(context.Background())
	if err == nil {
		t.Fatal("expected working directory guard to fail closed")
	}
	if cmd != nil {
		t.Fatalf("expected no subprocess command when working directory guard rejects launch, got %+v", cmd)
	}
	if !strings.Contains(err.Error(), "subprocess launch guard blocked start:") || !strings.Contains(err.Error(), "escapes workspace root") {
		t.Fatalf("expected guarded working directory error, got %v", err)
	}
}

func TestRuntimeSubprocessHelperEnvelopeCarriesCommandPayloadBeyondEventOnlyBoundary(t *testing.T) {
	t.Parallel()

	request := hostRequestEnvelope{
		Type:     "command",
		PluginID: "plugin-admin",
		Command:  json.RawMessage(`{"command":{"name":"admin","raw":"/admin prepare plugin-echo","metadata":{"actor":"admin-user"}},"ctx":{"trace_id":"trace-admin-helper","event_id":"evt-admin-helper","plugin_id":"plugin-admin","run_id":"run-admin-helper","correlation_id":"corr-admin-helper"}}`),
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("encode command helper envelope: %v", err)
	}
	var decoded hostRequestEnvelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode command helper envelope: %v", err)
	}
	if decoded.Event != nil {
		t.Fatalf("expected command helper envelope to avoid event payload coupling, got %s", string(decoded.Event))
	}
	if len(decoded.Command) == 0 {
		t.Fatalf("expected command helper envelope to preserve command payload, got %s", string(encoded))
	}
	if err := handleRuntimeSubprocessCommand(decoded.PluginID, decoded.Command, nil); err != nil {
		t.Fatalf("expected command helper envelope to validate, got %v", err)
	}
	if !strings.Contains(string(encoded), `"command"`) || !strings.Contains(string(encoded), `"plugin_id":"plugin-admin"`) {
		t.Fatalf("expected encoded command helper envelope to expose command lane, got %s", string(encoded))
	}
}

func TestRuntimeSubprocessHelperEnvelopeCarriesJobPayloadBeyondEventOnlyBoundary(t *testing.T) {
	t.Parallel()

	reply := eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"}
	request := hostRequestEnvelope{
		Type:     "job",
		PluginID: "plugin-ai-chat",
		Job:      json.RawMessage(`{"job":{"id":"job-ai-chat-helper","type":"ai.chat","metadata":{"source":"runtime-job-queue"}},"ctx":{"trace_id":"trace-ai-helper","event_id":"evt-ai-helper","plugin_id":"plugin-ai-chat","run_id":"run-ai-helper","correlation_id":"corr-ai-helper","reply":{"capability":"onebot.reply","target_id":"group-42"}}}`),
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("encode job helper envelope: %v", err)
	}
	var decoded hostRequestEnvelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode job helper envelope: %v", err)
	}
	if decoded.Event != nil {
		t.Fatalf("expected job helper envelope to avoid event payload coupling, got %s", string(decoded.Event))
	}
	if len(decoded.Job) == 0 {
		t.Fatalf("expected job helper envelope to preserve job payload, got %s", string(encoded))
	}
	if err := handleRuntimeSubprocessJob(decoded.PluginID, decoded.Job, nil); err != nil {
		t.Fatalf("expected job helper envelope to validate, got %v", err)
	}
	var payload runtimePluginJobPayload
	if err := json.Unmarshal(decoded.Job, &payload); err != nil {
		t.Fatalf("decode typed job payload: %v", err)
	}
	if payload.Job.ID != "job-ai-chat-helper" || payload.Job.Type != "ai.chat" {
		t.Fatalf("unexpected typed job payload %+v", payload.Job)
	}
	if payload.Ctx.Reply == nil || !reflect.DeepEqual(*payload.Ctx.Reply, reply) {
		t.Fatalf("expected typed job payload to preserve reply handle, got %+v", payload.Ctx.Reply)
	}
	if !strings.Contains(string(encoded), `"job"`) || !strings.Contains(string(encoded), `"plugin_id":"plugin-ai-chat"`) {
		t.Fatalf("expected encoded job helper envelope to expose job lane, got %s", string(encoded))
	}
}

func TestRuntimeSubprocessHelperRejectsMalformedCommandAndJobPayloads(t *testing.T) {
	t.Parallel()

	if err := handleRuntimeSubprocessCommand("plugin-admin", json.RawMessage(`{"command":{"raw":"/admin prepare plugin-echo"},"ctx":{"trace_id":"trace-bad-command","event_id":"evt-bad-command"}}`), nil); err == nil || !strings.Contains(err.Error(), "command payload missing command name") {
		t.Fatalf("expected malformed command payload rejection, got %v", err)
	}
	if err := handleRuntimeSubprocessJob("plugin-ai-chat", json.RawMessage(`{"job":{"type":"ai.chat"},"ctx":{"trace_id":"trace-bad-job","event_id":"evt-bad-job","reply":{"capability":"onebot.reply","target_id":"group-42"}}}`), nil); err == nil || !strings.Contains(err.Error(), "job payload missing job id") {
		t.Fatalf("expected malformed job payload rejection, got %v", err)
	}
	if err := handleRuntimeSubprocessJob("plugin-ai-chat", json.RawMessage(`{"job":{"id":"job-bad-job","type":"ai.chat"},"ctx":{"trace_id":"trace-bad-job-no-reply","event_id":"evt-bad-job-no-reply"}}`), nil); err == nil || !strings.Contains(err.Error(), "validate job execution context") {
		t.Fatalf("expected missing reply validation for ai-chat-style job context, got %v", err)
	}
}

func TestRuntimeSubprocessPluginAdminCommandExecutesRealPluginSemanticsThroughBridge(t *testing.T) {
	t.Parallel()

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	defer func() { _ = stdinReader.Close() }()
	defer func() { _ = stdinWriter.Close() }()

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	defer func() { _ = stdoutReader.Close() }()
	defer func() { _ = stdoutWriter.Close() }()

	ackResultCh := make(chan helperCallbackCapture, 1)
	go func() {
		defer close(ackResultCh)
		reader := bufio.NewReader(stdoutReader)
		var captured strings.Builder
		for i := 0; i < 3; i++ {
			var callback runtimePluginEchoCallbackEnvelope
			line, err := reader.ReadString('\n')
			if err != nil {
				ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: err}
				return
			}
			captured.WriteString(line)
			if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &callback); err != nil {
				ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: err}
				return
			}
			result := runtimePluginEchoCallbackResult{Type: "callback_result", Status: "ok"}
			switch callback.Callback {
			case "admin_request":
				if callback.AdminRequest == nil {
					ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: errors.New("admin_request payload missing")}
					return
				}
				switch callback.AdminRequest.Action {
				case "authorize":
					decision := &pluginsdk.AuthorizationDecision{Allowed: true, Permission: callback.AdminRequest.Permission}
					result.AdminRequest = &runtimecore.SubprocessAdminResult{Authorization: decision}
				case "prepare_rollout":
					record := &pluginsdk.RolloutRecord{PluginID: callback.AdminRequest.TargetPluginID, Status: pluginsdk.RolloutStatusPrepared}
					result.AdminRequest = &runtimecore.SubprocessAdminResult{RolloutRecord: record}
				case "canary_rollout":
					record := &pluginsdk.RolloutRecord{PluginID: callback.AdminRequest.TargetPluginID, Phase: pluginsdk.RolloutPhaseCanary, Status: pluginsdk.RolloutStatusCanarying}
					result.AdminRequest = &runtimecore.SubprocessAdminResult{RolloutRecord: record}
				case "record_audit":
					result.AdminRequest = &runtimecore.SubprocessAdminResult{}
				default:
					ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: errors.New("unexpected admin action " + callback.AdminRequest.Action)}
					return
				}
			default:
				ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: errors.New("unexpected callback " + callback.Callback)}
				return
			}
			if _, err := stdinWriter.Write(append(mustMarshalRuntimeSubprocessCallbackResult(t, result), '\n')); err != nil {
				ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: err}
				return
			}
		}
		ackResultCh <- helperCallbackCapture{stdout: captured.String()}
	}()

	command := json.RawMessage(`{"command":{"name":"admin","raw":"/admin prepare plugin-echo","metadata":{"actor":"admin-user"}},"ctx":{"trace_id":"trace-admin-helper-real","event_id":"evt-admin-helper-real","plugin_id":"plugin-admin","run_id":"run-admin-helper-real","correlation_id":"corr-admin-helper-real"}}`)
	if err := handleRuntimePluginAdminCommand(bufio.NewReader(stdinReader), stdoutWriter, command); err != nil {
		t.Fatalf("execute real helper admin command: %v", err)
	}
	_ = stdoutWriter.Close()
	_ = stdinWriter.Close()
	var stdout string
	select {
	case result := <-ackResultCh:
		stdout = result.stdout
		if result.err != nil {
			t.Fatalf("callback ack loop: %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for helper callback acknowledgements")
	}
	for _, expected := range []string{`"callback":"admin_request"`, `"action":"authorize"`, `"action":"prepare_rollout"`, `"action":"record_audit"`, `"permission":"plugin:rollout"`, `"target_plugin_id":"plugin-echo"`} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected real helper admin callback evidence %q, got %s", expected, stdout)
		}
	}
}

func mustMarshalRuntimeSubprocessCallbackResult(t *testing.T, result runtimePluginEchoCallbackResult) []byte {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal callback result: %v", err)
	}
	return encoded
}

func TestHandleRuntimePluginAdminCommandSupportsCanaryAction(t *testing.T) {
	t.Parallel()

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	defer func() { _ = stdinReader.Close() }()
	defer func() { _ = stdinWriter.Close() }()

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	defer func() { _ = stdoutReader.Close() }()
	defer func() { _ = stdoutWriter.Close() }()

	ackResultCh := make(chan helperCallbackCapture, 1)
	go func() {
		defer close(ackResultCh)
		reader := bufio.NewReader(stdoutReader)
		var captured strings.Builder
		for i := 0; i < 3; i++ {
			var callback runtimePluginEchoCallbackEnvelope
			line, err := reader.ReadString('\n')
			if err != nil {
				ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: err}
				return
			}
			captured.WriteString(line)
			if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &callback); err != nil {
				ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: err}
				return
			}
			result := runtimePluginEchoCallbackResult{Type: "callback_result", Status: "ok"}
			switch callback.Callback {
			case "admin_request":
				if callback.AdminRequest == nil {
					ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: errors.New("admin_request payload missing")}
					return
				}
				switch callback.AdminRequest.Action {
				case "authorize":
					decision := &pluginsdk.AuthorizationDecision{Allowed: true, Permission: callback.AdminRequest.Permission}
					result.AdminRequest = &runtimecore.SubprocessAdminResult{Authorization: decision}
				case "canary_rollout":
					record := &pluginsdk.RolloutRecord{PluginID: callback.AdminRequest.TargetPluginID, Phase: pluginsdk.RolloutPhaseCanary, Status: pluginsdk.RolloutStatusCanarying}
					result.AdminRequest = &runtimecore.SubprocessAdminResult{RolloutRecord: record}
				case "record_audit":
					result.AdminRequest = &runtimecore.SubprocessAdminResult{}
				default:
					ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: errors.New("unexpected admin action " + callback.AdminRequest.Action)}
					return
				}
			default:
				ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: errors.New("unexpected callback " + callback.Callback)}
				return
			}
			if _, err := stdinWriter.Write(append(mustMarshalRuntimeSubprocessCallbackResult(t, result), '\n')); err != nil {
				ackResultCh <- helperCallbackCapture{stdout: captured.String(), err: err}
				return
			}
		}
		ackResultCh <- helperCallbackCapture{stdout: captured.String()}
	}()

	command := json.RawMessage(`{"command":{"name":"admin","raw":"/admin canary plugin-echo","metadata":{"actor":"admin-user"}},"ctx":{"trace_id":"trace-admin-canary","event_id":"evt-admin-canary","plugin_id":"plugin-admin","run_id":"run-admin-canary","correlation_id":"corr-admin-canary"}}`)
	if err := handleRuntimePluginAdminCommand(bufio.NewReader(stdinReader), stdoutWriter, command); err != nil {
		t.Fatalf("execute real helper admin canary command: %v", err)
	}
	_ = stdoutWriter.Close()
	_ = stdinWriter.Close()
	var stdout string
	select {
	case result := <-ackResultCh:
		stdout = result.stdout
		if result.err != nil {
			t.Fatalf("callback ack loop: %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canary helper callback acknowledgements")
	}
	for _, expected := range []string{`"callback":"admin_request"`, `"action":"authorize"`, `"action":"canary_rollout"`, `"action":"record_audit"`, `"permission":"plugin:rollout"`, `"target_plugin_id":"plugin-echo"`} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected real helper admin canary callback evidence %q, got %s", expected, stdout)
		}
	}
}
