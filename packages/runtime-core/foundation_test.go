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
	}, map[string]any{"component": "runtime"})
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
}

func TestLoadConfigReadsYAMLAndAppliesEnvOverride(t *testing.T) {
	t.Setenv("BOT_PLATFORM_RUNTIME_ENVIRONMENT", "production")
	t.Setenv("BOT_PLATFORM_RUNTIME_LOG_LEVEL", "warn")
	t.Setenv("BOT_PLATFORM_RUNTIME_HTTP_PORT", "9090")
	t.Setenv("BOT_PLATFORM_RUNTIME_SMOKE_STORE_BACKEND", "postgres")
	t.Setenv("BOT_PLATFORM_RUNTIME_POSTGRES_DSN", "postgres://runtime:test@localhost/runtime_test?sslmode=disable")

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
	if cfg.Secrets.WebhookTokenRef != contract.DefaultDevRef {
		t.Fatalf("expected secret ref to load from yaml, got %+v", cfg.Secrets)
	}
}

func TestDevelopmentConfigWebhookSecretMainPathContractStaysAligned(t *testing.T) {
	t.Parallel()

	contract := WebhookSecretMainPathContract()
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
	if len(policy.ConfigRefs) != 1 || policy.ConfigRefs[0] != contract.ConfigRef {
		t.Fatalf("expected secret policy config ref %q, got %+v", contract.ConfigRef, policy.ConfigRefs)
	}
	if len(policy.IntegrationPoints) != 2 || policy.IntegrationPoints[0] != contract.ConfigRef || policy.IntegrationPoints[1] != contract.Consumer {
		t.Fatalf("expected secret policy integration points [%q %q], got %+v", contract.ConfigRef, contract.Consumer, policy.IntegrationPoints)
	}
	if !strings.Contains(strings.Join(policy.Facts, " "), contract.PathNote) {
		t.Fatalf("expected secret policy facts to include path note %q, got %+v", contract.PathNote, policy.Facts)
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
	raw := []byte("runtime:\n  environment: development\n  log_level: debug\n  http_port: 8080\n  bot_instances:\n    - id: adapter-onebot-alpha\n      adapter: onebot\n      source: onebot-alpha\n      platform: onebot/v11\n      demo_path: /demo/onebot/message\n      self_id: 10001\n    - id: adapter-onebot-beta\n      adapter: onebot\n      source: onebot-beta\n      demo_path: /demo/onebot/message-beta\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Runtime.BotInstances) != 2 {
		t.Fatalf("expected two bot instances, got %+v", cfg.Runtime.BotInstances)
	}
	alpha := cfg.Runtime.BotInstances[0]
	if alpha.ID != "adapter-onebot-alpha" || alpha.Adapter != "onebot" || alpha.Source != "onebot-alpha" || alpha.Platform != "onebot/v11" || alpha.DemoPath != "/demo/onebot/message" || alpha.SelfID != 10001 {
		t.Fatalf("unexpected alpha bot instance %+v", alpha)
	}
	beta := cfg.Runtime.BotInstances[1]
	if beta.ID != "adapter-onebot-beta" || beta.Adapter != "onebot" || beta.Source != "onebot-beta" || beta.Platform != "onebot/v11" || beta.DemoPath != "/demo/onebot/message-beta" || beta.SelfID != 0 {
		t.Fatalf("unexpected beta bot instance %+v", beta)
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
