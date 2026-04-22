package runtimecore

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
)

func TestLoggerWritesStructuredEntryWithDefaultFields(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	logger.now = func() time.Time {
		return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	}

	err := logger.Log("info", "runtime started", LogContext{
		TraceID:       "trace-1",
		EventID:       "event-1",
		PluginID:      "plugin-echo",
		RunID:         "run-1",
		CorrelationID: "corr-1",
	}, BaselineLogFields("runtime", "startup", nil))
	if err != nil {
		t.Fatalf("log entry: %v", err)
	}

	var entry LogEntry
	if err := json.Unmarshal(buffer.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal log entry: %v", err)
	}

	if entry.TraceID != "trace-1" || entry.EventID != "event-1" || entry.PluginID != "plugin-echo" || entry.RunID != "run-1" || entry.CorrelationID != "corr-1" {
		t.Fatal("expected structured log to include default trace fields")
	}
	if entry.Fields["component"] != "runtime" || entry.Fields["operation"] != "startup" {
		t.Fatalf("expected structured log baseline fields, got %+v", entry.Fields)
	}
}

func TestBaselineLogFieldsCreatesBaselineWithoutExistingFields(t *testing.T) {
	t.Parallel()

	fields := BaselineLogFields("runtime", "startup", nil)
	if len(fields) == 0 {
		t.Fatal("expected baseline fields to be created for empty input")
	}
	if fields["component"] != "runtime" || fields["operation"] != "startup" {
		t.Fatalf("expected baseline-only fields, got %+v", fields)
	}
}

func TestLoggerNormalizesBaselineErrorFields(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	logger.now = func() time.Time {
		return time.Date(2026, 4, 2, 12, 1, 0, 0, time.UTC)
	}

	err := logger.Log("error", "runtime dispatch failed", LogContext{TraceID: "trace-2"}, FailureLogFields("runtime", "dispatch.event", context.DeadlineExceeded, "response_timeout", map[string]any{"dispatch_kind": "event"}))
	if err != nil {
		t.Fatalf("log entry: %v", err)
	}

	var entry LogEntry
	if err := json.Unmarshal(buffer.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal log entry: %v", err)
	}
	if entry.Fields["component"] != "runtime" || entry.Fields["operation"] != "dispatch.event" {
		t.Fatalf("expected baseline component/operation fields, got %+v", entry.Fields)
	}
	if entry.Fields["error_category"] != "timeout" || entry.Fields["error_code"] != "response_timeout" {
		t.Fatalf("expected normalized timeout error fields, got %+v", entry.Fields)
	}
}

func TestLoadConfigReadsYAMLAndAppliesEnvOverride(t *testing.T) {
	t.Setenv("BOT_PLATFORM_RUNTIME_ENVIRONMENT", "production")
	t.Setenv("BOT_PLATFORM_RUNTIME_LOG_LEVEL", "warn")
	t.Setenv("BOT_PLATFORM_RUNTIME_HTTP_PORT", "9090")
	t.Setenv("BOT_PLATFORM_RUNTIME_SMOKE_STORE_BACKEND", "postgres")
	t.Setenv("BOT_PLATFORM_RUNTIME_POSTGRES_DSN", "postgres://runtime:test@localhost/runtime_test?sslmode=disable")
	t.Setenv("BOT_PLATFORM_TRACING_EXPORTER_ENABLED", "true")
	t.Setenv("BOT_PLATFORM_TRACING_EXPORTER_KIND", "test")
	t.Setenv("BOT_PLATFORM_TRACING_EXPORTER_ENDPOINT", "memory://active2")

	configPath := filepath.Join("..", "..", "deploy", "config.dev.yaml")
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	contract := WebhookSecretMainPathContract()

	if cfg.Runtime.Environment != "production" || cfg.Runtime.LogLevel != "warn" || cfg.Runtime.HTTPPort != 9090 {
		t.Fatalf("expected env override to win, got %+v", cfg.Runtime)
	}
	if cfg.Runtime.SmokeStoreBackend != "postgres" || cfg.Runtime.PostgresDSN != "postgres://runtime:test@localhost/runtime_test?sslmode=disable" {
		t.Fatalf("expected smoke store env override to win, got %+v", cfg.Runtime)
	}
	if !cfg.Tracing.Exporter.Enabled || cfg.Tracing.Exporter.Kind != "test" || cfg.Tracing.Exporter.Endpoint != "memory://active2" {
		t.Fatalf("expected tracing exporter env override to win, got %+v", cfg.Tracing)
	}
	if cfg.Secrets.WebhookTokenRef != contract.DefaultDevRef {
		t.Fatalf("expected secret ref to load from yaml, got %+v", cfg.Secrets)
	}
}

func TestLoadConfigDefaultsTracingExporterDisabled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Tracing.Exporter.Enabled {
		t.Fatalf("expected tracing exporter disabled by default, got %+v", cfg.Tracing)
	}
	if cfg.Tracing.Exporter.Kind != "otlp" {
		t.Fatalf("expected default tracing exporter kind otlp, got %+v", cfg.Tracing)
	}
}

func TestDevelopmentConfigWebhookSecretMainPathContractStaysAligned(t *testing.T) {
	t.Parallel()

	contract := WebhookSecretMainPathContract()
	aiContract := AIChatAPIKeySecretContract()
	configPath := filepath.Join("..", "..", "deploy", "config.dev.yaml")
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	policy := SecretPolicy()

	if cfg.Secrets.WebhookTokenRef != contract.DefaultDevRef {
		t.Fatalf("expected dev config secret ref %q, got %q", contract.DefaultDevRef, cfg.Secrets.WebhookTokenRef)
	}
	if policy.MainPathContract != contract {
		t.Fatalf("expected secret policy main path contract %+v, got %+v", contract, policy.MainPathContract)
	}
	if len(policy.ConfigRefs) != 2 || policy.ConfigRefs[0] != contract.ConfigRef || policy.ConfigRefs[1] != aiContract.ConfigRef {
		t.Fatalf("expected secret policy config ref %q, got %+v", contract.ConfigRef, policy.ConfigRefs)
	}
	if len(policy.AdditionalContracts) != 1 || policy.AdditionalContracts[0] != aiContract {
		t.Fatalf("expected ai-chat secret policy additional contract %+v, got %+v", aiContract, policy.AdditionalContracts)
	}
	if len(policy.IntegrationPoints) != 4 || policy.IntegrationPoints[0] != contract.ConfigRef || policy.IntegrationPoints[1] != contract.Consumer || policy.IntegrationPoints[2] != aiContract.ConfigRef || policy.IntegrationPoints[3] != aiContract.Consumer {
		t.Fatalf("expected secret policy integration points [%q %q %q %q], got %+v", contract.ConfigRef, contract.Consumer, aiContract.ConfigRef, aiContract.Consumer, policy.IntegrationPoints)
	}
	if !strings.Contains(strings.Join(policy.Facts, " "), contract.PathNote) || !strings.Contains(strings.Join(policy.Facts, " "), aiContract.PathNote) {
		t.Fatalf("expected secret policy facts to include path notes %q and %q, got %+v", contract.PathNote, aiContract.PathNote, policy.Facts)
	}
}

func TestLoadConfigRejectsInvalidAIChatSecretRef(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\nai_chat:\n  provider: openai_compat\n  endpoint: https://example.invalid/v1/chat/completions\n  model: test-model\nsecrets:\n  ai_chat_api_key_ref: ai.chat.key\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadConfig(path)
	expectedErr := "invalid " + AIChatAPIKeySecretConfigRef() + ": secret ref must use BOT_PLATFORM_ prefix"
	if err == nil || err.Error() != expectedErr {
		t.Fatalf("expected invalid ai chat secret ref config error, got %v", err)
	}
}

func TestLoadConfigRejectsInsecureNonLoopbackOpenAICompatEndpoint(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\nai_chat:\n  provider: openai_compat\n  endpoint: http://example.invalid/v1/chat/completions\n  model: test-model\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "ai_chat.endpoint must use https unless it targets localhost or loopback") {
		t.Fatalf("expected insecure non-loopback openai endpoint rejection, got %v", err)
	}
}

func TestLoadConfigAllowsLoopbackHTTPForOpenAICompatEndpoint(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"http://localhost:11434/v1/chat/completions",
		"http://127.0.0.1:8081/v1/chat/completions",
		"http://[::1]:8082/v1/chat/completions",
	} {
		t.Run(endpoint, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\nai_chat:\n  provider: openai_compat\n  endpoint: " + endpoint + "\n  model: test-model\nsecrets:\n  ai_chat_api_key_ref: BOT_PLATFORM_AI_CHAT_API_KEY\n")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("expected loopback openai endpoint to pass validation, got %v", err)
			}
			if cfg.AIChat.Endpoint != endpoint || cfg.AIChat.Provider != "openai_compat" {
				t.Fatalf("unexpected ai config %+v", cfg.AIChat)
			}
		})
	}
}

func TestDevelopmentTemplateIsReadable(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "deploy", "config.dev.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config template: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("expected config template to be non-empty")
	}
}

func TestDevelopmentConfigIncludesOptionalOpenAICompatEntryPath(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join("..", "..", "deploy", "config.dev.yaml")
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AIChat.Provider != "mock" || cfg.AIChat.Endpoint != "https://api.openai.com/v1/chat/completions" || cfg.AIChat.Model != "gpt-4.1-mini" {
		t.Fatalf("expected development config to include optional real-provider ai chat entry while defaulting to mock, got %+v", cfg.AIChat)
	}
	if cfg.Secrets.AIChatAPIKeyRef != "BOT_PLATFORM_AI_CHAT_API_KEY" {
		t.Fatalf("expected development config ai chat secret ref hook, got %+v", cfg.Secrets)
	}
}

func TestLoadConfigRejectsInvalidWebhookSecretRef(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\nsecrets:\n  webhook_token_ref: db.password\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadConfig(path)
	expectedErr := "invalid " + WebhookSecretMainPathContract().ConfigRef + ": secret ref must use BOT_PLATFORM_ prefix"
	if err == nil || err.Error() != expectedErr {
		t.Fatalf("expected invalid secret ref config error, got %v", err)
	}
}

func TestLoadConfigReadsRuntimeRBACSection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\nrbac:\n  actor_roles:\n    admin-user: [admin]\n  policies:\n    admin:\n      permissions: [plugin:enable, plugin:disable, plugin:rollout]\n      plugin_scope: ['*']\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RBAC == nil {
		t.Fatal("expected RBAC config to be present")
	}
	if len(cfg.RBAC.ActorRoles["admin-user"]) != 1 || cfg.RBAC.ActorRoles["admin-user"][0] != "admin" {
		t.Fatalf("unexpected actor roles %+v", cfg.RBAC.ActorRoles)
	}
	policy := cfg.RBAC.Policies["admin"]
	if len(policy.Permissions) != 3 || len(policy.PluginScope) != 1 || policy.PluginScope[0] != "*" {
		t.Fatalf("unexpected RBAC policy %+v", policy)
	}
}

func TestLoadConfigReadsConsoleReadPermission(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\nrbac:\n  console_read_permission: console:read\n  actor_roles:\n    viewer-user: [viewer]\n  policies:\n    viewer:\n      permissions: [console:read]\n      plugin_scope: ['console']\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RBAC == nil || cfg.RBAC.ConsoleReadPermission != "console:read" {
		t.Fatalf("expected console_read_permission to load, got %+v", cfg.RBAC)
	}
}

func TestLoadConfigReadsRuntimeBotInstances(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\n  bot_instances:\n    - id: adapter-onebot-alpha\n      adapter: onebot\n      source: onebot-alpha\n      platform: onebot/v11\n      path: /demo/onebot/message\n      demo_path: /demo/onebot/message\n      self_id: 10001\n    - id: adapter-onebot-beta\n      adapter: onebot\n      source: onebot-beta\n      path: /demo/onebot/message-beta\n    - id: adapter-webhook-main\n      adapter: webhook\n      source: webhook-main\n      path: /ingress/webhook/main\nsecrets:\n  webhook_token_ref: BOT_PLATFORM_WEBHOOK_TOKEN\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Runtime.BotInstances) != 3 {
		t.Fatalf("expected two bot instances, got %+v", cfg.Runtime.BotInstances)
	}
	alpha := cfg.Runtime.BotInstances[0]
	if alpha.ID != "adapter-onebot-alpha" || alpha.Adapter != "onebot" || alpha.Source != "onebot-alpha" || alpha.Platform != "onebot/v11" || alpha.Path != "/demo/onebot/message" || alpha.DemoPath != "/demo/onebot/message" || alpha.SelfID != 10001 {
		t.Fatalf("unexpected alpha bot instance %+v", alpha)
	}
	beta := cfg.Runtime.BotInstances[1]
	if beta.ID != "adapter-onebot-beta" || beta.Adapter != "onebot" || beta.Source != "onebot-beta" || beta.Platform != "onebot/v11" || beta.Path != "/demo/onebot/message-beta" || beta.DemoPath != "/demo/onebot/message-beta" || beta.SelfID != 0 {
		t.Fatalf("unexpected beta bot instance %+v", beta)
	}
	webhook := cfg.Runtime.BotInstances[2]
	if webhook.ID != "adapter-webhook-main" || webhook.Adapter != "webhook" || webhook.Source != "webhook-main" || webhook.Platform != "webhook/http" || webhook.Path != "/ingress/webhook/main" || webhook.DemoPath != "" {
		t.Fatalf("unexpected webhook bot instance %+v", webhook)
	}
}

func TestLoadConfigRejectsWebhookBotInstanceWithoutSecretRef(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\n  bot_instances:\n    - id: adapter-webhook-main\n      adapter: webhook\n      source: webhook-main\n      path: /ingress/webhook/main\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadConfig(path)
	expected := "secrets.webhook_token_ref is required when runtime.bot_instances includes adapter \"webhook\""
	if err == nil || err.Error() != expected {
		t.Fatalf("expected missing webhook secret ref error, got %v", err)
	}
}

func TestLoadConfigTreatsExplicitEmptyRBACAsPresent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\nrbac: {}\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RBAC == nil {
		t.Fatal("expected explicit empty rbac config to remain present")
	}
}

func TestLoadConfigRejectsInvalidRBACPermissionFormat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\nrbac:\n  actor_roles:\n    admin-user: [admin]\n  policies:\n    admin:\n      permissions: [plugin-enable]\n      plugin_scope: ['*']\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "invalid rbac policy \"admin\"") {
		t.Fatalf("expected invalid RBAC permission format error, got %v", err)
	}
}

func TestLoadConfigRejectsInvalidConsoleReadPermissionFormat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\nrbac:\n  console_read_permission: console-read\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "invalid console_read_permission") {
		t.Fatalf("expected invalid console_read_permission error, got %v", err)
	}
}

func TestNewAdminCommandAuthorizerTreatsExplicitEmptyRBACAsDenyAll(t *testing.T) {
	t.Parallel()

	authorizer := NewAdminCommandAuthorizer(&RBACConfig{})
	if authorizer == nil {
		t.Fatal("expected explicit empty RBAC config to still create an authorizer")
	}
	err := authorizer.AuthorizeCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-echo", Metadata: map[string]any{"actor": "admin-user"}}, eventmodel.ExecutionContext{})
	if err == nil || err.Error() != "permission denied" {
		t.Fatalf("expected explicit empty RBAC config to deny command, got %v", err)
	}
}
