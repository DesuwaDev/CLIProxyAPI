package logging

import "context"

type observationIDKey struct{}

// WithObservationID carries an opaque correlation ID for optional observers.
// It does not alter cancellation, request IDs, routing, or protocol behavior.
func WithObservationID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, observationIDKey{}, id)
}

func ObservationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(observationIDKey{}).(string)
	return id
}
