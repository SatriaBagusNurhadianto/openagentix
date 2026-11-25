package core

import (
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The platform packages (plan, policy, api) hold platform words only. An upstream product's name belongs to its adapter, to
// the catalog's adapter-owned section, or to the chart. This reads every non-test file of the three packages and refuses the
// five words below (case-insensitive) in code, in identifiers and in strings; a comment may use them only when it cites an ADR.
func TestPlatformPackagesHoldNoUpstreamVocabulary(t *testing.T) {
	banned := []string{"kagent", "toolhive", "vmcp", "harness", "modelconfig"}
	files := 0
	for _, pkg := range []string{"plan", "policy", "api"} {
		list, err := filepath.Glob(filepath.Join("..", pkg, "*.go"))
		if err != nil || len(list) == 0 {
			t.Fatalf("%s: no files: %v", pkg, err)
		}
		for _, f := range list {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			files++
			fset := token.NewFileSet()
			file := fset.AddFile(f, fset.Base(), len(src))
			var s scanner.Scanner
			s.Init(file, src, nil, scanner.ScanComments)
			for {
				pos, tok, lit := s.Scan()
				if tok == token.EOF {
					break
				}
				text := lit
				if tok != token.COMMENT && tok != token.IDENT && tok != token.STRING && tok != token.CHAR {
					continue
				}
				if tok == token.COMMENT && strings.Contains(text, "ADR") {
					continue
				}
				low := strings.ToLower(text)
				for _, b := range banned {
					if strings.Contains(low, b) {
						t.Errorf("%s: %q names an upstream product (%s); move it to the adapter or the catalog's adapter section", fset.Position(pos), text, b)
					}
				}
			}
		}
	}
	if files < 4 {
		t.Fatalf("scanned %d files: the test is broken", files)
	}
}

// The test above must be able to fail: this feeds it a word it must refuse.
func TestVocabularyScanSeesAPlantedWord(t *testing.T) {
	var s scanner.Scanner
	fset := token.NewFileSet()
	src := []byte("package x\n// uses kagent here\nvar a = \"ModelConfig\"\nvar harnessEnv int\n// see ADR 0012 about toolhive\n")
	s.Init(fset.AddFile("x.go", fset.Base(), len(src)), src, nil, scanner.ScanComments)
	hits := 0
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if (tok == token.COMMENT && !strings.Contains(lit, "ADR") || tok == token.STRING || tok == token.IDENT) && (strings.Contains(strings.ToLower(lit), "kagent") || strings.Contains(strings.ToLower(lit), "modelconfig") || strings.Contains(strings.ToLower(lit), "harness")) {
			hits++
		}
	}
	if hits != 3 {
		t.Fatalf("hits %d, want 3 (a comment, a string, an identifier; the ADR comment is allowed)", hits)
	}
}
