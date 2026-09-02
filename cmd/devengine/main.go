package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
  devengine new -module github.com/your-org/my-app [-dir ./my-app]
`)
}

func runNew(args []string) error {
	flags := flag.NewFlagSet("new", flag.ContinueOnError)
	module := flags.String("module", "", "Go module path")
	dir := flags.String("dir", "", "target directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	*module = strings.TrimSpace(*module)
	if *module == "" {
		return errors.New("-module is required")
	}
	if *dir == "" {
		parts := strings.Split(*module, "/")
		*dir = parts[len(parts)-1]
	}
	return scaffold(*module, *dir)
}

func scaffold(module, dir string) error {
	files := map[string]string{
		"go.mod":                             "module " + module + "\n\ngo 1.27.0\n\nrequire github.com/andreunix/devengine v0.0.0\n",
		".gitignore":                         ".env\n.DS_Store\nbin/\n",
		"cmd/server/main.go":                 starterMain,
		"internal/modules/example/module.go": starterModule,
		"db/migrations/1000_initial.up.sql":  "-- Application migrations start at version 1000.\n",
		"README.md":                          "# " + filepath.Base(dir) + "\n\nGenerated with devengine.\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("refusing to overwrite %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		content = strings.ReplaceAll(content, "{{MODULE}}", module)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("created %s\nnext: cd %s && go mod tidy\n", dir, dir)
	return nil
}

const starterMain = `package main

import (
    "context"
    "log/slog"
    "os"

    "github.com/andreunix/devengine/engine"
    httpmiddleware "github.com/andreunix/devengine/httpx/middleware"
    "{{MODULE}}/internal/modules/example"
)

func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
    app := engine.New(
        engine.WithName("app"),
        engine.WithAddress(":8080"),
        engine.WithLogger(logger),
        engine.WithMiddleware(
            httpmiddleware.RequestID,
            httpmiddleware.Recover(logger),
            httpmiddleware.Logging(logger),
            httpmiddleware.SecurityHeaders,
        ),
    )
    if err := app.Register(example.New()); err != nil {
        logger.Error("register modules", "error", err)
        os.Exit(1)
    }
    if err := app.Run(context.Background()); err != nil {
        logger.Error("run application", "error", err)
        os.Exit(1)
    }
}
`

const starterModule = `package example

import (
    "net/http"
    "github.com/andreunix/devengine/engine"
)

type Module struct{}
func New() *Module { return &Module{} }
func (*Module) Name() string { return "example" }
func (*Module) Register(app *engine.Engine) error {
    app.HandleFunc("GET /api/example", func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte("ok\n"))
    })
    return nil
}
`
