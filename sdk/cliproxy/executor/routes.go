package executor

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/routes"
)

// WithRouteMetadata adds the HTTP endpoint route to an execution metadata map.
// Without a route it preserves the original map, including a nil map.
func WithRouteMetadata(ctx context.Context, metadata map[string]any) map[string]any {
	if route := routes.FromContext(ctx); route != nil {
		if metadata == nil {
			metadata = make(map[string]any, 1)
		}
		metadata[RouteMetadataKey] = route.Name()
	}
	return metadata
}
