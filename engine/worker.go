package engine

import "context"

// Worker is a long-running background component owned by the application lifecycle.
// Run must return when ctx is cancelled.
type Worker interface {
	Name() string
	Run(context.Context) error
}

// WorkerFunc adapts a function into a Worker.
type WorkerFunc struct {
	WorkerName string
	RunFunc    func(context.Context) error
}

func (w WorkerFunc) Name() string { return w.WorkerName }

func (w WorkerFunc) Run(ctx context.Context) error {
	if w.RunFunc == nil {
		<-ctx.Done()
		return nil
	}
	return w.RunFunc(ctx)
}
