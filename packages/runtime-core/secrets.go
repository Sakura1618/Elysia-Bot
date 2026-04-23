package runtimecore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type SecretProvider interface {
	ResolveSecret(context.Context, string) (string, error)
}

var secretRefPattern = regexp.MustCompile(`^[A-Z0-9_]+$`)

const (
	secretRefPrefix                  = "BOT_PLATFORM_"
	secretProviderEnv                = "env"
	secretFailureInvalidRef          = "invalid_secret_ref"
	secretFailureNotFound            = "secret_not_found"
	secretFailureReadCanceled        = "secret_read_canceled"
	secretFailureGenericReadFail     = "secret_read_failed"
	webhookTokenSecretConfigRef      = "secrets.webhook_token_ref"
	webhookTokenSecretDefaultDevRef  = "BOT_PLATFORM_WEBHOOK_TOKEN"
	webhookTokenSecretConsumer       = "adapter-webhook.NewWithSecretRef"
	aiChatAPIKeySecretConfigRef      = "secrets.ai_chat_api_key_ref"
	aiChatAPIKeySecretConsumer       = "apps/runtime.buildAIProvider"
	operatorAuthTokenSecretConfigRef = "operator_auth.tokens[].token_ref"
	operatorAuthTokenSecretConsumer  = "runtimecore.NewOperatorBearerIdentityResolver"
)

type SecretContract struct {
	ConfigRef     string `json:"configRef"`
	DefaultDevRef string `json:"defaultDevRef"`
	Consumer      string `json:"consumer"`
	PathNote      string `json:"pathNote"`
}

type SecretPolicyDeclaration struct {
	Provider              string           `json:"provider"`
	RefPrefix             string           `json:"refPrefix"`
	RefFormat             string           `json:"refFormat"`
	RuntimeOwned          bool             `json:"runtimeOwned"`
	MainPathContract      SecretContract   `json:"mainPathContract"`
	AdditionalContracts   []SecretContract `json:"additionalContracts,omitempty"`
	ConfigRefs            []string         `json:"configRefs,omitempty"`
	ActiveConsumers       []string         `json:"activeConsumers,omitempty"`
	IntegrationPoints     []string         `json:"integrationPoints,omitempty"`
	AuditAction           string           `json:"auditAction,omitempty"`
	AuditOutcomes         []string         `json:"auditOutcomes,omitempty"`
	UnsupportedModes      []string         `json:"unsupportedModes,omitempty"`
	VerificationEndpoints []string         `json:"verificationEndpoints,omitempty"`
	Facts                 []string         `json:"facts,omitempty"`
	Summary               string           `json:"summary,omitempty"`
}

func WebhookSecretMainPathContract() SecretContract {
	pathNote := webhookTokenSecretConfigRef + " -> " + webhookTokenSecretConsumer + " -> SecretRegistry.Resolve"
	return SecretContract{
		ConfigRef:     webhookTokenSecretConfigRef,
		DefaultDevRef: webhookTokenSecretDefaultDevRef,
		Consumer:      webhookTokenSecretConsumer,
		PathNote:      pathNote,
	}
}

func AIChatAPIKeySecretConfigRef() string {
	return aiChatAPIKeySecretConfigRef
}

func AIChatAPIKeySecretContract() SecretContract {
	pathNote := aiChatAPIKeySecretConfigRef + " -> " + aiChatAPIKeySecretConsumer + " -> SecretRegistry.Resolve"
	return SecretContract{
		ConfigRef:     aiChatAPIKeySecretConfigRef,
		DefaultDevRef: "",
		Consumer:      aiChatAPIKeySecretConsumer,
		PathNote:      pathNote,
	}
}

func OperatorAuthTokenSecretContract() SecretContract {
	pathNote := operatorAuthTokenSecretConfigRef + " -> " + operatorAuthTokenSecretConsumer + " -> SecretRegistry.Resolve"
	return SecretContract{
		ConfigRef:     operatorAuthTokenSecretConfigRef,
		DefaultDevRef: "",
		Consumer:      operatorAuthTokenSecretConsumer,
		PathNote:      pathNote,
	}
}

func SecretPolicy() SecretPolicyDeclaration {
	contract := WebhookSecretMainPathContract()
	aiChatContract := AIChatAPIKeySecretContract()
	operatorAuthContract := OperatorAuthTokenSecretContract()
	declaration := SecretPolicyDeclaration{
		Provider:              secretProviderEnv,
		RefPrefix:             secretRefPrefix,
		RefFormat:             "BOT_PLATFORM_[A-Z0-9_]+",
		RuntimeOwned:          true,
		MainPathContract:      contract,
		AdditionalContracts:   []SecretContract{aiChatContract, operatorAuthContract},
		ConfigRefs:            []string{contract.ConfigRef, aiChatContract.ConfigRef, operatorAuthContract.ConfigRef},
		ActiveConsumers:       []string{"adapter-webhook", "apps/runtime", "runtime-core"},
		IntegrationPoints:     []string{contract.ConfigRef, contract.Consumer, aiChatContract.ConfigRef, aiChatContract.Consumer, operatorAuthContract.ConfigRef, operatorAuthContract.Consumer},
		AuditAction:           "secret.read",
		AuditOutcomes:         []string{"success", secretFailureInvalidRef, secretFailureNotFound, secretFailureReadCanceled, secretFailureGenericReadFail},
		UnsupportedModes:      []string{"multi-provider", "secret-write-api", "rotation", "versioning", "scope-based-secret-authorization", "console-secret-management", "plugin-secret-injection"},
		VerificationEndpoints: []string{"GET /api/console", "go test ./packages/runtime-core ./adapters/adapter-webhook ./apps/runtime -run Secret"},
		Facts: []string{
			"secret refs must be runtime-owned BOT_PLATFORM_* names and may contain only A-Z, 0-9, and _",
			"the only provider wired today is environment-variable lookup via EnvSecretProvider",
			"real config-to-read paths wired today are " + contract.PathNote + ", " + aiChatContract.PathNote + ", and " + operatorAuthContract.PathNote,
			"every registry resolve records secret.read audit with success or failure reason while webhook clients still receive only generic secret resolution failures",
		},
	}
	declaration.Summary = "provider=env; runtime-owned BOT_PLATFORM_* refs only; current active read paths are " + contract.ConfigRef + " -> " + contract.Consumer + ", " + aiChatContract.ConfigRef + " -> " + aiChatContract.Consumer + ", and " + operatorAuthContract.ConfigRef + " -> " + operatorAuthContract.Consumer + "; every resolve records secret.read audit; no multi-provider or secret write API"
	return declaration
}

func ValidateSecretRef(ref string) error {
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("secret ref is required")
	}
	if ref != strings.TrimSpace(ref) {
		return fmt.Errorf("secret ref must not contain leading or trailing whitespace")
	}
	if !strings.HasPrefix(ref, secretRefPrefix) {
		return fmt.Errorf("secret ref must use BOT_PLATFORM_ prefix")
	}
	if ref == secretRefPrefix {
		return fmt.Errorf("secret ref must include a non-empty name after BOT_PLATFORM_")
	}
	if !secretRefPattern.MatchString(ref) {
		return fmt.Errorf("secret ref must contain only A-Z, 0-9, and _")
	}
	return nil
}

type EnvSecretProvider struct{}

type secretInvalidRefError struct {
	err error
}

func (e *secretInvalidRefError) Error() string {
	if e == nil || e.err == nil {
		return "invalid secret ref"
	}
	return e.err.Error()
}

func (e *secretInvalidRefError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type secretNotFoundError struct {
	ref string
}

func (e *secretNotFoundError) Error() string {
	if e == nil {
		return "secret not found in environment"
	}
	return fmt.Sprintf("secret %q not found in environment", e.ref)
}

func (EnvSecretProvider) ResolveSecret(_ context.Context, ref string) (string, error) {
	if err := ValidateSecretRef(ref); err != nil {
		return "", &secretInvalidRefError{err: err}
	}
	value := os.Getenv(ref)
	if value == "" {
		return "", &secretNotFoundError{ref: ref}
	}
	return value, nil
}

type SecretRegistry struct {
	provider SecretProvider
	audits   AuditRecorder
	now      func() time.Time
}

func NewSecretRegistry(provider SecretProvider, audits AuditRecorder) *SecretRegistry {
	return &SecretRegistry{provider: provider, audits: audits, now: time.Now().UTC}
}

func (r *SecretRegistry) Resolve(ctx context.Context, ref string, consumer string) (string, error) {
	if r == nil || r.provider == nil {
		return "", fmt.Errorf("secret provider is required")
	}
	value, err := r.provider.ResolveSecret(ctx, ref)
	r.recordAudit(consumer, ref, err == nil, SecretResolutionFailureReason(err))
	if err != nil {
		return "", err
	}
	return value, nil
}

func (r *SecretRegistry) recordAudit(actor, target string, allowed bool, reason string) {
	if r == nil || r.audits == nil {
		return
	}
	if actor == "" {
		actor = "unknown"
	}
	entry := pluginsdk.AuditEntry{
		Actor:      actor,
		Permission: "secret:read",
		Action:     "secret.read",
		Target:     target,
		Allowed:    allowed,
		OccurredAt: r.now().Format(time.RFC3339),
	}
	setAuditEntryReason(&entry, reason)
	_ = r.audits.RecordAudit(entry)
}

func SecretResolutionFailureReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return secretFailureReadCanceled
	}
	var notFoundErr *secretNotFoundError
	if errors.As(err, &notFoundErr) {
		return secretFailureNotFound
	}
	var invalidRefErr *secretInvalidRefError
	if errors.As(err, &invalidRefErr) {
		return secretFailureInvalidRef
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "context canceled"):
		return secretFailureReadCanceled
	case strings.Contains(message, "not found in environment"):
		return secretFailureNotFound
	case strings.Contains(message, "secret ref"):
		return secretFailureInvalidRef
	default:
		return secretFailureGenericReadFail
	}
}
