package main

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type topic struct {
	ID               int64 `json:"TopicId"`
	Title            string
	TypeIdentifier   string
	URL              string `json:"Url"`
	LastModifiedDate string
	IsHidden         bool
}

type module struct {
	ID       int64 `json:"ModuleId"`
	Title    string
	IsHidden bool
	Modules  []module
	Topics   []topic
}

func (a *api) toc(ctx context.Context, org int64) ([]module, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/content/toc", org))
	if err != nil {
		return nil, err
	}
	var r struct{ Modules []module }
	if err := a.getJSON(ctx, path, &r); err != nil {
		return nil, err
	}
	return r.Modules, nil
}

// flatTopic is a topic with the titles of the modules above it.
type flatTopic struct {
	topic
	path []string
}

func flatten(ms []module, path []string, out *[]flatTopic) {
	for _, m := range ms {
		if m.IsHidden {
			continue
		}
		p := append(append([]string{}, path...), m.Title)
		for _, t := range m.Topics {
			if !t.IsHidden {
				*out = append(*out, flatTopic{t, p})
			}
		}
		flatten(m.Modules, p, out)
	}
}

func findTopic(ts []flatTopic, arg string) (flatTopic, error) {
	arg = strings.TrimSpace(arg)
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, t := range ts {
			if t.ID == id {
				return t, nil
			}
		}
		return flatTopic{}, fail("NOT_FOUND", "list the topics with brightspace_content.", "No topic has id %d in this course", id)
	}
	f := strings.ToLower(arg)
	var m []flatTopic
	for _, t := range ts {
		if strings.Contains(strings.ToLower(t.Title), f) {
			m = append(m, t)
		}
	}
	switch len(m) {
	case 0:
		return flatTopic{}, fail("NOT_FOUND", "list the topics with brightspace_content.", "No topic matches %q", arg)
	case 1:
		return m[0], nil
	}
	var names []string
	for i, t := range m {
		if i == 5 {
			names = append(names, "…")
			break
		}
		names = append(names, fmt.Sprintf("%d %s", t.ID, t.Title))
	}
	return flatTopic{}, invalid("%q matches several topics (%s). Use the id", arg, strings.Join(names, "; "))
}

func (a *api) toolContent(ctx context.Context, args map[string]string) (string, error) {
	c, err := a.resolveCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	ms, err := a.toc(ctx, c.ID)
	if err != nil {
		return "", err
	}
	if args["topic"] == "" {
		var b strings.Builder
		lines := 0
		var walk func(ms []module, depth int)
		walk = func(ms []module, depth int) {
			for _, m := range ms {
				if m.IsHidden {
					continue
				}
				if lines == 150 {
					return
				}
				fmt.Fprintf(&b, "%s%s\n", strings.Repeat("  ", depth), m.Title)
				lines++
				for _, t := range m.Topics {
					if t.IsHidden {
						continue
					}
					if lines == 150 {
						return
					}
					fmt.Fprintf(&b, "%s[%d] %s (%s)\n", strings.Repeat("  ", depth+1), t.ID, t.Title, t.TypeIdentifier)
					lines++
				}
				walk(m.Modules, depth+1)
			}
		}
		walk(ms, 0)
		if lines == 0 {
			return "This course has no content.", nil
		}
		if lines == 150 {
			b.WriteString("… (truncated; pass topic to read one)\n")
		}
		return strings.TrimSpace(b.String()), nil
	}
	var flat []flatTopic
	flatten(ms, nil, &flat)
	t, err := findTopic(flat, args["topic"])
	if err != nil {
		return "", err
	}
	if t.TypeIdentifier != "File" {
		return fmt.Sprintf("%s (%s)\n%s", t.Title, t.TypeIdentifier, t.URL), nil
	}
	resp, err := a.topicFile(ctx, c.ID, t.ID)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/") {
		return fmt.Sprintf("%s: a %s file (%s). Use brightspace_download to save it.", t.Title, ct, size(resp.ContentLength)), nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	txt := string(b)
	if strings.Contains(ct, "html") {
		txt = plain(txt)
	}
	return trunc(strings.TrimSpace(txt), 6000), nil
}

// topicFile opens a file topic's content.
func (a *api) topicFile(ctx context.Context, org, topicID int64) (*http.Response, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/content/topics/%d/file?stream=true", org, topicID))
	if err != nil {
		return nil, err
	}
	resp, err := a.send(ctx, func(string) (*http.Request, error) { return http.NewRequest("GET", a.site+path, nil) })
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, statusErr(resp, "The file")
	}
	return resp, nil
}

// remoteName is the file name the site gives a download, or fallback.
func remoteName(resp *http.Response, fallback string) string {
	if _, p, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && p["filename"] != "" {
		return safeName(p["filename"])
	}
	return safeName(fallback)
}

// defaultDest is where downloads go when no destination is given.
func defaultDest(c course) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	code := c.Code
	if code == "" {
		code = strconv.FormatInt(c.ID, 10)
	}
	return filepath.Join(home, "Downloads", "brightspace", safeName(code))
}

// saveTo streams body into dir/name through a temporary file.
func saveTo(dir, name string, body io.Reader) (string, int64, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	final := filepath.Join(dir, name)
	tmp := final + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", 0, err
	}
	n, err := io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", 0, err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", 0, err
	}
	return final, n, nil
}

func (a *api) toolDownload(ctx context.Context, args map[string]string) (string, error) {
	if args["assignment"] != "" {
		if args["file"] == "" || args["topic"] != "" {
			return "", invalid("with assignment, pass file (and no topic)")
		}
	} else if args["topic"] == "" {
		return "", invalid("pass topic (a course file) or assignment and file")
	}
	c, err := a.resolveCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	if args["assignment"] != "" {
		return a.downloadAssignFile(ctx, args, c)
	}
	ms, err := a.toc(ctx, c.ID)
	if err != nil {
		return "", err
	}
	var flat []flatTopic
	flatten(ms, nil, &flat)
	t, err := findTopic(flat, args["topic"])
	if err != nil {
		return "", err
	}
	if t.TypeIdentifier != "File" {
		return "", invalid("%q is a %s topic, not a file", t.Title, t.TypeIdentifier)
	}
	dest := args["dest"]
	if dest == "" {
		dest = defaultDest(c)
	}
	resp, err := a.topicFile(ctx, c.ID, t.ID)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	path, n, err := saveTo(dest, remoteName(resp, t.Title), resp.Body)
	if err != nil {
		return "", fail("API_ERROR", "", "Couldn't save the file: %v", err)
	}
	return fmt.Sprintf("Saved %s (%s)", path, size(n)), nil
}
