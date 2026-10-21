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

// Not for a user JWT sent to config-service: any Authorization header is treated as an M2M API key. User JWTs go out as X-Token only.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey, token)
}

func TokenFromContext(ctx context.Context) string {
	s, _ := ctx.Value(tokenKey).(string)
	return s
}

func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey, role)
}

func RoleFromContext(ctx context.Context) string {
	s, _ := ctx.Value(roleKey).(string)
	return s
}

// No-op until role checks are enforced on the tool path.
func CheckPermission(ctx context.Context, toolName, role string) error {
	return nil
}

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
