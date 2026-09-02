package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestScaffoldProfiles(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Dir(filepath.Dir(wd)) // cmd/devengine -> cmd -> repo root

	profiles := []string{"http", "worker", "combined"}
	for _, p := range profiles {
		t.Run(p, func(t *testing.T) {
			dir := t.TempDir()
			appName := "testapp"
			err := scaffold("github.com/example/testapp", appName, dir, p)
			if err != nil {
				t.Fatalf("scaffold failed: %v", err)
			}

			// Point the go.mod to the local repo so go mod tidy and build works
			// without needing internet access or published tags.
			modFile := filepath.Join(dir, "go.mod")
			modContent, err := os.ReadFile(modFile)
			if err != nil {
				t.Fatal(err)
			}
			replaceDirective := "\nreplace github.com/andreunix/devengine => " + repoRoot + "\n"
			err = os.WriteFile(modFile, append(modContent, []byte(replaceDirective)...), 0o644)
			if err != nil {
				t.Fatal(err)
			}

			// Run go mod tidy
			cmd := exec.Command("go", "mod", "tidy")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go mod tidy failed: %v\n%s", err, string(out))
			}

			// Run go build
			cmd = exec.Command("go", "build", "./...")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go build failed: %v\n%s", err, string(out))
			}
		})
	}
}
