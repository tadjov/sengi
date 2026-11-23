package pkg_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// allowedInternalImports maps a package folder under pkg/ to why it may still import internal/.
var allowedInternalImports = map[string]string{
	"apis/mapping": "its spec maps onto document.Article and language.Code, which still live in internal/",
}

func TestPackagesUnderPkgImportNothingFromInternal(t *testing.T) {
	internal := modulePath(t) + "/internal"
	files := token.NewFileSet()
	parsed := 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if _, allowed := allowedInternalImports[filepath.ToSlash(path)]; allowed || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		file, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		parsed++
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if imported == internal || strings.HasPrefix(imported, internal+"/") {
				t.Errorf("pkg/%s imports %s; a package under pkg/ must not import internal/", filepath.ToSlash(path), imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if parsed == 0 {
		t.Fatal("found no Go files under pkg/")
	}
}

func modulePath(t *testing.T) string {
	t.Helper()
	goMod, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(goMod), "\n") {
		if module, found := strings.CutPrefix(strings.TrimSpace(line), "module "); found {
			return strings.TrimSpace(module)
		}
	}
	t.Fatal("go.mod declares no module")
	return ""
}
