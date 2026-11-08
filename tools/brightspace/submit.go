package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func findFolder(fs []folder, arg string) (folder, error) {
	arg = strings.TrimSpace(arg)
	var visible []folder
	for _, f := range fs {
		if !f.IsHidden {
			visible = append(visible, f)
		}
	}
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, f := range visible {
			if f.ID == id {
				return f, nil
			}
		}
		return folder{}, fail("NOT_FOUND", "", "No assignment folder has id %d in this course", id)
	}
	low := strings.ToLower(arg)
	var m []folder
	for _, f := range visible {
		if strings.Contains(strings.ToLower(f.Name), low) {
			m = append(m, f)
		}
	}
	switch len(m) {
	case 0:
		return folder{}, fail("NOT_FOUND", "", "No assignment matches %q", arg)
	case 1:
		return m[0], nil
	}
	var names []string
	for i, f := range m {
		if i == 5 {
			names = append(names, "…")
			break
		}
		names = append(names, fmt.Sprintf("%d %s", f.ID, f.Name))
	}
	return folder{}, invalid("%q matches several assignments (%s). Use the id", arg, strings.Join(names, "; "))
}

type upload struct {
	path string
	size int64
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// checkFolder refuses submissions the folder can't take, from what the API
// tells students. Attempt limits and type categories are enforced by the
// site when the upload is sent.
func checkFolder(f folder, now time.Time, hasFiles, hasText bool) error {
	if f.Availability != nil {
		if s := parseTime(f.Availability.StartDate); !s.IsZero() && now.Before(s) {
			return fail("CLOSED", "", "%q opens on %s", f.Name, when(s))
		}
		if e := parseTime(f.Availability.EndDate); !e.IsZero() && now.After(e) {
			return fail("CLOSED", "", "%q closed on %s", f.Name, when(e))
		}
	}
	switch f.SubmissionType {
	case 3:
		return fail("UNSUPPORTED_FOLDER", "", "%q is an in-person (observed) assignment, not an upload", f.Name)
	case 1:
		if hasFiles {
			return fail("FILE_REJECTED", "pass text instead of files.", "%q only accepts online text", f.Name)
		}
	case 0:
		if !hasFiles {
			return fail("FILE_REJECTED", "pass files.", "%q only accepts files", f.Name)
		}
	}
	return nil
}

func extAllowed(f folder, name string) bool {
	if f.CustomAllowableFileTypes == "" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	for _, t := range strings.FieldsFunc(f.CustomAllowableFileTypes, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if strings.ToLower(strings.TrimPrefix(t, ".")) == strings.TrimPrefix(ext, ".") {
			return true
		}
	}
	return false
}

func (a *api) toolSubmit(ctx context.Context, args map[string]string) (string, error) {
	files := splitList(args["files"])
	text := strings.TrimSpace(args["text"])
	if len(files) == 0 && text == "" {
		return "", invalid("pass files, text, or both")
	}
	c, err := a.resolveCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	fs, err := a.folders(ctx, c.ID)
	if err != nil {
		return "", err
	}
	f, err := findFolder(fs, args["assignment"])
	if err != nil {
		return "", err
	}
	if err := checkFolder(f, a.now(), len(files) > 0, text != "" && len(files) == 0); err != nil {
		return "", err
	}
	var ups []upload
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			return "", fail("FILE_REJECTED", "pass full paths of regular files, comma-separated.", "%q isn't a readable file", p)
		}
		if st.Size() == 0 {
			return "", fail("FILE_REJECTED", "", "%q is empty", p)
		}
		if !extAllowed(f, p) {
			return "", fail("FILE_REJECTED", "", "%q isn't an accepted type (%s)", filepath.Base(p), f.CustomAllowableFileTypes)
		}
		ups = append(ups, upload{p, st.Size()})
	}

	prior, _ := a.mySubmissions(ctx, c.ID, f.ID)
	var b strings.Builder
	fmt.Fprintf(&b, "Course: %s\nAssignment: %s (id %d)\n", c, f.Name, f.ID)
	fmt.Fprintf(&b, "Due: %s\n", when(parseTime(f.DueDate)))
	if f.Availability != nil && f.Availability.EndDate != "" {
		fmt.Fprintf(&b, "Closes: %s\n", when(parseTime(f.Availability.EndDate)))
	}
	fmt.Fprintf(&b, "Existing submissions: %d (attempt limits and file rules are enforced by the site)\n", len(prior))
	for _, u := range ups {
		fmt.Fprintf(&b, "File: %s (%s)\n", u.path, size(u.size))
	}
	if text != "" {
		fmt.Fprintf(&b, "Text: %s\n", trunc(text, 300))
	}
	if args["confirm"] != "true" {
		b.WriteString("\nNothing was sent. Show this to the user and, once they agree, call again with confirm=true.")
		return b.String(), nil
	}

	resp, err := a.send(ctx, func(string) (*http.Request, error) {
		path, err := a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/%d/submissions/mysubmissions/", c.ID, f.ID))
		if err != nil {
			return nil, err
		}
		body, ctype := submissionBody(text, ups)
		req, err := http.NewRequest("POST", a.site+path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", ctype)
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", statusErr(resp, "The submission")
	}
	after, err := a.mySubmissions(ctx, c.ID, f.ID)
	if err != nil || len(after) == 0 {
		return fmt.Sprintf("Submitted to %q (the site accepted it), but the submission couldn't be read back.", f.Name), nil
	}
	last := after[len(after)-1]
	var names []string
	for _, fl := range last.Files {
		names = append(names, fmt.Sprintf("%s (%s)", fl.FileName, size(fl.Size)))
	}
	out := fmt.Sprintf("Submitted to %q: submission %d at %s, %d submission(s) in total.", f.Name, last.ID, when(parseTime(last.SubmissionDate)), len(after))
	if len(names) > 0 {
		out += "\nFiles: " + strings.Join(names, ", ")
	}
	return out, nil
}

// submissionBody streams a multipart/mixed body: a JSON part with the text,
// then one part per file.
func submissionBody(text string, ups []upload) (io.Reader, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			h := textproto.MIMEHeader{}
			h.Set("Content-Type", "application/json")
			part, err := mw.CreatePart(h)
			if err != nil {
				return err
			}
			js, _ := json.Marshal(map[string]any{"Text": text, "Html": nil})
			if _, err := part.Write(js); err != nil {
				return err
			}
			for _, u := range ups {
				ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(u.path)))
				if ct == "" {
					ct = "application/octet-stream"
				}
				h := textproto.MIMEHeader{}
				h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=""; filename=%q`, filepath.Base(u.path)))
				h.Set("Content-Type", ct)
				part, err := mw.CreatePart(h)
				if err != nil {
					return err
				}
				f, err := os.Open(u.path)
				if err != nil {
					return err
				}
				_, err = io.Copy(part, f)
				f.Close()
				if err != nil {
					return err
				}
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()
	return pr, "multipart/mixed; boundary=" + mw.Boundary()
}
