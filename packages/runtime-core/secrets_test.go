package runtimecore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type canceledSecretProvider struct{}

func (canceledSecretProvider) ResolveSecret(ctx context.Context, ref string) (string, error) {
	if err := ValidateSecretRef(ref); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", errors.New("expected canceled context")
}

func TestSecretRegistryResolvesEnvSecretAndAuditsRead(t *testing.T) {
	t.Setenv("BOT_PLATFORM_WEBHOOK_TOKEN", "secret-token")
	audits := NewInMemoryAuditLog()
	registry := NewSecretRegistry(EnvSecretProvider{}, audits)
	registry.now = func() time.Time { return time.Date(2026, 4, 4, 10, 0, 0, 0, time.UTC) }

	value, err := registry.Resolve(context.Background(), "BOT_PLATFORM_WEBHOOK_TOKEN", "adapter-webhook")
	if err != nil {
		t.Fatalf("resolve secret: %v", err)
	}
	if value != "secret-token" {
		t.Fatalf("unexpected secret value %q", value)
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 || entries[0].Action != "secret.read" || entries[0].Permission != "secret:read" || !entries[0].Allowed || auditEntryReason(entries[0]) != "" {
		t.Fatalf("unexpected audit entries %+v", entries)
	}
}

func TestSecretRegistryAuditsMissingSecretLookup(t *testing.T) {
	t.Parallel()

	audits := NewInMemoryAuditLog()
	registry := NewSecretRegistry(EnvSecretProvider{}, audits)

	_, err := registry.Resolve(context.Background(), "BOT_PLATFORM_MISSING_SECRET", "adapter-webhook")
	if err == nil {
		t.Fatal("expected missing secret lookup to fail")
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 || entries[0].Allowed || entries[0].Target != "BOT_PLATFORM_MISSING_SECRET" || auditEntryReason(entries[0]) != "secret_not_found" {
		t.Fatalf("unexpected audit entries %+v", entries)
	}
}

func TestSecretRegistryAuditsCanceledSecretLookup(t *testing.T) {
	t.Parallel()

	audits := NewInMemoryAuditLog()
	registry := NewSecretRegistry(canceledSecretProvider{}, audits)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := registry.Resolve(ctx, "BOT_PLATFORM_WEBHOOK_TOKEN", "adapter-webhook")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled secret lookup, got %v", err)
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 || entries[0].Allowed || entries[0].Target != "BOT_PLATFORM_WEBHOOK_TOKEN" || auditEntryReason(entries[0]) != "secret_read_canceled" {
		t.Fatalf("unexpected audit entries %+v", entries)
	}
}

func TestSecretRegistryRejectsInvalidSecretRefBeforeLookup(t *testing.T) {
	t.Parallel()

	audits := NewInMemoryAuditLog()
	registry := NewSecretRegistry(EnvSecretProvider{}, audits)

	_, err := registry.Resolve(context.Background(), "db.password", "adapter-webhook")
	if err == nil || err.Error() != "secret ref must use BOT_PLATFORM_ prefix" {
		t.Fatalf("expected invalid secret ref rejection, got %v", err)
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 || entries[0].Allowed || entries[0].Target != "db.password" || auditEntryReason(entries[0]) != "invalid_secret_ref" {
		t.Fatalf("unexpected audit entries %+v", entries)
	}
}

func TestSecretPolicyDeclaresCurrentRuntimeBoundaries(t *testing.T) {
	t.Parallel()

	policy := SecretPolicy()
	aiContract := AIChatAPIKeySecretContract()
	operatorAuthContract := OperatorAuthTokenSecretContract()
	if policy.Provider != "env" {
		t.Fatalf("expected env provider, got %q", policy.Provider)
	}
	if policy.RefPrefix != "BOT_PLATFORM_" {
		t.Fatalf("expected BOT_PLATFORM_ prefix, got %q", policy.RefPrefix)
	}
	if !policy.RuntimeOwned {
		t.Fatal("expected runtime-owned secret refs")
	}
	if len(policy.ConfigRefs) != 3 || policy.ConfigRefs[0] != "secrets.webhook_token_ref" || policy.ConfigRefs[1] != aiContract.ConfigRef || policy.ConfigRefs[2] != operatorAuthContract.ConfigRef {
		t.Fatalf("expected webhook config ref declaration, got %+v", policy.ConfigRefs)
	}
	if len(policy.ActiveConsumers) != 3 || policy.ActiveConsumers[0] != "adapter-webhook" || policy.ActiveConsumers[1] != "apps/runtime" || policy.ActiveConsumers[2] != "runtime-core" {
		t.Fatalf("expected adapter-webhook, apps/runtime, and runtime-core as active consumers, got %+v", policy.ActiveConsumers)
	}
	if len(policy.AdditionalContracts) != 2 || policy.AdditionalContracts[0] != aiContract || policy.AdditionalContracts[1] != operatorAuthContract {
		t.Fatalf("expected ai chat secret contract declaration, got %+v", policy.AdditionalContracts)
	}
	if !containsSecretPolicyString(policy.IntegrationPoints, "adapter-webhook.NewWithSecretRef") {
		t.Fatalf("expected NewWithSecretRef integration point, got %+v", policy.IntegrationPoints)
	}
	if !containsSecretPolicyString(policy.IntegrationPoints, aiContract.Consumer) {
		t.Fatalf("expected ai chat integration point %q, got %+v", aiContract.Consumer, policy.IntegrationPoints)
	}
	if !containsSecretPolicyString(policy.IntegrationPoints, operatorAuthContract.Consumer) {
		t.Fatalf("expected operator auth integration point %q, got %+v", operatorAuthContract.Consumer, policy.IntegrationPoints)
	}
	if policy.AuditAction != "secret.read" {
		t.Fatalf("expected secret.read audit action, got %q", policy.AuditAction)
	}
	for _, expected := range []string{"invalid_secret_ref", "secret_not_found", "secret_read_canceled", "secret_read_failed"} {
		if !containsSecretPolicyString(policy.AuditOutcomes, expected) {
			t.Fatalf("expected audit outcome %q, got %+v", expected, policy.AuditOutcomes)
		}
	}
	for _, expected := range []string{"multi-provider", "secret-write-api", "rotation", "plugin-secret-injection"} {
		if !containsSecretPolicyString(policy.UnsupportedModes, expected) {
			t.Fatalf("expected unsupported mode %q, got %+v", expected, policy.UnsupportedModes)
		}
	}
	for _, expected := range []string{"GET /api/console", "go test ./packages/runtime-core ./adapters/adapter-webhook ./apps/runtime -run Secret"} {
		if !containsSecretPolicyString(policy.VerificationEndpoints, expected) {
			t.Fatalf("expected verification endpoint %q, got %+v", expected, policy.VerificationEndpoints)
		}
	}
	for _, expected := range []string{"BOT_PLATFORM_*", "EnvSecretProvider", "secrets.webhook_token_ref", aiContract.ConfigRef, operatorAuthContract.ConfigRef, "SecretRegistry.Resolve", aiContract.Consumer, operatorAuthContract.Consumer, "generic secret resolution failures"} {
		if !strings.Contains(policy.Summary+" "+strings.Join(policy.Facts, " "), expected) {
			t.Fatalf("expected declaration to mention %q, got summary=%q facts=%+v", expected, policy.Summary, policy.Facts)
		}
	}
}

func containsSecretPolicyString(items []string, target string) bool {
	return slices.Contains(items, target)
}

func TestSecretRegistryRejectsBarePrefixAndLowercaseSecretRefs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		ref    string
		expect string
	}{
		{ref: "BOT_PLATFORM_", expect: "secret ref must include a non-empty name after BOT_PLATFORM_"},
		{ref: "BOT_PLATFORM_webhook", expect: "secret ref must contain only A-Z, 0-9, and _"},
		{ref: " BOT_PLATFORM_WEBHOOK_TOKEN", expect: "secret ref must not contain leading or trailing whitespace"},
	} {
		audits := NewInMemoryAuditLog()
		registry := NewSecretRegistry(EnvSecretProvider{}, audits)
		_, err := registry.Resolve(context.Background(), tc.ref, "adapter-webhook")
		if err == nil || err.Error() != tc.expect {
			t.Fatalf("expected %q for ref %q, got %v", tc.expect, tc.ref, err)
		}
		entries := audits.AuditEntries()
		if len(entries) != 1 || entries[0].Allowed || entries[0].Target != tc.ref || auditEntryReason(entries[0]) != "invalid_secret_ref" {
			t.Fatalf("unexpected audit entries for ref %q: %+v", tc.ref, entries)
		}
	}
}

func TestSecretResolutionFailureReasonUsesStructuredSecretErrors(t *testing.T) {
	t.Parallel()

	invalidRefErr := &secretInvalidRefError{err: errors.New("secret ref must use BOT_PLATFORM_ prefix")}
	if reason := SecretResolutionFailureReason(invalidRefErr); reason != "invalid_secret_ref" {
		t.Fatalf("expected invalid secret ref reason, got %q", reason)
	}
	wrappedInvalidRefErr := errors.Join(errors.New("adapter webhook preflight failed"), invalidRefErr)
	if reason := SecretResolutionFailureReason(wrappedInvalidRefErr); reason != "invalid_secret_ref" {
		t.Fatalf("expected joined invalid ref reason, got %q", reason)
	}

	notFoundErr := &secretNotFoundError{ref: "BOT_PLATFORM_MISSING_SECRET"}
	if reason := SecretResolutionFailureReason(notFoundErr); reason != "secret_not_found" {
		t.Fatalf("expected secret not found reason, got %q", reason)
	}
	wrappedNotFoundErr := errors.Join(errors.New("adapter webhook resolve failed"), notFoundErr)
	if reason := SecretResolutionFailureReason(wrappedNotFoundErr); reason != "secret_not_found" {
		t.Fatalf("expected joined not found reason, got %q", reason)
	}

	canceledErr := context.Canceled
	if reason := SecretResolutionFailureReason(canceledErr); reason != "secret_read_canceled" {
		t.Fatalf("expected canceled secret read reason, got %q", reason)
	}
	wrappedCanceledErr := errors.Join(errors.New("adapter webhook resolve canceled"), canceledErr)
	if reason := SecretResolutionFailureReason(wrappedCanceledErr); reason != "secret_read_canceled" {
		t.Fatalf("expected joined canceled secret read reason, got %q", reason)
	}

	if reason := SecretResolutionFailureReason(errors.New("provider temporarily unavailable")); reason != "secret_read_failed" {
		t.Fatalf("expected generic secret read failure reason, got %q", reason)
	}
}
