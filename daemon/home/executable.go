package home

import (
	"sort"
	"strings"
)

// pathDelimiter is the PATH separator of a platform.
func pathDelimiter(platform string) string {
	if platform == windows {
		return ";"
	}
	return ":"
}

// joinFor joins with the path rules of a platform.
func joinFor(platform string, parts ...string) string {
	return join(Host{Platform: platform}, parts...)
}

// PathKey is the name of the PATH variable: Windows spells it Path and compares names without case.
func PathKey(env Env, platform string) string {
	if platform != windows {
		return "PATH"
	}
	names := make([]string, 0, len(env))
	for k := range env {
		if strings.EqualFold(k, "PATH") {
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		return "Path"
	}
	sort.Strings(names) // a map has no order; the choice stays the same between runs
	return names[0]
}

// PathDirs is the non-empty directories of PATH in `env`.
func PathDirs(env Env, platform string) []string {
	var dirs []string
	for _, d := range strings.Split(env[PathKey(env, platform)], pathDelimiter(platform)) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func nameVariants(name string, env Env, platform string) []string {
	if platform != windows {
		return []string{name}
	}
	pathext, ok := env["PATHEXT"]
	if !ok {
		pathext = ".COM;.EXE;.BAT;.CMD"
	}
	var exts []string
	for _, e := range strings.Split(pathext, ";") {
		if e != "" {
			exts = append(exts, e)
		}
	}
	upper := strings.ToUpper(name)
	for _, e := range exts {
		if strings.HasSuffix(upper, strings.ToUpper(e)) {
			return []string{name}
		}
	}
	variants := []string{name}
	for _, e := range exts {
		variants = append(variants, name+e)
	}
	return variants
}

// ResolveExecutable finds `name` the way a shell would, against the PATH in `env`: the first directory that holds an
// executable file of that name. A name with a directory part is checked as given. `isExecutable` is the only I/O.
func ResolveExecutable(name string, env Env, platform string, isExecutable func(file string) bool) (string, bool) {
	variants := nameVariants(name, env, platform)
	if strings.Contains(name, "/") || (platform == windows && strings.Contains(name, `\`)) {
		for _, v := range variants {
			if isExecutable(v) {
				return v, true
			}
		}
		return "", false
	}
	for _, dir := range PathDirs(env, platform) {
		for _, v := range variants {
			if hit := joinFor(platform, dir, v); isExecutable(hit) {
				return hit, true
			}
		}
	}
	return "", false
}
