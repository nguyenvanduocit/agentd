package home

import "strings"

// winJoin is Node's path.win32.join for the paths the daemon builds: parts joined with a backslash, then normalized
// ("/" becomes "\", "." and ".." resolve, repeated separators collapse; a drive or UNC root stays).
func winJoin(parts ...string) string {
	joined := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if joined == "" {
			joined = p
		} else {
			joined += `\` + p
		}
	}
	if joined == "" {
		return "."
	}
	return winNormalize(joined)
}

func winNormalize(p string) string {
	p = strings.ReplaceAll(p, "/", `\`)
	root, rest := winRoot(p)
	absolute := strings.HasSuffix(root, `\`)
	var stack []string
	for _, seg := range strings.Split(rest, `\`) {
		switch seg {
		case "", ".":
		case "..":
			if n := len(stack); n > 0 && stack[n-1] != ".." {
				stack = stack[:n-1]
			} else if !absolute {
				stack = append(stack, "..")
			}
		default:
			stack = append(stack, seg)
		}
	}
	tail := strings.Join(stack, `\`)
	if tail == "" {
		if absolute {
			return root
		}
		return root + "."
	}
	if strings.HasSuffix(rest, `\`) {
		tail += `\`
	}
	return root + tail
}

// winRoot splits a backslash path into its root (`C:\`, `C:`, `\`, `\\server\share\` or "") and the rest.
func winRoot(p string) (root, rest string) {
	switch {
	case strings.HasPrefix(p, `\\`):
		segs := strings.SplitN(p[2:], `\`, 3)
		if len(segs) >= 2 && segs[0] != "" && segs[1] != "" {
			root = `\\` + segs[0] + `\` + segs[1] + `\`
			if len(segs) == 3 {
				rest = segs[2]
			}
			return root, rest
		}
		return `\`, p[1:]
	case len(p) >= 2 && p[1] == ':' && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z'):
		root, rest = p[:2], p[2:]
		if strings.HasPrefix(rest, `\`) {
			return root + `\`, rest[1:]
		}
		return root, rest
	case strings.HasPrefix(p, `\`):
		return `\`, p[1:]
	}
	return "", p
}
