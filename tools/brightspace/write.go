package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Shared pieces of the staff write tools: what a preview saw (for CONFLICT),
// batch files, dates and rich text. See spec/brightspace-ta.md.

const batchMax = 200

const notSent = "\nNothing was sent. Show this to the user and, once they agree, call again with the same arguments and confirm=true."

// remember records the state a preview showed for key.
func (a *api) remember(key, state string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen == nil {
		a.seen = map[string]string{}
	}
	a.seen[key] = state
}

// checkSeen fails with CONFLICT when key changed since its preview. A write
// without a preview in this server process has nothing to compare against.
func (a *api) checkSeen(key, state, who string) error {
	a.mu.Lock()
	old, ok := a.seen[key]
	a.mu.Unlock()
	if ok && old != state {
		return fail("CONFLICT", "preview again to see the new value.", "%s changed since the preview (someone else edited it)", who)
	}
	return nil
}

// batchRows reads a CSV (with a header) or JSON (an array of objects) file
// into rows of lowercased column → value.
func batchRows(path string) ([]map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, invalid("can't read %s: %v", path, err)
	}
	var rows []map[string]string
	if strings.EqualFold(filepath.Ext(path), ".json") || strings.HasPrefix(strings.TrimSpace(string(b)), "[") {
		var raw []map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, invalid("%s: want a JSON array of objects: %v", filepath.Base(path), err)
		}
		for _, r := range raw {
			row := map[string]string{}
			for k, v := range r {
				switch x := v.(type) {
				case string:
					row[strings.ToLower(k)] = x
				case nil:
				default:
					js, _ := json.Marshal(x)
					row[strings.ToLower(k)] = string(js)
				}
			}
			rows = append(rows, row)
		}
	} else {
		cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(b), "\ufeff")))
		cr.FieldsPerRecord = -1
		recs, err := cr.ReadAll()
		if err != nil {
			return nil, invalid("%s: %v", filepath.Base(path), err)
		}
		if len(recs) < 2 {
			return nil, invalid("%s needs a header row and at least one row", filepath.Base(path))
		}
		head := recs[0]
		for _, rec := range recs[1:] {
			row := map[string]string{}
			for i, h := range head {
				if i < len(rec) {
					row[strings.ToLower(strings.TrimSpace(h))] = strings.TrimSpace(rec[i])
				}
			}
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, invalid("%s has no rows", filepath.Base(path))
	}
	if len(rows) > batchMax {
		return nil, invalid("%s has %d rows; at most %d per call", filepath.Base(path), len(rows), batchMax)
	}
	for i, r := range rows {
		if r["who"] == "" {
			if r["entity"] != "" {
				r["who"] = r["entity"]
			} else {
				return nil, invalid("row %d has no who (or entity) column", i+1)
			}
		}
		if r["text"] == "" && r["feedback"] != "" {
			r["text"] = r["feedback"]
		}
	}
	return rows, nil
}

// userTime reads a date the user typed, in local time. A date alone means
// the end of that day.
func userTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, l := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t.Add(23*time.Hour + 59*time.Minute), nil
	}
	return time.Time{}, invalid("%q isn't a date: use 2026-11-20 23:59", s)
}

var (
	mdBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdItalic = regexp.MustCompile(`(^|[^*])\*([^*\s][^*]*)\*`)
	mdCode   = regexp.MustCompile("`([^`]+)`")
	mdLink   = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	mdItem   = regexp.MustCompile(`^\s*([-*]|\d+\.)\s+`)
)

func mdInline(s string) string {
	s = html.EscapeString(s)
	s = mdLink.ReplaceAllString(s, `<a href="$2">$1</a>`)
	s = mdBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = mdItalic.ReplaceAllString(s, "$1<em>$2</em>")
	s = mdCode.ReplaceAllString(s, "<code>$1</code>")
	return s
}

// mdToHTML turns light markdown (paragraphs, lists, bold, italic, code,
// links) into the HTML the site shows.
func mdToHTML(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n")
	var b strings.Builder
	for _, block := range regexp.MustCompile(`\n\s*\n`).Split(s, -1) {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 || lines[0] == "" {
			continue
		}
		list := true
		for _, l := range lines {
			if !mdItem.MatchString(l) {
				list = false
			}
		}
		if list {
			tag := "ul"
			if m := mdItem.FindStringSubmatch(lines[0]); m != nil && m[1] != "-" && m[1] != "*" {
				tag = "ol"
			}
			b.WriteString("<" + tag + ">")
			for _, l := range lines {
				b.WriteString("<li>" + mdInline(mdItem.ReplaceAllString(l, "")) + "</li>")
			}
			b.WriteString("</" + tag + ">")
			continue
		}
		if h := strings.TrimLeft(lines[0], "#"); len(lines) == 1 && len(h) < len(lines[0]) && strings.HasPrefix(h, " ") {
			b.WriteString("<p><strong>" + mdInline(strings.TrimSpace(h)) + "</strong></p>")
			continue
		}
		parts := make([]string, len(lines))
		for i, l := range lines {
			parts[i] = mdInline(strings.TrimSpace(l))
		}
		b.WriteString("<p>" + strings.Join(parts, "<br>") + "</p>")
	}
	return b.String()
}

func richInput(md string) map[string]string {
	return map[string]string{"Content": mdToHTML(md), "Type": "Html"}
}

// sendJSON sends a JSON body and returns the reply's bytes.
func (a *api) sendJSON(ctx context.Context, method, path string, body any, what string) ([]byte, error) {
	js, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := a.send(ctx, func(string) (*http.Request, error) {
		req, err := http.NewRequest(method, a.site+path, strings.NewReader(string(js)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, statusErr(resp, what)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func parseNumber(s, what string) (*float64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, invalid("%s %q isn't a number", what, s)
	}
	return &f, nil
}
