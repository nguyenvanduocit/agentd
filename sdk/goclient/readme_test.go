package goclient_test

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"
)

// readmeLines is README.md with its line numbers.
func readmeLines(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("README.md")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// compiledLines is the trimmed lines of the files that the compiler sees and the README quotes.
func compiledLines(t *testing.T, files ...string) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	for _, name := range files {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			set[strings.TrimSpace(line)] = true
		}
	}
	return set
}

var structural = regexp.MustCompile(`^(import \(|\)|\{|\}|\}\)|)$`)

// A Go block of the README is a quotation of code that is compiled (example_test.go, examples/askagent/main.go): every
// line of it is a line of one of them, so a README cannot keep naming what the package no longer has. A block that is
// meant as a sketch says so with the line `<!-- fragment -->` before it.
func TestEveryLineOfAGoBlockOfTheReadmeIsCompiled(t *testing.T) {
	compiled := compiledLines(t, "example_test.go", "examples/askagent/main.go")
	lines := readmeLines(t)
	inBlock, skip := false, false
	blocks := 0
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "```go"):
			inBlock = true
			skip = i > 0 && strings.TrimSpace(lines[i-1]) == "<!-- fragment -->"
			blocks++
		case inBlock && strings.HasPrefix(line, "```"):
			inBlock = false
		case inBlock && !skip:
			trimmed := strings.TrimSpace(line)
			if structural.MatchString(trimmed) || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if !compiled[trimmed] {
				t.Errorf("README.md:%d: %q is not a line of example_test.go or examples/askagent/main.go", i+1, trimmed)
			}
		}
	}
	if blocks == 0 {
		t.Fatal("the README has no Go block")
	}
}

// exportedNames is every exported top-level name of the package, from its source.
func exportedNames(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil && d.Name.IsExported() {
						names[d.Name.Name] = true
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							names[s.Name.Name] = true
						case *ast.ValueSpec:
							for _, n := range s.Names {
								names[n.Name] = true
							}
						}
					}
				}
			}
		}
	}
	return names
}

var qualified = regexp.MustCompile(`goclient\.([A-Z][A-Za-z0-9]*)`)

// The README names the package's identifiers as goclient.Name, in prose and in code: each is there.
func TestEveryGoclientNameTheReadmeMentionsExists(t *testing.T) {
	names := exportedNames(t)
	for i, line := range readmeLines(t) {
		for _, m := range qualified.FindAllStringSubmatch(line, -1) {
			if !names[m[1]] {
				t.Errorf("README.md:%d: goclient.%s does not exist", i+1, m[1])
			}
		}
	}
}
