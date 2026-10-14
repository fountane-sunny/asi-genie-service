// Package toolregistry holds shared hooks and context helpers used by the
// generated PMS tools in toolregistry/generated.
package toolregistry

import (
	"context"
	"encoding/json"
)

type ctxKey int

const (
	tokenKey ctxKey = iota
	roleKey
)

// WithToken attaches the caller's bearer token (Authorization: Bearer on outbound ASI calls).
// Do not use for user passthrough to config-service: it treats any Authorization header as M2M
// API key. Genie execTool forwards user JWT via X-Token only.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey, token)
}

// TokenFromContext returns the bearer token, if any.
func TokenFromContext(ctx context.Context) string {
	s, _ := ctx.Value(tokenKey).(string)
	return s
}

// WithRole attaches the caller's role for permission checks.
func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey, role)
}

// RoleFromContext returns the caller's role, if any.
func RoleFromContext(ctx context.Context) string {
	s, _ := ctx.Value(roleKey).(string)
	return s
}

// CheckPermission is a placeholder RBAC hook run before every tool call.
// TODO: enforce the Role Matrix / RBAC reference and Phase 1 read-only rules.
func CheckPermission(ctx context.Context, toolName, role string) error {
	return nil
}

// MapToCard is a placeholder hook that turns a raw service response into a
// Genie response card. TODO: per-tool card mapping.
func MapToCard(toolName string, resp []byte) (any, error) {
	var v any
	if len(resp) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(resp, &v); err != nil {
		return string(resp), nil
	}
	return v, nil
}
