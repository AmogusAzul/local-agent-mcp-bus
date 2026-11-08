package main

import (
	"path"
	"regexp"
	"strings"
)

var (
	fileLineRe = regexp.MustCompile(`^(?:\./)?([^\s:()]+\.[A-Za-z]+):(\d+): (.+)$`)
	lineNumRe  = regexp.MustCompile(`^l\.(\d+)`)
	warningRe  = regexp.MustCompile(`^(?:LaTeX|Package \S+|Class \S+) Warning: `)
	openFileRe = regexp.MustCompile(`^"?(\.?/?[^\s()"]+\.[A-Za-z0-9]+)`)
)

// logErrors pulls up to max errors out of a LaTeX log as "file:line: message"
// and counts warnings. Without -file-line-error (Overleaf's default) the file
// comes from TeX's "(file … )" nesting and the line from the "l.N" after "! …".
func logErrors(log string, max int) (errs []string, warnings int) {
	lines := strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n")
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] && len(errs) < max {
			seen[s] = true
			errs = append(errs, s)
		}
	}
	var stack []string // "" for a parenthesis that isn't a file
	current := func() string {
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i] != "" {
				return stack[i]
			}
		}
		return ""
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case warningRe.MatchString(l):
			warnings++
		case fileLineRe.MatchString(l):
			m := fileLineRe.FindStringSubmatch(l)
			add(m[1] + ":" + m[2] + ": " + strings.TrimSpace(m[3]))
			continue
		case strings.HasPrefix(l, "! "):
			msg := strings.TrimSpace(l[2:])
			line := ""
			j := i + 1
			for ; j < min(i+12, len(lines)); j++ {
				if m := lineNumRe.FindStringSubmatch(lines[j]); m != nil {
					line = m[1]
					break
				}
			}
			if f := current(); f != "" && line != "" {
				add(f + ":" + line + ": " + msg)
			} else if line != "" {
				add("! " + msg + " (l." + line + ")")
			} else {
				add("! " + msg)
			}
			if line != "" {
				i = j // skip the context lines, whose parentheses are source text
			}
			continue
		}
		for k := 0; k < len(l); k++ {
			switch l[k] {
			case '(':
				name := ""
				if m := openFileRe.FindStringSubmatch(l[k+1:]); m != nil {
					name = m[1]
				}
				stack = append(stack, displayName(name))
			case ')':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			}
		}
	}
	return errs, warnings
}

// displayName keeps project files relative and shortens system ones.
func displayName(name string) string {
	switch {
	case name == "":
		return ""
	case strings.HasPrefix(name, "./"):
		return name[2:]
	case strings.HasPrefix(name, "/"):
		return path.Base(name)
	}
	return name
}
