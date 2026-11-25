package core

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The core talks to adapters only through the interfaces it declares. A direct import of an adapter package would tie the
// core to one upstream tool, so this test parses every file of the package (tests included) and refuses it.
func TestCoreImportsNoAdapterPackage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no files: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(p, "/internal/adapters") {
				t.Errorf("%s imports %s: the core must not import an adapter package", f, p)
			}
		}
	}
}

// The same rule, transitively, for the non-test code: nothing the core imports may import an adapter either.
func TestCoreDoesNotReachAdaptersThroughItsImports(t *testing.T) {
	reached := map[string]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		fset := token.NewFileSet()
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range af.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				i := strings.Index(p, "/internal/")
				if i < 0 || reached[p] {
					continue
				}
				reached[p] = true
				if strings.Contains(p, "/internal/adapters") {
					t.Errorf("%s reaches adapter package %s", dir, p)
				}
				walk(filepath.Join("..", strings.TrimPrefix(p[i:], "/internal/")))
			}
		}
	}
	walk(".")
	if len(reached) == 0 {
		t.Fatal("the walk found nothing: the test is broken")
	}
}
