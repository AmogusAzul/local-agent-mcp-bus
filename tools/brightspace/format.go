package main

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

var (
	tagRe   = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>`)
	brRe    = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/h[1-6]|/tr)\s*/?>`)
	anyTag  = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe = regexp.MustCompile(`[ \t\r\f\v]+`)
	nlRe    = regexp.MustCompile(`\n{3,}`)
)

// plain turns HTML into compact text.
func plain(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = tagRe.ReplaceAllString(s, "")
	s = brRe.ReplaceAllString(s, "\n")
	s = anyTag.ReplaceAllString(s, "")
	s = strings.ReplaceAll(html.UnescapeString(s), "\u00a0", " ")
	s = spaceRe.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = strings.Join(lines, "\n")
	s = nlRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// trunc cuts s to at most n runes, marking the cut.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// parseTime reads an API timestamp. Empty or unparseable gives the zero time.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// when formats a time as "Mon 12 Nov 14:00" in the local zone.
func when(t time.Time) string {
	if t.IsZero() {
		return "no date"
	}
	return t.Local().Format("Mon 2 Jan 15:04")
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// safeName makes a string usable as a file or folder name.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), ". ")
	if s == "" {
		s = "_"
	}
	return trunc(s, 100)
}
