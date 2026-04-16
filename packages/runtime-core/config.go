package runtimecore

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Runtime RuntimeConfig `yaml:"runtime"`
	Secrets SecretsConfig `yaml:"secrets,omitempty"`
	RBAC    *RBACConfig   `yaml:"rbac,omitempty"`
}

type RuntimeConfig struct {
	Environment         string `yaml:"environment"`
	LogLevel            string `yaml:"log_level"`
	HTTPPort            int    `yaml:"http_port"`
	SQLitePath          string `yaml:"sqlite_path,omitempty"`
	SmokeStoreBackend   string `yaml:"smoke_store_backend,omitempty"`
	PostgresDSN         string `yaml:"postgres_dsn,omitempty"`
	SchedulerIntervalMs int    `yaml:"scheduler_interval_ms,omitempty"`
}

type SecretsConfig struct {
	WebhookTokenRef string `yaml:"webhook_token_ref,omitempty"`
}

type RBACConfig struct {
	ActorRoles            map[string][]string                      `yaml:"actor_roles,omitempty"`
	Policies              map[string]pluginsdk.AuthorizationPolicy `yaml:"policies,omitempty"`
	ConsoleReadPermission string                                   `yaml:"console_read_permission,omitempty"`
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
	if err := cfg.RBAC.Validate(); err != nil {
		return Config{}, err
	}

	applyEnvOverride(&cfg)
	applyRuntimeDefaults(&cfg)
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
}

func applyRuntimeDefaults(cfg *Config) {
	if cfg.Runtime.SQLitePath == "" {
		cfg.Runtime.SQLitePath = filepath.Join("data", "dev", "runtime.sqlite")
	}
	cfg.Runtime.SmokeStoreBackend = strings.ToLower(strings.TrimSpace(cfg.Runtime.SmokeStoreBackend))
	cfg.Runtime.PostgresDSN = strings.TrimSpace(cfg.Runtime.PostgresDSN)
	if cfg.Runtime.SmokeStoreBackend == "" {
		cfg.Runtime.SmokeStoreBackend = "sqlite"
	}
	if cfg.Runtime.SchedulerIntervalMs <= 0 {
		cfg.Runtime.SchedulerIntervalMs = 100
	}
}
