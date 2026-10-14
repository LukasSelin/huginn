package semdiff

import (
	"path"
	"strconv"
	"strings"
)

// ModulePath returns the module path declared by a go.mod file, or "" if it
// declares none.
func ModulePath(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "module" {
			continue
		}
		mod := fields[1]
		if unq, err := strconv.Unquote(mod); err == nil {
			mod = unq
		}
		return mod
	}
	return ""
}

// modules maps repository directories holding a go.mod to their module paths.
type modules map[string]string

func newModules(gomods map[string][]byte) modules {
	m := modules{}
	for p, src := range gomods {
		if mod := ModulePath(src); mod != "" {
			m[cleanDir(path.Dir(p))] = mod
		}
	}
	return m
}

// importPath returns the import path of the package in the repository
// directory dir, using the nearest enclosing module. Outside any module the
// directory itself is used, which still distinguishes packages from each
// other within the repository.
func (m modules) importPath(dir string) string {
	dir = cleanDir(dir)
	for d := dir; ; d = cleanDir(path.Dir(d)) {
		if mod, ok := m[d]; ok {
			rel := strings.TrimPrefix(strings.TrimPrefix(dir, d), "/")
			if d == "" {
				rel = dir
			}
			if rel == "" {
				return mod
			}
			return mod + "/" + rel
		}
		if d == "" {
			return dir
		}
	}
}

// cleanDir normalizes a slash-separated repository directory, with "" for
// the root.
func cleanDir(dir string) string {
	dir = path.Clean(strings.ReplaceAll(dir, `\`, "/"))
	if dir == "." || dir == "/" {
		return ""
	}
	return strings.TrimPrefix(dir, "/")
}
