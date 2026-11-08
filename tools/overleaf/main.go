// overleaf creates, edits and compiles Overleaf projects (free plan) through the
// web app's own endpoints, with a local mirror of each project on Windows.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

const (
	maxRead   = 100 << 10
	remoteDir = outputDir + "/remote" // Overleaf's side of a push conflict
)

type server struct {
	base     string // Overleaf origin
	root     string // where mirrors live
	accounts string // account store file
	open     func(path string) error

	mu      sync.Mutex
	clients map[string]*client
}

func newServer() *server {
	conf, err := os.UserConfigDir()
	if err != nil {
		conf = os.TempDir()
	}
	return &server{
		base:     defaultBase,
		root:     filepath.Join(os.TempDir(), "lamb-overleaf"),
		accounts: filepath.Join(conf, "local-agent-mcp-bus", "overleaf.json"),
		open:     openFile,
		clients:  map[string]*client{},
	}
}

func openFile(p string) error {
	if runtime.GOOS != "windows" {
		return errors.New("opening files is only supported on Windows")
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", p).Start()
}

// client returns the client of an account ("" = the active one), reusing it
// across calls so the CSRF token and routing cookies stay warm.
func (s *server) client(account string) (*client, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	accts, err := loadAccounts(s.accounts)
	if err != nil {
		return nil, "", err
	}
	name, cookie, err := accts.cookie(account)
	if err != nil {
		return nil, "", err
	}
	if c, ok := s.clients[name]; ok {
		return c, name, nil
	}
	c := newClient(s.base, cookie, func(v string) { s.saveCookie(name, v) })
	s.clients[name] = c
	return c, name, nil
}

func (s *server) saveCookie(name, value string) {
	if name == "env" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	accts, err := loadAccounts(s.accounts)
	if err != nil {
		return
	}
	if _, ok := accts.Cookies[name]; ok {
		accts.Cookies[name] = value
		accts.save(s.accounts)
	}
}

func flag(name, v string) (bool, error) {
	switch v {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	}
	return false, invalid("%s must be \"true\" or \"false\", got %q", name, v)
}

// ---- accounts ----

func (s *server) login(ctx context.Context, args map[string]string) (string, error) {
	if args["cookie"] != "" {
		cookie := cleanCookie(args["cookie"])
		d, err := newClient(s.base, cookie, nil).dashboard(ctx)
		if err != nil {
			return "", err
		}
		email := d.Email
		if email == "" {
			email = args["account"]
		}
		if email == "" {
			return "", invalid("couldn't read the account's email; pass account=<name>")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		accts, err := loadAccounts(s.accounts)
		if err != nil {
			return "", err
		}
		accts.Cookies[email] = cookie
		accts.Active = email
		delete(s.clients, email)
		if err := accts.save(s.accounts); err != nil {
			return "", err
		}
		return fmt.Sprintf("Logged in as %s (active). %d projects.", email, len(live(d.Projects))), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	accts, err := loadAccounts(s.accounts)
	if err != nil {
		return "", err
	}
	if a := args["account"]; a != "" {
		if _, ok := accts.Cookies[a]; !ok {
			return "", fail("NOT_FOUND", "log in with cookie=<overleaf_session2>.", "account %s isn't logged in", a)
		}
		accts.Active = a
		if err := accts.save(s.accounts); err != nil {
			return "", err
		}
		return "Active account: " + a + ".", nil
	}
	if len(accts.Cookies) == 0 {
		return "No accounts. Hint: " + cookieHint, nil
	}
	var b strings.Builder
	for _, n := range accts.names() {
		mark := ""
		if n == accts.Active {
			mark = " (active)"
		}
		b.WriteString(n + mark + "\n")
	}
	return b.String(), nil
}

func (s *server) logout(_ context.Context, args map[string]string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	accts, err := loadAccounts(s.accounts)
	if err != nil {
		return "", err
	}
	a := args["account"]
	if a == "" {
		a = accts.Active
	}
	if _, ok := accts.Cookies[a]; !ok {
		return "", fail("NOT_FOUND", "", "account %q isn't logged in", a)
	}
	delete(accts.Cookies, a)
	delete(s.clients, a)
	if accts.Active == a {
		accts.Active = ""
		if n := accts.names(); len(n) > 0 {
			accts.Active = n[0]
		}
	}
	if err := accts.save(s.accounts); err != nil {
		return "", err
	}
	msg := "Logged out " + a + "."
	if accts.Active != "" {
		msg += " Active account: " + accts.Active + "."
	}
	return msg, nil
}

// ---- projects ----

func live(ps []project) []project {
	var out []project
	for _, p := range ps {
		if !p.Trashed && !p.Archived {
			out = append(out, p)
		}
	}
	return out
}

func (s *server) projects(ctx context.Context, _ map[string]string) (string, error) {
	c, name, err := s.client("")
	if err != nil {
		return "", err
	}
	d, err := c.dashboard(ctx)
	if err != nil {
		return "", err
	}
	mirrored := map[string]bool{}
	for _, m := range mirrors(s.root) {
		mirrored[m.snap.ProjectID] = true
	}
	ps := live(d.Projects)
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %d projects\n", name, len(ps))
	for _, p := range ps {
		fmt.Fprintf(&b, "%s · id:%s · updated %s", p.Name, p.ID, day(p.LastUpdated))
		if p.AccessLevel != "" && p.AccessLevel != "owner" {
			b.WriteString(" · " + p.AccessLevel)
		}
		if mirrored[p.ID] {
			b.WriteString(" · mirrored")
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func day(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

func (s *server) create(ctx context.Context, args map[string]string) (string, error) {
	name := strings.TrimSpace(args["name"])
	c, account, err := s.client("")
	if err != nil {
		return "", err
	}
	id, err := c.createProject(ctx, name)
	if err != nil {
		return "", err
	}
	m, n, err := s.pullInto(ctx, c, account, project{ID: id, Name: name}, true)
	if err != nil {
		return "", fmt.Errorf("created %q (id:%s), but the pull failed: %w", name, id, err)
	}
	return fmt.Sprintf("Created %q (id:%s), %d files mirrored in %s.\n%s", name, id, n, m.dir, s.listing(m)), nil
}

// remote finds a project in the account's list by id or exact name.
func (s *server) remote(ctx context.Context, c *client, ref string) (project, error) {
	d, err := c.dashboard(ctx)
	if err != nil {
		return project{}, err
	}
	var hits []project
	for _, p := range d.Projects {
		if p.ID == ref {
			return p, nil
		}
		if strings.EqualFold(p.Name, ref) && !p.Trashed {
			hits = append(hits, p)
		}
	}
	switch len(hits) {
	case 0:
		return project{}, fail("NOT_FOUND", "overleaf_projects lists them.", "no project %q", ref)
	case 1:
		return hits[0], nil
	}
	var ids []string
	for _, p := range hits {
		ids = append(ids, p.ID)
	}
	return project{}, fail("AMBIGUOUS", "pass the project id.", "%d projects are named %q: %s", len(hits), ref, strings.Join(ids, ", "))
}

func (s *server) pullInto(ctx context.Context, c *client, account string, p project, force bool) (*mirror, int, error) {
	m, err := findMirror(s.root, p.ID)
	if err != nil {
		return nil, 0, err
	}
	if m == nil {
		m = &mirror{mirrorDir(s.root, p.Name, p.ID), &snapshot{Hashes: map[string]string{}}}
	} else if !force {
		ch, err := m.changes()
		if err != nil {
			return nil, 0, err
		}
		if u := ch.unpushed(); len(u) > 0 {
			return nil, 0, fail("LOCAL_CHANGES", "push them first, or pull with force=\"true\" to discard them.", "unpushed local changes: %s", strings.Join(u, ", "))
		}
	}
	files, err := c.download(ctx, p.ID)
	if err != nil {
		return nil, 0, err
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return nil, 0, err
	}
	m.snap.Account, m.snap.ProjectID, m.snap.Name = account, p.ID, p.Name
	if err := m.write(files); err != nil {
		return nil, 0, err
	}
	return m, len(files), nil
}

func (s *server) pull(ctx context.Context, args map[string]string) (string, error) {
	force, err := flag("force", args["force"])
	if err != nil {
		return "", err
	}
	account := ""
	m, err := findMirror(s.root, args["project"])
	if err != nil {
		return "", err
	}
	if m != nil {
		account = m.snap.Account
	}
	c, account, err := s.client(account)
	if err != nil {
		return "", err
	}
	p, err := s.remote(ctx, c, args["project"])
	if err != nil {
		return "", err
	}
	m, n, err := s.pullInto(ctx, c, account, p, force)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Pulled %d files of %q into %s.\n%s", n, p.Name, m.dir, s.listing(m)), nil
}

// mirrorOf returns the project's mirror, pulling it first if there's none.
func (s *server) mirrorOf(ctx context.Context, ref string) (*mirror, error) {
	m, err := findMirror(s.root, ref)
	if err != nil || m != nil {
		return m, err
	}
	c, account, err := s.client("")
	if err != nil {
		return nil, err
	}
	p, err := s.remote(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	m, _, err = s.pullInto(ctx, c, account, p, false)
	return m, err
}

// ---- files ----

func (s *server) listing(m *mirror) string {
	local, err := m.files()
	if err != nil {
		return err.Error()
	}
	ch := diff(local, m.snap.Hashes)
	mark := map[string]string{}
	for _, p := range ch.Modified {
		mark[p] = "M "
	}
	for _, p := range ch.Added {
		mark[p] = "A "
	}
	paths := make([]string, 0, len(local))
	for p := range local {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		size := ""
		if fi, err := os.Stat(m.abs(p)); err == nil {
			size = humanSize(fi.Size())
		}
		fmt.Fprintf(&b, "%s%s (%s)\n", mark[p], p, size)
	}
	for _, p := range ch.Deleted {
		fmt.Fprintf(&b, "D %s\n", p)
	}
	return b.String()
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

var mimeByExt = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".pdf": "application/pdf",
}

func blobOf(p string, data []byte) core.Blob {
	return core.Blob{URI: "file:///" + filepath.ToSlash(p), MIMEType: mimeByExt[strings.ToLower(filepath.Ext(p))], Data: data}
}

var linesRe = regexp.MustCompile(`^(\d+)(?:-(\d*))?$`)

func (s *server) read(ctx context.Context, args map[string]string) (core.Result, error) {
	m, err := s.mirrorOf(ctx, args["project"])
	if err != nil {
		return core.Result{}, err
	}
	if args["path"] == "" {
		return core.Result{Text: m.dir + "\n" + s.listing(m)}, nil
	}
	rel, err := cleanRel(args["path"])
	if err != nil {
		return core.Result{}, err
	}
	p := m.abs(rel)
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return core.Result{}, fail("NOT_FOUND", "overleaf_read without path lists the files.", "no file %s", rel)
	}
	if err != nil {
		return core.Result{}, err
	}
	if mimeByExt[strings.ToLower(filepath.Ext(rel))] != "" {
		return core.Result{Text: fmt.Sprintf("%s (%s)", rel, humanSize(int64(len(data)))), Blobs: []core.Blob{blobOf(p, data)}}, nil
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return core.Result{Text: fmt.Sprintf("%s: binary, %s", rel, humanSize(int64(len(data))))}, nil
	}
	text := string(data)
	if r := args["lines"]; r != "" {
		mm := linesRe.FindStringSubmatch(strings.TrimSpace(r))
		if mm == nil {
			return core.Result{}, invalid("lines must look like \"40-80\", \"40-\" or \"40\", got %q", r)
		}
		all := strings.Split(text, "\n")
		from, _ := strconv.Atoi(mm[1])
		to := from
		if strings.Contains(r, "-") {
			to = len(all)
			if mm[2] != "" {
				to, _ = strconv.Atoi(mm[2])
			}
		}
		from, to = max(from, 1), min(to, len(all))
		if from > to {
			return core.Result{}, invalid("lines %q is outside the file's %d lines", r, len(all))
		}
		text = fmt.Sprintf("[%s lines %d-%d of %d]\n", rel, from, to, len(all)) + strings.Join(all[from-1:to], "\n")
	}
	if len(text) > maxRead {
		cut := strings.LastIndexByte(text[:maxRead], '\n')
		if cut < 0 {
			cut = maxRead
		}
		text = text[:cut] + fmt.Sprintf("\n[truncated at line %d; pass lines to read more]", strings.Count(text[:cut], "\n")+1)
	}
	return core.Result{Text: text}, nil
}

func (s *server) write(ctx context.Context, args map[string]string) (string, error) {
	m, err := s.mirrorOf(ctx, args["project"])
	if err != nil {
		return "", err
	}
	rel, err := cleanRel(args["path"])
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, outputDir+"/") {
		return "", invalid("%s holds compile output and is read-only", outputDir)
	}
	p := m.abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	_, existed := m.snap.Hashes[rel]
	if err := os.WriteFile(p, []byte(args["content"]), 0o644); err != nil {
		return "", err
	}
	verb := "Created"
	if existed {
		verb = "Wrote"
	}
	return fmt.Sprintf("%s %s (%s). Not pushed yet.", verb, rel, humanSize(int64(len(args["content"])))), nil
}

func (s *server) edit(ctx context.Context, args map[string]string) (string, error) {
	m, err := s.mirrorOf(ctx, args["project"])
	if err != nil {
		return "", err
	}
	rel, err := cleanRel(args["path"])
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, outputDir+"/") {
		return "", invalid("%s holds compile output and is read-only", outputDir)
	}
	p := m.abs(rel)
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", fail("NOT_FOUND", "use overleaf_write to create it.", "no file %s", rel)
	}
	if err != nil {
		return "", err
	}
	text, old := string(data), args["old"]
	if n := strings.Count(text, old); n != 1 {
		hint := "copy the text exactly, including whitespace."
		if n > 1 {
			hint = "include more surrounding text so it matches once."
		}
		return "", fail("INVALID_ARG", hint, "old text found %d times in %s, need exactly 1", n, rel)
	}
	text = strings.Replace(text, old, args["new"], 1)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		return "", err
	}
	line := strings.Count(string(data)[:strings.Index(string(data), old)], "\n") + 1
	return fmt.Sprintf("Edited %s at line %d. Not pushed yet.", rel, line), nil
}

func (s *server) status(ctx context.Context, args map[string]string) (string, error) {
	m, err := s.mirrorOf(ctx, args["project"])
	if err != nil {
		return "", err
	}
	ch, err := m.changes()
	if err != nil {
		return "", err
	}
	if ch.empty() {
		return fmt.Sprintf("No local changes in %s (synced %s).", m.dir, m.snap.Synced), nil
	}
	var b strings.Builder
	for _, x := range []struct {
		tag   string
		paths []string
	}{{"M", ch.Modified}, {"A", ch.Added}, {"D", ch.Deleted}} {
		for _, p := range x.paths {
			fmt.Fprintf(&b, "%s %s\n", x.tag, p)
		}
	}
	return b.String(), nil
}

// ---- sync ----

func (s *server) push(ctx context.Context, args map[string]string) (string, error) {
	force, err := flag("force", args["force"])
	if err != nil {
		return "", err
	}
	m, err := findMirror(s.root, args["project"])
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", fail("NOT_FOUND", "overleaf_pull it first.", "%q isn't mirrored, so there's nothing to push", args["project"])
	}
	ch, err := m.changes()
	if err != nil {
		return "", err
	}
	todo := ch.unpushed()
	if only := args["paths"]; only != "" {
		pending := map[string]bool{}
		for _, p := range todo {
			pending[p] = true
		}
		todo = nil
		for _, raw := range strings.Split(only, ",") {
			rel, err := cleanRel(raw)
			if err != nil {
				return "", err
			}
			if !pending[rel] {
				return "", invalid("%s has no unpushed changes", rel)
			}
			todo = append(todo, rel)
		}
	}
	if len(todo) == 0 {
		return "Nothing to push.", nil
	}
	c, _, err := s.client(m.snap.Account)
	if err != nil {
		return "", err
	}
	id := m.snap.ProjectID
	if !force {
		remote, err := c.download(ctx, id)
		if err != nil {
			return "", err
		}
		var conflicts []string
		for _, p := range todo {
			data, onRemote := remote[p]
			old, known := m.snap.Hashes[p]
			if onRemote != known || onRemote && hash(data) != old {
				conflicts = append(conflicts, p)
			}
		}
		if len(conflicts) > 0 {
			var saved []string
			for _, p := range conflicts {
				if data, ok := remote[p]; ok {
					rp := m.abs(remoteDir + "/" + p)
					if os.MkdirAll(filepath.Dir(rp), 0o755) == nil && os.WriteFile(rp, data, 0o644) == nil {
						saved = append(saved, remoteDir+"/"+p)
					}
				}
			}
			hint := "push with force=\"true\" to overwrite them."
			if len(saved) > 0 {
				hint = "the Overleaf versions are in " + strings.Join(saved, ", ") + " (overleaf_read them). Merge into your files, then push with force=\"true\"."
			}
			return "", fail("REMOTE_CHANGED", hint, "changed on Overleaf since the last pull: %s", strings.Join(conflicts, ", "))
		}
	}
	t, err := c.join(ctx, id)
	if err != nil {
		return "", err
	}
	var done []string
	for _, rel := range todo {
		data, err := os.ReadFile(m.abs(rel))
		if err != nil {
			return "", err
		}
		folder, err := ensureFolder(ctx, c, id, t, path.Dir(rel))
		if err != nil {
			return "", pushErr(done, err)
		}
		if err := c.upload(ctx, id, folder, path.Base(rel), data); err != nil {
			return "", pushErr(done, err)
		}
		m.snap.Hashes[rel] = hash(data)
		if err := m.save(); err != nil {
			return "", err
		}
		done = append(done, rel)
	}
	msg := fmt.Sprintf("Pushed %d: %s.", len(done), strings.Join(done, ", "))
	if len(ch.Deleted) > 0 {
		msg += " Deleted locally but not on Overleaf: " + strings.Join(ch.Deleted, ", ") + "."
	}
	return msg, nil
}

func pushErr(done []string, err error) error {
	if len(done) == 0 {
		return err
	}
	return fmt.Errorf("%w (already pushed: %s)", err, strings.Join(done, ", "))
}

// ensureFolder returns the id of a project folder ("." is the root), creating
// missing ones.
func ensureFolder(ctx context.Context, c *client, id string, t tree, dir string) (string, error) {
	if dir == "." {
		dir = ""
	}
	if e, ok := t[dir]; ok {
		if e.Type != "folder" {
			return "", invalid("%s is a file on Overleaf, not a folder", dir)
		}
		return e.ID, nil
	}
	parentDir := path.Dir(dir)
	parent, err := ensureFolder(ctx, c, id, t, parentDir)
	if err != nil {
		return "", err
	}
	fid, err := c.mkdir(ctx, id, parent, path.Base(dir))
	if err != nil {
		return "", err
	}
	t[dir] = entity{fid, "folder"}
	return fid, nil
}

func (s *server) deleteFile(ctx context.Context, args map[string]string) (string, error) {
	m, err := s.mirrorOf(ctx, args["project"])
	if err != nil {
		return "", err
	}
	rel, err := cleanRel(args["path"])
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, outputDir+"/") {
		return "", invalid("%s holds compile output", outputDir)
	}
	c, _, err := s.client(m.snap.Account)
	if err != nil {
		return "", err
	}
	t, err := c.join(ctx, m.snap.ProjectID)
	if err != nil {
		return "", err
	}
	e, onRemote := t[rel]
	_, statErr := os.Stat(m.abs(rel))
	if !onRemote && statErr != nil {
		return "", fail("NOT_FOUND", "overleaf_read without path lists the files.", "no file or folder %s", rel)
	}
	msg := "Deleted " + rel + " on Overleaf and locally."
	if onRemote {
		if err := c.deleteEntity(ctx, m.snap.ProjectID, e); err != nil {
			return "", err
		}
	} else {
		msg = rel + " wasn't on Overleaf; deleted it locally."
	}
	if err := os.RemoveAll(m.abs(rel)); err != nil {
		return "", err
	}
	for p := range m.snap.Hashes {
		if p == rel || strings.HasPrefix(p, rel+"/") {
			delete(m.snap.Hashes, p)
		}
	}
	return msg, m.save()
}

// ---- compile ----

func (s *server) compile(ctx context.Context, args map[string]string) (core.Result, error) {
	open, err := flag("open", args["open"])
	if err != nil {
		return core.Result{}, err
	}
	attach, err := flag("attach", args["attach"])
	if err != nil {
		return core.Result{}, err
	}
	m, err := s.mirrorOf(ctx, args["project"])
	if err != nil {
		return core.Result{}, err
	}
	c, _, err := s.client(m.snap.Account)
	if err != nil {
		return core.Result{}, err
	}
	r, err := c.compile(ctx, m.snap.ProjectID)
	if err != nil {
		return core.Result{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Compile: %s\n", r.Status)
	if ch, err := m.changes(); err == nil {
		if u := ch.unpushed(); len(u) > 0 {
			fmt.Fprintf(&b, "Not pushed (compiled Overleaf's version): %s\n", strings.Join(u, ", "))
		}
	}
	out := filepath.Join(m.dir, outputDir)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return core.Result{}, err
	}
	logData, err := c.output(ctx, r, "output.log")
	if err != nil {
		return core.Result{}, err
	}
	if logData != nil {
		os.WriteFile(filepath.Join(out, "output.log"), logData, 0o644)
		errs, warns := logErrors(string(logData), 10)
		if len(errs) > 0 {
			fmt.Fprintf(&b, "Errors (first %d):\n%s\n", len(errs), strings.Join(errs, "\n"))
		}
		if warns > 0 {
			fmt.Fprintf(&b, "Warnings: %d (log: %s)\n", warns, filepath.Join(out, "output.log"))
		}
	}
	pdf, err := c.output(ctx, r, "output.pdf")
	if err != nil {
		return core.Result{}, err
	}
	res := core.Result{}
	pdfPath := filepath.Join(out, "output.pdf")
	if pdf == nil {
		os.Remove(pdfPath)
		b.WriteString("No PDF produced.\n")
	} else {
		if err := os.WriteFile(pdfPath, pdf, 0o644); err != nil {
			return core.Result{}, err
		}
		fmt.Fprintf(&b, "PDF: %s (%s)\n", pdfPath, humanSize(int64(len(pdf))))
		if open {
			if err := s.open(pdfPath); err != nil {
				fmt.Fprintf(&b, "Couldn't open it: %v\n", err)
			}
		}
		if attach {
			res.Blobs = []core.Blob{blobOf(pdfPath, pdf)}
		}
	}
	b.WriteString("Overleaf: " + c.projectURL(m.snap.ProjectID))
	res.Text = b.String()
	return res, nil
}

// ---- tools ----

func projectArg(what string) core.Arg {
	return core.Arg{Name: "project", Description: "Project name (exact) or id" + what, Required: true}
}

var pathArg = core.Arg{Name: "path", Description: "File path relative to the project root, e.g. chapters/intro.tex", Required: true}

func (s *server) tools() []core.Tool {
	return []core.Tool{
		{
			Name:        "overleaf_login",
			Description: "Save an Overleaf session (the browser's overleaf_session2 cookie) and make it active; with account only, switch accounts; with no args, list accounts.",
			Args: []core.Arg{
				{Name: "cookie", Description: "overleaf_session2 cookie value"},
				{Name: "account", Description: "Account email to switch to"},
			},
			Execute: s.login,
		},
		{
			Name:        "overleaf_logout",
			Description: "Forget an Overleaf account's session.",
			Args:        []core.Arg{{Name: "account", Description: "Account email; the active one if omitted"}},
			Execute:     s.logout,
		},
		{
			Name:        "overleaf_projects",
			Description: "List Overleaf projects of the active account: name, id, last update, whether mirrored locally.",
			Execute:     s.projects,
		},
		{
			Name:        "overleaf_create",
			Description: "Create a blank Overleaf project and mirror it locally.",
			Args:        []core.Arg{{Name: "name", Description: "Project name", Required: true}},
			Execute:     s.create,
		},
		{
			Name:        "overleaf_pull",
			Description: "Download a project into its local mirror (file tools pull automatically the first time). Refuses to drop unpushed edits unless force=\"true\".",
			Args:        []core.Arg{projectArg(""), {Name: "force", Description: "\"true\" to discard unpushed local edits"}},
			Execute:     s.pull,
		},
		{
			Name:        "overleaf_read",
			Description: "Read a file of a project's local mirror (images and PDFs as content), or list its files with change marks when path is omitted.",
			Args: []core.Arg{
				projectArg(""),
				{Name: "path", Description: "File path relative to the project root; omit to list files"},
				{Name: "lines", Description: "Line range, e.g. \"40-80\""},
			},
			Run: s.read,
		},
		{
			Name:        "overleaf_write",
			Description: "Create or overwrite a text file in a project's local mirror. Push to send it to Overleaf.",
			Args:        []core.Arg{projectArg(""), pathArg, {Name: "content", Description: "Full file content", Required: true}},
			Execute:     s.write,
		},
		{
			Name:        "overleaf_edit",
			Description: "Replace one exact occurrence of text in a mirrored file. Push to send it to Overleaf.",
			Args: []core.Arg{
				projectArg(""), pathArg,
				{Name: "old", Description: "Exact text to replace; must occur once", Required: true},
				{Name: "new", Description: "Replacement text (empty deletes)"},
			},
			Execute: s.edit,
		},
		{
			Name:        "overleaf_status",
			Description: "Show local changes since the last pull/push: M modified, A added, D deleted.",
			Args:        []core.Arg{projectArg("")},
			Execute:     s.status,
		},
		{
			Name:        "overleaf_push",
			Description: "Upload changed and new mirrored files to Overleaf. Stops with REMOTE_CHANGED if they changed there since the pull, unless force=\"true\".",
			Args: []core.Arg{
				projectArg(""),
				{Name: "paths", Description: "Comma-separated paths to push; all changes if omitted"},
				{Name: "force", Description: "\"true\" to overwrite remote changes"},
			},
			Execute: s.push,
		},
		{
			Name:        "overleaf_delete_file",
			Description: "Delete a file or folder on Overleaf and in the local mirror.",
			Args:        []core.Arg{projectArg(""), pathArg},
			Execute:     s.deleteFile,
		},
		{
			Name:        "overleaf_compile",
			Description: "Compile the project on Overleaf (its pushed version) and return the status, first errors and the saved PDF path.",
			Args: []core.Arg{
				projectArg(""),
				{Name: "open", Description: "\"true\" to open the PDF in the Windows viewer"},
				{Name: "attach", Description: "\"true\" to return the PDF in the result"},
			},
			Run: s.compile,
		},
	}
}

func main() {
	core.Serve("overleaf", "0.1.0", newServer().tools()...)
}
