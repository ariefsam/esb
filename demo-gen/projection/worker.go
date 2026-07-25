package projection

import "context"

// Worker is the interface implemented by all projection workers.
// Each worker runs as a goroutine in main.go.
type Worker interface {
	Run(ctx context.Context)
}
