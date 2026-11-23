package server

import "context"

type HealthChecker interface {
	Healthy(ctx context.Context) bool
}
