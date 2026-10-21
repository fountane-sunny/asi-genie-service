package genie

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

var ErrUnauthenticated = errors.New("unauthenticated")

var ErrForbidden = errors.New("forbidden")

type Caller struct {
	UserID string
	Name   string
	Email  string
}

func (e *Engine) Authorize(ctx context.Context, token string, propertyID int) (Caller, error) {
	if strings.TrimSpace(token) == "" {
		return Caller{}, ErrUnauthenticated
	}
	if propertyID <= 0 {
		return Caller{}, ErrPropertyRequired
	}
	rc := &reqCtx{e: e, ctx: ctx, token: token, pid: propertyID, resp: &Response{}}
	u, err := rc.loadCaller()
	if err != nil {
		if isAuthErr(err) {
			if strings.Contains(err.Error(), "HTTP 403") {
				return Caller{}, ErrForbidden
			}
			return Caller{}, ErrUnauthenticated
		}
		return Caller{}, err
	}
	if !u.allows(propertyID) {
		return Caller{}, ErrForbidden
	}
	return Caller{UserID: u.id, Name: u.name, Email: u.email}, nil
}

func propertyIDsOf(v any) []int {
	seen := map[int]bool{}
	var out []int
	var walk func(any, int)
	walk = func(v any, depth int) {
		if v == nil || depth > 6 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if strings.EqualFold(k, "propertyId") || strings.EqualFold(k, "propertyID") {
					if id, ok := positiveInt(val); ok && !seen[id] {
						seen[id] = true
						out = append(out, id)
					}
				}
				walk(val, depth+1)
			}
		case []any:
			for _, e := range x {
				walk(e, depth+1)
			}
		}
	}
	walk(v, 0)
	return out
}

func positiveInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n > 0 && n == float64(int(n)) {
			return int(n), true
		}
	case int:
		if n > 0 {
			return n, true
		}
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err == nil && i > 0 {
			return i, true
		}
	}
	return 0, false
}
