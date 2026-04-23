package runtimecore

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Runtime      RuntimeConfig       `yaml:"runtime"`
	Tracing      TracingConfig       `yaml:"tracing,omitempty"`
	AIChat       AIChatConfig        `yaml:"ai_chat,omitempty"`
	Secrets      SecretsConfig       `yaml:"secrets,omitempty"`
	RBAC         *RBACConfig         `yaml:"rbac,omitempty"`
	OperatorAuth *OperatorAuthConfig `yaml:"operator_auth,omitempty"`
}

type RuntimeConfig struct {
	Environment         string               `yaml:"environment"`
	LogLevel            string               `yaml:"log_level"`
	HTTPPort            int                  `yaml:"http_port"`
	SQLitePath          string               `yaml:"sqlite_path,omitempty"`
	SmokeStoreBackend   string               `yaml:"smoke_store_backend,omitempty"`
	PostgresDSN         string               `yaml:"postgres_dsn,omitempty"`
	SchedulerIntervalMs int                  `yaml:"scheduler_interval_ms,omitempty"`
	BotInstances        []RuntimeBotInstance `yaml:"bot_instances,omitempty"`
}

type RuntimeBotInstance struct {
	ID       string `yaml:"id,omitempty"`
	Adapter  string `yaml:"adapter,omitempty"`
	Source   string `yaml:"source,omitempty"`
	Platform string `yaml:"platform,omitempty"`
	Path     string `yaml:"path,omitempty"`
	DemoPath string `yaml:"demo_path,omitempty"`
	SelfID   int64  `yaml:"self_id,omitempty"`
}

type TracingConfig struct {
	Exporter TracingExporterConfig `yaml:"exporter,omitempty"`
}

type TracingExporterConfig struct {
	Enabled  bool   `yaml:"enabled,omitempty"`
	Kind     string `yaml:"kind,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
}

type AIChatConfig struct {
	Provider         string `yaml:"provider,omitempty"`
	Endpoint         string `yaml:"endpoint,omitempty"`
	Model            string `yaml:"model,omitempty"`
	RequestTimeoutMs int    `yaml:"request_timeout_ms,omitempty"`
}

type SecretsConfig struct {
	WebhookTokenRef string `yaml:"webhook_token_ref,omitempty"`
	AIChatAPIKeyRef string `yaml:"ai_chat_api_key_ref,omitempty"`
}

type RBACConfig struct {
	ActorRoles            map[string][]string                      `yaml:"actor_roles,omitempty"`
	Policies              map[string]pluginsdk.AuthorizationPolicy `yaml:"policies,omitempty"`
	ConsoleReadPermission string                                   `yaml:"console_read_permission,omitempty"`
}

type OperatorAuthConfig struct {
	Tokens []OperatorAuthTokenConfig `yaml:"tokens,omitempty"`
}

type OperatorAuthTokenConfig struct {
	ID       string `yaml:"id,omitempty"`
	ActorID  string `yaml:"actor_id,omitempty"`
	TokenRef string `yaml:"token_ref,omitempty"`
}

func (c *RBACConfig) Validate() error {
	if c == nil {
		return nil
	}
	for role, policy := range c.Policies {
		for _, permission := range policy.Permissions {
			if err := validateRBACPermission(permission); err != nil {
				return fmt.Errorf("invalid rbac policy %q: %w", role, err)
			}
		}
	}
	if c.ConsoleReadPermission != "" {
		if err := validateRBACPermission(c.ConsoleReadPermission); err != nil {
			return fmt.Errorf("invalid console_read_permission: %w", err)
		}
	}
	return nil
}

func (c *OperatorAuthConfig) Validate(rbac *RBACConfig) error {
	if c == nil {
		return nil
	}
	seenIDs := make(map[string]struct{}, len(c.Tokens))
	for index, token := range c.Tokens {
		if err := validateOperatorAuthTokenID(token.ID); err != nil {
			return fmt.Errorf("invalid operator_auth.tokens[%d].id: %w", index, err)
		}
		if _, exists := seenIDs[token.ID]; exists {
			return fmt.Errorf("operator_auth.tokens[%d].id %q must be unique", index, token.ID)
		}
		seenIDs[token.ID] = struct{}{}
		if err := validateOperatorAuthActorID(token.ActorID); err != nil {
			return fmt.Errorf("invalid operator_auth.tokens[%d].actor_id: %w", index, err)
		}
		if err := ValidateSecretRef(token.TokenRef); err != nil {
			return fmt.Errorf("invalid operator_auth.tokens[%d].token_ref: %w", index, err)
		}
		if !operatorAuthActorExists(rbac, token.ActorID) {
			return fmt.Errorf("operator_auth.tokens[%d].actor_id %q must reference an existing rbac.actor_roles entry", index, token.ActorID)
		}
	}
	return nil
}

func validateOperatorAuthTokenID(tokenID string) error {
	if strings.TrimSpace(tokenID) == "" {
		return fmt.Errorf("token id is required")
	}
	if tokenID != strings.TrimSpace(tokenID) {
		return fmt.Errorf("token id must not contain leading or trailing whitespace")
	}
	return nil
}

func validateOperatorAuthActorID(actorID string) error {
	if strings.TrimSpace(actorID) == "" {
		return fmt.Errorf("actor id is required")
	}
	if actorID != strings.TrimSpace(actorID) {
		return fmt.Errorf("actor id must not contain leading or trailing whitespace")
	}
	return nil
}

func operatorAuthActorExists(rbac *RBACConfig, actorID string) bool {
	if rbac == nil || len(rbac.ActorRoles) == 0 {
		return false
	}
	_, exists := rbac.ActorRoles[actorID]
	return exists
}

func validateRBACPermission(permission string) error {
	trimmed := strings.TrimSpace(permission)
	if trimmed == "" {
		return fmt.Errorf("permission is required")
	}
	if trimmed != permission {
		return fmt.Errorf("permission %q must not contain surrounding whitespace", permission)
	}
	resource, action, found := strings.Cut(trimmed, ":")
	if !found || strings.Contains(action, ":") {
		return fmt.Errorf("permission %q must use resource:action format", permission)
	}
	if strings.TrimSpace(resource) == "" || strings.TrimSpace(action) == "" {
		return fmt.Errorf("permission %q must use resource:action format", permission)
	}
	return nil
}

func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal yaml: %w", err)
	}
	if cfg.Secrets.WebhookTokenRef != "" {
		if err := ValidateSecretRef(cfg.Secrets.WebhookTokenRef); err != nil {
			return Config{}, fmt.Errorf("invalid %s: %w", WebhookSecretMainPathContract().ConfigRef, err)
		}
	}
	if cfg.Secrets.AIChatAPIKeyRef != "" {
		if err := ValidateSecretRef(cfg.Secrets.AIChatAPIKeyRef); err != nil {
			return Config{}, fmt.Errorf("invalid %s: %w", AIChatAPIKeySecretConfigRef(), err)
		}
	}
	if err := cfg.RBAC.Validate(); err != nil {
		return Config{}, err
	}
	if err := cfg.OperatorAuth.Validate(cfg.RBAC); err != nil {
		return Config{}, err
	}

	applyEnvOverride(&cfg)
	applyRuntimeDefaults(&cfg)
	if err := validateAIChatConfig(cfg.AIChat); err != nil {
		return Config{}, err
	}
	if err := validateRuntimeBotInstances(cfg.Runtime.BotInstances, cfg.Secrets); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnvOverride(cfg *Config) {
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_ENVIRONMENT")); value != "" {
		cfg.Runtime.Environment = value
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_LOG_LEVEL")); value != "" {
		cfg.Runtime.LogLevel = value
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_HTTP_PORT")); value != "" {
		if port, err := strconv.Atoi(value); err == nil {
			cfg.Runtime.HTTPPort = port
		}
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_SQLITE_PATH")); value != "" {
		cfg.Runtime.SQLitePath = value
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_SMOKE_STORE_BACKEND")); value != "" {
		cfg.Runtime.SmokeStoreBackend = value
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_POSTGRES_DSN")); value != "" {
		cfg.Runtime.PostgresDSN = value
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_SCHEDULER_INTERVAL_MS")); value != "" {
		if interval, err := strconv.Atoi(value); err == nil {
			cfg.Runtime.SchedulerIntervalMs = interval
		}
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_TRACING_EXPORTER_ENABLED")); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err == nil {
			cfg.Tracing.Exporter.Enabled = enabled
		}
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_TRACING_EXPORTER_KIND")); value != "" {
		cfg.Tracing.Exporter.Kind = value
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_TRACING_EXPORTER_ENDPOINT")); value != "" {
		cfg.Tracing.Exporter.Endpoint = value
	}
}

func applyRuntimeDefaults(cfg *Config) {
	if cfg.Runtime.SQLitePath == "" {
		cfg.Runtime.SQLitePath = filepath.Join("data", "dev", "runtime.sqlite")
	}
	cfg.AIChat.Provider = strings.ToLower(strings.TrimSpace(cfg.AIChat.Provider))
	if cfg.AIChat.Provider == "" {
		cfg.AIChat.Provider = "mock"
	}
	cfg.AIChat.Endpoint = strings.TrimSpace(cfg.AIChat.Endpoint)
	cfg.AIChat.Model = strings.TrimSpace(cfg.AIChat.Model)
	if cfg.AIChat.RequestTimeoutMs <= 0 {
		cfg.AIChat.RequestTimeoutMs = 30000
	}
	cfg.Runtime.SmokeStoreBackend = strings.ToLower(strings.TrimSpace(cfg.Runtime.SmokeStoreBackend))
	cfg.Runtime.PostgresDSN = strings.TrimSpace(cfg.Runtime.PostgresDSN)
	if cfg.Runtime.SmokeStoreBackend == "" {
		cfg.Runtime.SmokeStoreBackend = "sqlite"
	}
	if cfg.Runtime.SchedulerIntervalMs <= 0 {
		cfg.Runtime.SchedulerIntervalMs = 100
	}
	cfg.Tracing.Exporter.Kind = strings.ToLower(strings.TrimSpace(cfg.Tracing.Exporter.Kind))
	cfg.Tracing.Exporter.Endpoint = strings.TrimSpace(cfg.Tracing.Exporter.Endpoint)
	if cfg.Tracing.Exporter.Kind == "" {
		cfg.Tracing.Exporter.Kind = "otlp"
	}
	for index := range cfg.Runtime.BotInstances {
		cfg.Runtime.BotInstances[index].ID = strings.TrimSpace(cfg.Runtime.BotInstances[index].ID)
		cfg.Runtime.BotInstances[index].Adapter = strings.ToLower(strings.TrimSpace(cfg.Runtime.BotInstances[index].Adapter))
		if cfg.Runtime.BotInstances[index].Adapter == "" {
			cfg.Runtime.BotInstances[index].Adapter = "onebot"
		}
		cfg.Runtime.BotInstances[index].Source = strings.TrimSpace(cfg.Runtime.BotInstances[index].Source)
		if cfg.Runtime.BotInstances[index].Source == "" {
			cfg.Runtime.BotInstances[index].Source = cfg.Runtime.BotInstances[index].Adapter
		}
		cfg.Runtime.BotInstances[index].Platform = strings.TrimSpace(cfg.Runtime.BotInstances[index].Platform)
		if cfg.Runtime.BotInstances[index].Platform == "" {
			cfg.Runtime.BotInstances[index].Platform = defaultRuntimeBotInstancePlatform(cfg.Runtime.BotInstances[index].Adapter)
		}
		cfg.Runtime.BotInstances[index].Path = strings.TrimSpace(cfg.Runtime.BotInstances[index].Path)
		cfg.Runtime.BotInstances[index].DemoPath = strings.TrimSpace(cfg.Runtime.BotInstances[index].DemoPath)
		if cfg.Runtime.BotInstances[index].Path == "" {
			cfg.Runtime.BotInstances[index].Path = cfg.Runtime.BotInstances[index].DemoPath
		}
		if cfg.Runtime.BotInstances[index].Path == "" {
			cfg.Runtime.BotInstances[index].Path = defaultRuntimeBotInstancePath(cfg.Runtime.BotInstances[index].Adapter)
		}
		if cfg.Runtime.BotInstances[index].Adapter == "onebot" && cfg.Runtime.BotInstances[index].DemoPath == "" {
			cfg.Runtime.BotInstances[index].DemoPath = cfg.Runtime.BotInstances[index].Path
		}
	}
}

func defaultRuntimeBotInstancePlatform(adapter string) string {
	switch strings.ToLower(strings.TrimSpace(adapter)) {
	case "webhook":
		return "webhook/http"
	default:
		return "onebot/v11"
	}
}

func defaultRuntimeBotInstancePath(adapter string) string {
	switch strings.ToLower(strings.TrimSpace(adapter)) {
	case "webhook":
		return "/webhook"
	default:
		return "/demo/onebot/message"
	}
}

func validateAIChatConfig(cfg AIChatConfig) error {
	switch cfg.Provider {
	case "mock", "openai_compat":
	default:
		return fmt.Errorf("ai_chat.provider %q is unsupported; must be \"mock\" or \"openai_compat\"", cfg.Provider)
	}
	if cfg.Provider != "openai_compat" {
		return nil
	}
	if cfg.Endpoint == "" {
		return fmt.Errorf("ai_chat.endpoint is required when ai_chat.provider=openai_compat")
	}
	parsed, err := url.ParseRequestURI(cfg.Endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("ai_chat.endpoint must be a valid absolute URL when ai_chat.provider=openai_compat")
	}
	if parsed.Scheme != "https" && !isLoopbackAIChatEndpoint(parsed) {
		return fmt.Errorf("ai_chat.endpoint must use https unless it targets localhost or loopback when ai_chat.provider=openai_compat")
	}
	if cfg.Model == "" {
		return fmt.Errorf("ai_chat.model is required when ai_chat.provider=openai_compat")
	}
	return nil
}

func isLoopbackAIChatEndpoint(parsed *url.URL) bool {
	if parsed == nil || parsed.Scheme != "http" {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateRuntimeBotInstances(instances []RuntimeBotInstance, secrets SecretsConfig) error {
	seenIDs := make(map[string]struct{}, len(instances))
	seenSources := make(map[string]struct{}, len(instances))
	seenPaths := make(map[string]struct{}, len(instances))
	hasWebhook := false
	for index, instance := range instances {
		if instance.ID == "" {
			return fmt.Errorf("runtime.bot_instances[%d].id is required", index)
		}
		switch instance.Adapter {
		case "onebot", "webhook":
		default:
			return fmt.Errorf("runtime.bot_instances[%d].adapter %q is unsupported; only \"onebot\" and \"webhook\" are currently supported", index, instance.Adapter)
		}
		if _, exists := seenIDs[instance.ID]; exists {
			return fmt.Errorf("runtime.bot_instances[%d].id %q must be unique", index, instance.ID)
		}
		seenIDs[instance.ID] = struct{}{}
		if instance.Source == "" {
			return fmt.Errorf("runtime.bot_instances[%d].source is required", index)
		}
		if _, exists := seenSources[instance.Source]; exists {
			return fmt.Errorf("runtime.bot_instances[%d].source %q must be unique", index, instance.Source)
		}
		seenSources[instance.Source] = struct{}{}
		if instance.Path == "" {
			return fmt.Errorf("runtime.bot_instances[%d].path is required", index)
		}
		if !strings.HasPrefix(instance.Path, "/") {
			return fmt.Errorf("runtime.bot_instances[%d].path %q must start with '/'", index, instance.Path)
		}
		if _, exists := seenPaths[instance.Path]; exists {
			return fmt.Errorf("runtime.bot_instances[%d].path %q must be unique", index, instance.Path)
		}
		seenPaths[instance.Path] = struct{}{}
		if instance.Path != "" && instance.DemoPath != "" && instance.Path != instance.DemoPath {
			return fmt.Errorf("runtime.bot_instances[%d].path %q must match demo_path %q when both are set", index, instance.Path, instance.DemoPath)
		}
		if instance.Adapter == "webhook" {
			hasWebhook = true
		}
	}
	if hasWebhook && strings.TrimSpace(secrets.WebhookTokenRef) == "" {
		return fmt.Errorf("%s is required when runtime.bot_instances includes adapter \"webhook\"", WebhookSecretMainPathContract().ConfigRef)
	}
	return nil
}
