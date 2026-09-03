package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/andreunix/devengine/health"
	"github.com/andreunix/devengine/telemetry"
)

type serverTimeouts struct {
	readHeader time.Duration
	read       time.Duration
	write      time.Duration
	idle       time.Duration
}

type runningWorker struct {
	name string
	done chan struct{}
}

// Profile determines which subsystems the engine activates on Run.
type Profile int

const (
	// ProfileHTTPAndWorker (default) starts both the HTTP server and background workers.
	ProfileHTTPAndWorker Profile = iota
	// ProfileHTTP starts only the HTTP server. Workers are not started.
	ProfileHTTP
	// ProfileWorker starts only background workers. No HTTP server is bound.
	ProfileWorker
)

func (p Profile) String() string {
	switch p {
	case ProfileHTTP:
		return "http-only"
	case ProfileWorker:
		return "worker-only"
	default:
		return "http+worker"
	}
}

// Engine owns process lifecycle and infrastructure surfaces. It deliberately
// does not own domain repositories, authorization rules or business models.
type Engine struct {
	name                  string
	address               string
	profile               Profile
	version               string
	environment           string
	logger                *slog.Logger
	mux                   *http.ServeMux
	readiness             *health.Registry
	modules               []Module
	workers               []Worker
	middleware            []func(http.Handler) http.Handler
	shutdownTimeout       time.Duration
	workerShutdownTimeout time.Duration
	serverTimeouts        serverTimeouts

	tracer telemetry.Tracer
	meter  telemetry.Meter

	mu          sync.Mutex
	registered  map[string]struct{}
	workerNames map[string]struct{}
	started     bool
}

func New(options ...Option) *Engine {
	e := &Engine{
		name:                  "devengine-app",
		address:               ":8080",
		logger:                slog.New(slog.NewJSONHandler(os.Stdout, nil)),
		mux:                   http.NewServeMux(),
		readiness:             health.NewRegistry(),
		shutdownTimeout:       10 * time.Second,
		workerShutdownTimeout: 30 * time.Second,
		serverTimeouts: serverTimeouts{
			readHeader: 5 * time.Second,
			read:       30 * time.Second,
			write:      30 * time.Second,
			idle:       60 * time.Second,
		},
		tracer:      telemetry.NoopTracer,
		meter:       telemetry.NoopMeter,
		registered:  make(map[string]struct{}),
		workerNames: make(map[string]struct{}),
	}
	for _, option := range options {
		option(e)
	}
	return e
}

func (e *Engine) Name() string                { return e.name }
func (e *Engine) Logger() *slog.Logger        { return e.logger }
func (e *Engine) Router() *http.ServeMux      { return e.mux }
func (e *Engine) Readiness() *health.Registry { return e.readiness }
func (e *Engine) Tracer() telemetry.Tracer    { return e.tracer }
func (e *Engine) Meter() telemetry.Meter      { return e.meter }

func (e *Engine) Handle(pattern string, handler http.Handler) {
	e.mux.Handle(pattern, handler)
}

func (e *Engine) HandleFunc(pattern string, handler http.HandlerFunc) {
	e.mux.HandleFunc(pattern, handler)
}

func (e *Engine) Register(modules ...Module) error {
	for _, module := range modules {
		if module == nil {
			return errors.New("engine: nil module")
		}
		name := module.Name()
		if name == "" {
			return errors.New("engine: module name is required")
		}

		e.mu.Lock()
		if e.started {
			e.mu.Unlock()
			return errors.New("engine: cannot register modules after start")
		}
		if _, exists := e.registered[name]; exists {
			e.mu.Unlock()
			return fmt.Errorf("engine: module %q already registered", name)
		}
		// Reserve the name before calling the module so recursive registration
		// cannot create a duplicate. Do not hold the mutex while the module
		// registers workers or health checks.
		e.registered[name] = struct{}{}
		e.mu.Unlock()

		if err := module.Register(e); err != nil {
			e.mu.Lock()
			delete(e.registered, name)
			e.mu.Unlock()
			return fmt.Errorf("register module %q: %w", name, err)
		}

		e.mu.Lock()
		e.modules = append(e.modules, module)
		e.mu.Unlock()
	}
	return nil
}

func (e *Engine) AddWorker(workers ...Worker) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("engine: cannot register workers after start")
	}
	for _, worker := range workers {
		if worker == nil {
			return errors.New("engine: nil worker")
		}
		name := worker.Name()
		if name == "" {
			return errors.New("engine: worker name is required")
		}
		if _, exists := e.workerNames[name]; exists {
			return fmt.Errorf("engine: worker %q already registered", name)
		}
		e.workerNames[name] = struct{}{}
		e.workers = append(e.workers, worker)
	}
	return nil
}

// Run starts the subsystems determined by the engine Profile and returns on
// context cancellation, SIGINT/SIGTERM, worker failure, or HTTP server failure.
//
//   - ProfileHTTPAndWorker (default): starts HTTP server and all workers.
//   - ProfileHTTP: starts only the HTTP server; workers are not started.
//   - ProfileWorker: starts only workers; no HTTP server is bound.
func (e *Engine) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("engine: already started")
	}
	e.started = true
	e.mu.Unlock()

	// Build a logger enriched with base attributes so every log record
	// automatically carries service, version and environment.
	baseAttrs := []any{"service", e.name}
	if e.version != "" {
		baseAttrs = append(baseAttrs, "version", e.version)
	}
	if e.environment != "" {
		baseAttrs = append(baseAttrs, "environment", e.environment)
	}
	log := e.logger.With(baseAttrs...)

	log.Info("engine starting", "profile", e.profile.String())

	runCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, len(e.workers)+1)
	runningWorkers := make([]runningWorker, 0, len(e.workers))

	// Start workers unless HTTP-only.
	if e.profile != ProfileHTTP {
		for _, worker := range e.workers {
			worker := worker
			state := runningWorker{name: worker.Name(), done: make(chan struct{})}
			runningWorkers = append(runningWorkers, state)
			go func() {
				defer close(state.done)
				log.Info("worker started", "worker", worker.Name())
				err := worker.Run(runCtx)
				if err == nil && runCtx.Err() == nil {
					err = errors.New("exited unexpectedly before shutdown")
				}
				if err != nil && !errors.Is(err, context.Canceled) {
					select {
					case errCh <- fmt.Errorf("worker %q: %w", worker.Name(), err):
					default:
					}
					cancel()
				}
			}()
		}
	}

	// Start HTTP server unless Worker-only.
	var server *http.Server
	if e.profile != ProfileWorker {
		e.installInfrastructureRoutes()

		handler := http.Handler(e.mux)
		for i := len(e.middleware) - 1; i >= 0; i-- {
			if e.middleware[i] != nil {
				handler = e.middleware[i](handler)
			}
		}
		server = &http.Server{
			Addr:              e.address,
			Handler:           handler,
			ReadHeaderTimeout: e.serverTimeouts.readHeader,
			ReadTimeout:       e.serverTimeouts.read,
			WriteTimeout:      e.serverTimeouts.write,
			IdleTimeout:       e.serverTimeouts.idle,
		}
		go func() {
			log.Info("http server started", "address", e.address)
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				select {
				case errCh <- fmt.Errorf("http server: %w", err):
				default:
				}
				cancel()
			}
		}()
	}

	var runErr error
	select {
	case <-runCtx.Done():
		runErr = context.Cause(runCtx)
		if errors.Is(runErr, context.Canceled) {
			runErr = nil
		}
	case runErr = <-errCh:
	}

	if server != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), e.shutdownTimeout)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = err
		}
	}
	cancel()
	if err := waitForWorkers(runningWorkers, e.workerShutdownTimeout); err != nil {
		log.Error("worker shutdown timeout", "error", err, "timeout", e.workerShutdownTimeout)
		runErr = errors.Join(runErr, err)
	}

	if runErr == nil {
		select {
		case runErr = <-errCh:
		default:
		}
	}
	return runErr
}

func waitForWorkers(workers []runningWorker, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for _, worker := range workers {
		select {
		case <-worker.done:
		case <-timer.C:
			names := unfinishedWorkerNames(workers)
			if len(names) == 0 {
				return nil
			}
			return fmt.Errorf("engine: worker shutdown exceeded %s: %v", timeout, names)
		}
	}
	return nil
}

func unfinishedWorkerNames(workers []runningWorker) []string {
	names := make([]string, 0, len(workers))
	for _, worker := range workers {
		select {
		case <-worker.done:
		default:
			names = append(names, worker.name)
		}
	}
	return names
}

func (e *Engine) installInfrastructureRoutes() {
	e.mux.HandleFunc("GET /healthz", health.LiveHandler(e.name))
	e.mux.Handle("GET /readyz", e.readiness.Handler(e.name))
}
