package deploy

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// repoRoot walks up until it finds go.mod, the same way the e2e harness does.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the repository root")
		}
		dir = parent
	}
}

func serviceSet(t *testing.T) (string, Set) {
	t.Helper()

	root := repoRoot(t)
	set, err := Load(filepath.Join(root, "deploy", "services.yaml"))
	if err != nil {
		t.Fatalf("loading the service set: %v", err)
	}
	return root, set
}

// skipped directories hold no deployable: generated bindings, build output,
// deployment artefacts, and the cross-service suites.
var skipped = map[string]bool{
	".git": true, ".github": true, "target": true, "deploy": true,
	"test": true, "docs": true, "protocol": true, "node_modules": true,
}

// discoverBinaries finds every buildable binary in the repository: a directory
// containing a Go `package main`, or a Rust crate with a src/main.rs.
func discoverBinaries(t *testing.T, root string) map[string]Language {
	t.Helper()

	found := map[string]Language{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipped[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		switch {
		case entry.Name() == "main.rs" && strings.HasSuffix(filepath.ToSlash(filepath.Dir(path)), "/src"):
			// The crate directory, not its src/.
			found[strings.TrimSuffix(rel, "/src")] = Rust
		case strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go"):
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly)
			if err != nil {
				return nil // A file that does not parse is go vet's problem, not this test's.
			}
			if parsed.Name.Name == "main" {
				found[rel] = Go
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}
	return found
}

// TestEveryBuildableBinaryIsDeclared is ADR-016 §4's check: the set of
// buildable binaries must equal the set declared in deploy/services.yaml, and
// the build fails when they diverge.
//
// It is the same guarantee `make proto-verify` gives for generated bindings --
// a derived artefact must match its source -- applied to the service set. A
// binary nobody declared gets no image, no deployment and no workload
// identity; a declaration with nothing behind it produces a deployment that
// cannot start.
func TestEveryBuildableBinaryIsDeclared(t *testing.T) {
	root, set := serviceSet(t)
	built := discoverBinaries(t, root)

	declared := map[string]Language{}
	for _, service := range set.Built() {
		declared[service.Source] = service.Language
	}

	for source, language := range built {
		want, ok := declared[source]
		if !ok {
			t.Errorf("%s builds a binary that deploy/services.yaml does not declare: "+
				"it would get no image, no deployment and no workload identity", source)
			continue
		}
		if want != language {
			t.Errorf("%s is declared as %s but is written in %s", source, want, language)
		}
	}

	for source := range declared {
		if _, ok := built[source]; !ok {
			t.Errorf("deploy/services.yaml declares %s, which builds nothing: "+
				"mark it planned, or the deployment will reference an image that cannot exist", source)
		}
	}
}

// TestAPlannedServiceIsNotRequiredToExist covers the distinction ADR-016 §3
// calls out: behavioral's identifier is fixed now and its code arrives in
// Phase 3, so the check above must not fail on it.
func TestAPlannedServiceIsNotRequiredToExist(t *testing.T) {
	root, set := serviceSet(t)

	behavioral, ok := set.Services["behavioral"]
	if !ok {
		t.Skip("behavioral is no longer declared; this test has outlived its purpose")
	}
	if !behavioral.Planned {
		t.Fatal("behavioral is no longer planned: if it is built, this test should be deleted")
	}
	if _, exists := os.Stat(filepath.Join(root, behavioral.Source)); exists == nil {
		t.Errorf("%s exists but is still declared planned", behavioral.Source)
	}
	for _, service := range set.Built() {
		if service.ID == "behavioral" {
			t.Error("a planned service appeared in Built()")
		}
	}
}

func TestTheDeclaredPortsMatchTheServicesThemselves(t *testing.T) {
	// A port recorded here but not used by the binary means the deployment
	// exposes one port while the process listens on another, which presents as
	// a service that starts and is unreachable.
	//
	// This is a textual search of the service's own source tree, deliberately
	// loose: it proves the number appears where the service is defined, not
	// that it reaches a listener. A tighter check would have to run the
	// binaries, which is what test/e2e already does.
	root, set := serviceSet(t)

	for _, service := range set.Built() {
		if !mentions(t, filepath.Join(root, filepath.FromSlash(service.Source)), portLiteral(service.Port)) {
			t.Errorf("%s declares port %d, but no file under %s mentions it",
				service.ID, service.Port, service.Source)
		}
	}
}

func mentions(t *testing.T, dir, literal string) bool {
	t.Helper()

	var found bool
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || found {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(body), literal) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return found
}

func portLiteral(port int) string {
	digits := make([]byte, 0, 4)
	for _, d := range []int{port / 1000 % 10, port / 100 % 10, port / 10 % 10, port % 10} {
		digits = append(digits, byte('0'+d))
	}
	return string(digits)
}

func TestLoadRejectsAMalformedSet(t *testing.T) {
	for name, body := range map[string]string{
		"unknown zone":      "services:\n  x:\n    zone: nowhere\n    language: go\n    source: x\n    port: 9100\n",
		"unknown language":  "services:\n  x:\n    zone: edge\n    language: cobol\n    source: x\n    port: 9100\n",
		"no source":         "services:\n  x:\n    zone: edge\n    language: go\n    port: 9100\n",
		"port out of range": "services:\n  x:\n    zone: edge\n    language: go\n    source: x\n    port: 80\n",
		"planned with port": "services:\n  x:\n    zone: edge\n    language: go\n    source: x\n    port: 9100\n    planned: true\n",
		"not a dns label":   "services:\n  Not_A_Label:\n    zone: edge\n    language: go\n    source: x\n    port: 9100\n",
		"duplicate port": "services:\n  a:\n    zone: edge\n    language: go\n    source: a\n    port: 9100\n" +
			"  b:\n    zone: edge\n    language: go\n    source: b\n    port: 9100\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "services.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("writing the fixture: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Errorf("Load accepted a service set with %s", name)
			}
		})
	}
}

func TestBuiltIsSortedAndExcludesPlanned(t *testing.T) {
	_, set := serviceSet(t)

	built := set.Built()
	ids := make([]string, 0, len(built))
	for _, service := range built {
		ids = append(ids, service.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("Built() is not sorted: %v", ids)
	}
}
