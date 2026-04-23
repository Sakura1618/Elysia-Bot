package runtimecore

import (
	"context"
	"testing"
)

func TestOperatorBearerIdentityResolverResolvesConfiguredTokenToRequestIdentityContext(t *testing.T) {
	t.Setenv("BOT_PLATFORM_OPERATOR_TOKEN", "opaque-operator-token")
	registry := NewSecretRegistry(EnvSecretProvider{}, NewInMemoryAuditLog())
	resolver, err := NewOperatorBearerIdentityResolver(context.Background(), registry, &OperatorAuthConfig{Tokens: []OperatorAuthTokenConfig{{
		ID:       "console-main",
		ActorID:  "admin-user",
		TokenRef: "BOT_PLATFORM_OPERATOR_TOKEN",
	}}})
	if err != nil {
		t.Fatalf("build resolver: %v", err)
	}

	identity, ok := resolver.ResolveAuthorizationHeader("Bearer opaque-operator-token")
	if !ok {
		t.Fatal("expected authorization header to resolve")
	}
	if identity.ActorID != "admin-user" || identity.TokenID != "console-main" || identity.AuthMethod != RequestIdentityAuthMethodBearer || identity.SessionID != "session-operator-bearer-admin-user" {
		t.Fatalf("unexpected request identity %+v", identity)
	}
}

func TestOperatorBearerIdentityResolverFailsClosedWhenSecretIsMissing(t *testing.T) {
	t.Parallel()

	registry := NewSecretRegistry(EnvSecretProvider{}, NewInMemoryAuditLog())
	_, err := NewOperatorBearerIdentityResolver(context.Background(), registry, &OperatorAuthConfig{Tokens: []OperatorAuthTokenConfig{{
		ID:       "console-main",
		ActorID:  "admin-user",
		TokenRef: "BOT_PLATFORM_OPERATOR_TOKEN_MISSING",
	}}})
	if err == nil || err.Error() != `resolve operator_auth.tokens[0].token_ref for token "console-main": secret "BOT_PLATFORM_OPERATOR_TOKEN_MISSING" not found in environment` {
		t.Fatalf("expected missing secret failure, got %v", err)
	}
}

func TestOperatorBearerIdentityResolverRejectsDuplicateResolvedOpaqueTokens(t *testing.T) {
	t.Setenv("BOT_PLATFORM_OPERATOR_TOKEN_A", "same-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_TOKEN_B", "same-token")
	registry := NewSecretRegistry(EnvSecretProvider{}, NewInMemoryAuditLog())
	_, err := NewOperatorBearerIdentityResolver(context.Background(), registry, &OperatorAuthConfig{Tokens: []OperatorAuthTokenConfig{
		{ID: "console-a", ActorID: "admin-user", TokenRef: "BOT_PLATFORM_OPERATOR_TOKEN_A"},
		{ID: "console-b", ActorID: "admin-user", TokenRef: "BOT_PLATFORM_OPERATOR_TOKEN_B"},
	}})
	if err == nil || err.Error() != `operator_auth.tokens[1] bearer token duplicates configured token "console-a"` {
		t.Fatalf("expected duplicate opaque token rejection, got %v", err)
	}
}

func TestBearerTokenFromAuthorizationHeaderRejectsWhitespacePayloads(t *testing.T) {
	t.Parallel()

	for _, header := range []string{"", "Basic abc", "Bearer ", "Bearer has whitespace", "Bearer token\nnext"} {
		if token, ok := BearerTokenFromAuthorizationHeader(header); ok || token != "" {
			t.Fatalf("expected header %q to be rejected, got token=%q ok=%v", header, token, ok)
		}
	}
}

func TestRequestIdentityContextRoundTripsThroughContext(t *testing.T) {
	t.Parallel()

	ctx := WithRequestIdentityContext(context.Background(), RequestIdentityContext{
		ActorID:    " admin-user ",
		TokenID:    " console-main ",
		AuthMethod: " Bearer ",
		SessionID:  " session-operator-bearer-admin-user ",
	})
	identity := RequestIdentityContextFromContext(ctx)
	if identity.ActorID != "admin-user" || identity.TokenID != "console-main" || identity.AuthMethod != "bearer" || identity.SessionID != "session-operator-bearer-admin-user" {
		t.Fatalf("unexpected context identity %+v", identity)
	}
	if empty := RequestIdentityContextFromContext(context.Background()); empty != (RequestIdentityContext{}) {
		t.Fatalf("expected empty context identity from bare context, got %+v", empty)
	}
}
