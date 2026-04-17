package sessions

import (
	"context"
	"errors"
)

// ErrUserNotFound is returned by UserStore.GetUser when no profile exists for
// the given user ID. Callers can check with errors.Is.
var ErrUserNotFound = errors.New("user not found")

type contextKey string

const userIDKey contextKey = "userID"

// WithUserID returns a copy of ctx carrying the given userID.
// The router injects this before dispatching to agents so that tools can
// retrieve it via UserIDFromContext without needing it threaded through every
// function signature.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// UserIDFromContext extracts the user ID injected by WithUserID.
// Returns "" if no user ID is present.
func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}
