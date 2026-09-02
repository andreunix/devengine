package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

func engineVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "v0.0.0"
	}
	// Typically, devengine is a module dependency of this binary if it was
	// built via 'go install github.com/andreunix/devengine/cmd/devengine@latest'
	// Let's check info.Deps first.
	for _, dep := range info.Deps {
		if dep.Path == "github.com/andreunix/devengine" {
			if dep.Version != "" && dep.Version != "(devel)" {
				return dep.Version
			}
		}
	}
	// Fallback to Main.Version if the binary itself is the module (e.g. go build in repo)
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "v0.0.0"
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "new":
		err = runNew(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "devengine:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `devengine — scaffold companion for devengine

Usage:
  devengine new -module <path> [-name <app>] [-profile http|worker|combined] [-dir <dir>]

Profiles:
  http      HTTP server only (cmd/server)
  worker    Background worker only (cmd/worker)
  combined  HTTP server + worker (default)

Examples:
  devengine new -module github.com/my-org/my-svc
  devengine new -module github.com/my-org/my-svc -profile worker -dir ./my-svc
`)
}

func runNew(args []string) error {
	flags := flag.NewFlagSet("new", flag.ContinueOnError)
	module := flags.String("module", "", "Go module path (required)")
	name := flags.String("name", "", "application name (defaults to last path segment)")
	profile := flags.String("profile", "combined", "runtime profile: http, worker, combined")
	dir := flags.String("dir", "", "target directory (defaults to app name)")
	if err := flags.Parse(args); err != nil {
		return err
	}

	*module = strings.TrimSpace(*module)
	if *module == "" {
		return errors.New("-module is required")
	}

	parts := strings.Split(*module, "/")
	appName := strings.TrimSpace(*name)
	if appName == "" {
		appName = parts[len(parts)-1]
	}

	targetDir := strings.TrimSpace(*dir)
	if targetDir == "" {
		targetDir = appName
	}

	p := strings.TrimSpace(*profile)
	switch p {
	case "http", "worker", "combined":
	default:
		return fmt.Errorf("unknown profile %q: must be http, worker, or combined", p)
	}

	return scaffold(*module, appName, targetDir, p)
}

type scaffoldFile struct {
	path    string
	content string
}

func scaffold(module, appName, dir, profile string) error {
	files := baseFiles(module, appName, profile)

	switch profile {
	case "http":
		files = append(files, httpFiles(module, appName)...)
	case "worker":
		files = append(files, workerFiles(module, appName)...)
		files = append(files, workerComponentFile(module, appName)...)
	case "combined":
		files = append(files, combinedMainFile(module, appName)...)
		files = append(files, workerComponentFile(module, appName)...)
	}

	for _, f := range files {
		fullPath := filepath.Join(dir, f.path)
		if _, err := os.Stat(fullPath); err == nil {
			return fmt.Errorf("refusing to overwrite %s", fullPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return err
		}
		content := strings.ReplaceAll(f.content, "{{MODULE}}", module)
		content = strings.ReplaceAll(content, "{{APP}}", appName)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("✓ created %s\n  profile: %s\n  next:    cd %s && go mod tidy && make run\n", dir, profile, dir)
	return nil
}

func baseFiles(module, appName, profile string) []scaffoldFile {
	return []scaffoldFile{
		{
			path:    "go.mod",
			content: "module {{MODULE}}\n\ngo 1.27.0\n\nrequire github.com/andreunix/devengine " + engineVersion() + "\n",
		},
		{
			path:    ".gitignore",
			content: ".env\n.DS_Store\nbin/\n*.test\n",
		},
		{
			path:    "db/migrations/1000_initial.up.sql",
			content: "-- Application migrations start at version 1000.\n-- Add your schema changes here.\n",
		},
		{
			path:    "internal/app/app.go",
			content: appFile(profile),
		},
		{
			path:    "internal/modules/example/module.go",
			content: exampleModuleFile(profile),
		},
		{
			path:    "Makefile",
			content: makefileContent(profile),
		},
		{
			path:    ".github/workflows/ci.yml",
			content: ciWorkflow(),
		},
		{
			path:    "README.md",
			content: "# {{APP}}\n\nGenerated with [devengine](https://github.com/andreunix/devengine).\n\nProfile: `" + profile + "`\n",
		},
	}
}

func httpFiles(module, appName string) []scaffoldFile {
	return []scaffoldFile{
		{
			path:    "cmd/server/main.go",
			content: serverMainFile(),
		},
	}
}

func workerFiles(module, appName string) []scaffoldFile {
	return []scaffoldFile{
		{
			path:    "cmd/worker/main.go",
			content: workerMainFile(),
		},
	}
}

func combinedMainFile(module, appName string) []scaffoldFile {
	return []scaffoldFile{
		{
			path:    "cmd/app/main.go",
			content: appMainFile(),
		},
	}
}

func workerComponentFile(module, appName string) []scaffoldFile {
	return []scaffoldFile{
		{
			path:    "internal/modules/example/worker.go",
			content: exampleWorkerFile(),
		},
	}
}

func appFile(profile string) string {
	return `package app

import (
    "log/slog"

    "github.com/andreunix/devengine/engine"
    httpmiddleware "github.com/andreunix/devengine/httpx/middleware"
    "{{MODULE}}/internal/modules/example"
)

// New constructs and returns a configured engine for the application.
// Wire your modules, workers and infrastructure here.
func New(logger *slog.Logger, opts ...engine.Option) (*engine.Engine, error) {
    base := []engine.Option{
        engine.WithName("{{APP}}"),
        engine.WithAddress(":8080"),
        engine.WithLogger(logger),
        engine.WithMiddleware(
            httpmiddleware.RequestID,
            httpmiddleware.Recover(logger),
            httpmiddleware.Logging(logger),
            httpmiddleware.SecurityHeaders,
        ),
    }
    app := engine.New(append(base, opts...)...)
    if err := app.Register(example.New()); err != nil {
        return nil, err
    }
    return app, nil
}
`
}

func exampleModuleFile(profile string) string {
	workerReg := ""
	if profile == "worker" || profile == "combined" {
		workerReg = "\n    _ = app.AddWorker(NewExampleWorker())"
	}
	return `package example

import (
    "net/http"

    "github.com/andreunix/devengine/engine"
    "github.com/andreunix/devengine/httpx"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string { return "example" }

func (*Module) Register(app *engine.Engine) error {
    app.HandleFunc("GET /api/example", func(w http.ResponseWriter, r *http.Request) {
        _ = httpx.Encode(w, http.StatusOK, map[string]string{"status": "ok"})
    })` + workerReg + `
    return nil
}
`
}

func exampleWorkerFile() string {
	return `package example

import (
    "context"
    "log/slog"
    "time"
)

type ExampleWorker struct{}

func NewExampleWorker() *ExampleWorker { return &ExampleWorker{} }

func (*ExampleWorker) Name() string { return "example-worker" }

func (*ExampleWorker) Run(ctx context.Context) error {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return nil
        case <-ticker.C:
            slog.Info("example worker tick")
        }
    }
}
`
}

func serverMainFile() string {
	return `package main

import (
    "context"
    "log/slog"
    "os"

    "github.com/andreunix/devengine/engine"
    "{{MODULE}}/internal/app"
)

func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
    srv, err := app.New(logger, engine.WithProfile(engine.ProfileHTTP))
    if err != nil {
        logger.Error("init", "error", err)
        os.Exit(1)
    }
    if err := srv.Run(context.Background()); err != nil {
        logger.Error("run", "error", err)
        os.Exit(1)
    }
}
`
}

func workerMainFile() string {
	return `package main

import (
    "context"
    "log/slog"
    "os"

    "github.com/andreunix/devengine/engine"
    "{{MODULE}}/internal/app"
)

func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
    w, err := app.New(logger, engine.WithProfile(engine.ProfileWorker))
    if err != nil {
        logger.Error("init", "error", err)
        os.Exit(1)
    }
    if err := w.Run(context.Background()); err != nil {
        logger.Error("run", "error", err)
        os.Exit(1)
    }
}
`
}

func appMainFile() string {
	return `package main

import (
    "context"
    "log/slog"
    "os"

    "github.com/andreunix/devengine/engine"
    "{{MODULE}}/internal/app"
)

func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
    appEngine, err := app.New(logger, engine.WithProfile(engine.ProfileHTTPAndWorker))
    if err != nil {
        logger.Error("init", "error", err)
        os.Exit(1)
    }
    if err := appEngine.Run(context.Background()); err != nil {
        logger.Error("run", "error", err)
        os.Exit(1)
    }
}
`
}

func makefileContent(profile string) string {
	targets := `.PHONY: test vet fmt run migrate

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

migrate:
	@echo "Run your migrate command here"
`
	switch profile {
	case "worker":
		targets += "\nrun:\n\tgo run ./cmd/worker\n"
	case "http":
		targets += "\nrun:\n\tgo run ./cmd/server\n"
	default:
		targets += "\nrun:\n\tgo run ./cmd/app\n"
	}
	return targets
}

func ciWorkflow() string {
	return `name: CI
on:
  push:
    branches: [main]
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.27'
          cache: true
      - name: gofmt
        run: |
          out=$(gofmt -l .)
          if [ -n "$out" ]; then echo "$out"; exit 1; fi
      - name: vet
        run: go vet ./...
      - name: test
        run: go test -race ./...
`
}
