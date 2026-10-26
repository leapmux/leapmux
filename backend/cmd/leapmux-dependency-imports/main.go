// Command leapmux-dependency-imports rewrites Go imports and module directives.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

type moduleChange struct {
	Old         string   `json:"old"`
	New         string   `json:"new"`
	Version     string   `json:"version"`
	Directories []string `json:"directories"`
}

type textEdit struct {
	start int
	end   int
	value string
}

func rewritePath(path string, changes []moduleChange, roots []string) string {
	owner := ""
	consider := func(root string) {
		if (path == root || strings.HasPrefix(path, root+"/")) && len(root) > len(owner) {
			owner = root
		}
	}
	for _, root := range roots {
		consider(root)
	}
	for _, change := range changes {
		consider(change.Old)
		consider(change.New)
	}
	for _, change := range changes {
		if owner == change.Old {
			return change.New + strings.TrimPrefix(path, change.Old)
		}
	}
	return path
}

func applyEdits(source []byte, edits []textEdit) []byte {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, edit := range edits {
		source = append(append(append([]byte{}, source[:edit.start]...), edit.value...), source[edit.end:]...)
	}
	return source
}

func rewriteGo(source []byte, changes []moduleChange, roots ...string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", source, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse Go imports: %w", err)
	}
	var edits []textEdit
	for _, spec := range file.Imports {
		old, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("unquote import: %w", err)
		}
		if updated := rewritePath(old, changes, roots); updated != old {
			edits = append(edits, textEdit{fset.Position(spec.Path.Pos()).Offset, fset.Position(spec.Path.End()).Offset, strconv.Quote(updated)})
		}
	}
	return applyEdits(source, edits), nil
}

func rewriteMod(source []byte, changes []moduleChange, roots ...string) ([]byte, error) {
	file, err := modfile.Parse("go.mod", source, nil)
	if err != nil {
		return nil, fmt.Errorf("parse module directives: %w", err)
	}
	var edits []textEdit
	processLine := func(line *modfile.Line, directive string, offset int) {
		if directive != "require" && directive != "replace" && directive != "tool" {
			return
		}
		raw := string(source[line.Start.Byte:line.End.Byte])
		cursor := 0
		versionIndex := -1
		targetVersion := ""
		for index, value := range line.Token {
			at := strings.Index(raw[cursor:], value)
			if at < 0 {
				continue
			}
			at += cursor
			cursor = at + len(value)
			if index == versionIndex {
				edits = append(edits, textEdit{line.Start.Byte + at, line.Start.Byte + cursor, targetVersion})
				continue
			}
			// A replace directive has a module path on each side of =>.
			isPath := index == offset || (directive == "replace" && index > offset && line.Token[index-1] == "=>")
			if !isPath {
				continue
			}
			path := value
			if unquoted, err := strconv.Unquote(value); err == nil {
				path = unquoted
			}
			updated := path
			if directive == "tool" {
				updated = rewritePath(path, changes, roots)
			} else {
				for _, change := range changes {
					if path == change.Old {
						updated = change.New
						break
					}
				}
			}
			if updated == path {
				continue
			}
			for _, change := range changes {
				if path == change.Old || strings.HasPrefix(path, change.Old+"/") {
					if directive != "tool" && index+1 < len(line.Token) && strings.HasPrefix(line.Token[index+1], "v") {
						versionIndex = index + 1
						targetVersion = change.Version
					}
					break
				}
			}
			if path != value {
				updated = strconv.Quote(updated)
			}
			edits = append(edits, textEdit{line.Start.Byte + at, line.Start.Byte + cursor, updated})
		}
	}
	for _, statement := range file.Syntax.Stmt {
		switch stmt := statement.(type) {
		case *modfile.Line:
			if len(stmt.Token) > 0 {
				processLine(stmt, stmt.Token[0], 1)
			}
		case *modfile.LineBlock:
			if len(stmt.Token) > 0 {
				for _, line := range stmt.Line {
					processLine(line, stmt.Token[0], 0)
				}
			}
		}
	}
	return applyEdits(source, edits), nil
}

func run(input io.Reader) error {
	const maxModuleChangeBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(input, maxModuleChangeBytes+1))
	if err != nil {
		return fmt.Errorf("read module changes: %w", err)
	}
	if len(data) > maxModuleChangeBytes {
		return errors.New("module changes exceed the one-megabyte input limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var changes []moduleChange
	if err := decoder.Decode(&changes); err != nil {
		return fmt.Errorf("decode module changes: %w", err)
	}
	if changes == nil {
		return errors.New("module changes must be an array")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("module changes must contain one JSON value")
	}
	seen := map[string]bool{}
	directories := []string{"backend", "desktop/go", "site"}
	for _, change := range changes {
		if err := module.CheckPath(change.Old); err != nil {
			return fmt.Errorf("invalid old module path: %w", err)
		}
		if err := module.CheckPath(change.New); err != nil {
			return fmt.Errorf("invalid new module path: %w", err)
		}
		if err := module.Check(change.New, change.Version); err != nil {
			return fmt.Errorf("invalid target module version: %w", err)
		}
		if change.Old == "github.com/leapmux/leapmux" || change.Old == change.New {
			return errors.New("module changes contain a protected or unchanged module")
		}
		if len(change.Directories) == 0 {
			return errors.New("module changes require affected directories")
		}
		for _, directory := range change.Directories {
			if !slices.Contains(directories, directory) {
				return fmt.Errorf("unsupported module directory: %s", directory)
			}
			key := directory + "\x00" + change.Old
			if seen[key] {
				return fmt.Errorf("duplicate module change for %s in %s", change.Old, directory)
			}
			seen[key] = true
		}
	}
	if len(changes) == 0 {
		return nil
	}
	roots := map[string][]string{}
	for _, directory := range directories {
		source, err := os.ReadFile(filepath.Join(directory, "go.mod"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read module roots in %s: %w", directory, err)
		}
		file, err := modfile.Parse(directory+"/go.mod", source, nil)
		if err != nil {
			return fmt.Errorf("parse module roots in %s: %w", directory, err)
		}
		if file.Module != nil {
			roots[directory] = append(roots[directory], file.Module.Mod.Path)
		}
		for _, requirement := range file.Require {
			roots[directory] = append(roots[directory], requirement.Mod.Path)
		}
		for _, replacement := range file.Replace {
			roots[directory] = append(roots[directory], replacement.Old.Path)
			if replacement.New.Version != "" {
				roots[directory] = append(roots[directory], replacement.New.Path)
			}
		}
	}
	workspaceRoots := append(append([]string{}, roots["backend"]...), roots["desktop/go"]...)
	roots["backend"], roots["desktop/go"] = workspaceRoots, workspaceRoots
	output, err := exec.Command("git", "ls-files", "-z", "--", "backend", "desktop/go", "site").Output()
	if err != nil {
		return fmt.Errorf("list tracked Go files: %w", err)
	}
	type pendingWrite struct {
		path    string
		content []byte
		mode    os.FileMode
	}
	var pending []pendingWrite
	for _, path := range strings.Split(string(output), "\x00") {
		if path == "" || (filepath.Ext(path) != ".go" && filepath.Base(path) != "go.mod") {
			continue
		}
		directory := ""
		for _, candidate := range directories {
			if strings.HasPrefix(path, candidate+"/") {
				directory = candidate
				break
			}
		}
		var applicable []moduleChange
		for _, change := range changes {
			if slices.Contains(change.Directories, directory) {
				applicable = append(applicable, change)
			}
		}
		if len(applicable) == 0 {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		var updated []byte
		if filepath.Ext(path) == ".go" {
			updated, err = rewriteGo(source, applicable, roots[directory]...)
		} else {
			updated, err = rewriteMod(source, applicable, roots[directory]...)
		}
		if err != nil {
			return fmt.Errorf("rewrite %s: %w", path, err)
		}
		if !bytes.Equal(source, updated) {
			pending = append(pending, pendingWrite{path, updated, info.Mode()})
		}
	}
	for _, write := range pending {
		if err := os.WriteFile(write.path, write.content, write.mode); err != nil {
			return fmt.Errorf("write %s: %w", write.path, err)
		}
	}
	return nil
}

func main() {
	if err := run(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
