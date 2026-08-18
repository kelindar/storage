package storage

import "context"

const (
	UnknownActor = "unknown"
	SystemActor  = "system"
)

type actorKey struct{}

// WithActor returns a context carrying the audit identity for storage mutations.
func WithActor(ctx context.Context, actor string) context.Context {
	if actor == "" {
		actor = UnknownActor
	}
	return context.WithValue(ctx, actorKey{}, actor)
}

// Actor returns the audit identity carried by ctx, or UnknownActor.
func Actor(ctx context.Context) string {
	if ctx != nil {
		if actor, ok := ctx.Value(actorKey{}).(string); ok {
			return actor
		}
	}
	return UnknownActor
}
