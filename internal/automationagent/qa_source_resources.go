package automationagent

import "context"

type qaSourceResourceBoundaryKey struct{}

func withQASourceResourceBoundary(ctx context.Context) context.Context {
	return context.WithValue(ctx, qaSourceResourceBoundaryKey{}, true)
}
