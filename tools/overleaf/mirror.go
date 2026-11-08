package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	snapName  = ".overleaf.json"
	outputDir = ".overleaf-output"
)

// snapshot is a mirror's .overleaf.json: whose project it is and the hash of
// each file as of the last pull or push.
type snapshot struct {
	Account   string            `json:"account"`
	ProjectID string            `json:"project_id"`
	Name      string            `json:"name"`
	Synced    string            `json:"synced"`
	Hashes    map[string]string `json:"hashes"`
}

type mirror struct {
	dir  string
	snap *snapshot
}

func hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "project"
	}
	return s
}

func mirrorDir(root, name, id string) string {
	suffix := id
	if len(id) > 6 {
		suffix = id[len(id)-6:]
	}
	return filepath.Join(root, slug(name)+"-"+suffix)
}

func (m *mirror) save() error {
	m.snap.Synced = time.Now().UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(m.snap, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.dir, snapName), b, 0o600)
}

// mirrors lists every mirror under root.
func mirrors(root string) []*mirror {
	entries, _ := os.ReadDir(root)
	var out []*mirror
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		b, err := os.ReadFile(filepath.Join(dir, snapName))
		if err != nil {
			continue
		}
		var s snapshot
		if json.Unmarshal(b, &s) != nil || s.ProjectID == "" {
			continue
		}
		if s.Hashes == nil {
			s.Hashes = map[string]string{}
		}
		out = append(out, &mirror{dir, &s})
	}
	return out
}

// findMirror finds the mirror of a project by id or exact name (any case). It
// returns nil, nil when there's none.
func findMirror(root, ref string) (*mirror, error) {
	var hits []*mirror
	for _, m := range mirrors(root) {
		if m.snap.ProjectID == ref {
			return m, nil
		}
		if strings.EqualFold(m.snap.Name, ref) {
			hits = append(hits, m)
		}
	}
	switch len(hits) {
	case 0:
		return nil, nil
	case 1:
		return hits[0], nil
	}
	var ids []string
	for _, m := range hits {
		ids = append(ids, m.snap.ProjectID)
	}
	return nil, fail("AMBIGUOUS", "pass the project id.", "%d mirrored projects are named %q: %s", len(hits), ref, strings.Join(ids, ", "))
}

// skipped reports paths that belong to the tool, not the project.
func skipped(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	return strings.HasPrefix(first, ".overleaf") || first == ".git"
}

// files returns the mirror's project files: /-separated path → content hash.
func (m *mirror) files() (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(m.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(m.dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if skipped(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = hash(b)
		return nil
	})
	return out, err
}

type changes struct {
	Modified, Added, Deleted []string
}

func (c changes) empty() bool { return len(c.Modified)+len(c.Added)+len(c.Deleted) == 0 }

func (c changes) unpushed() []string { return append(append([]string{}, c.Modified...), c.Added...) }

func diff(local, snap map[string]string) changes {
	var c changes
	for p, h := range local {
		if old, ok := snap[p]; !ok {
			c.Added = append(c.Added, p)
		} else if old != h {
			c.Modified = append(c.Modified, p)
		}
	}
	for p := range snap {
		if _, ok := local[p]; !ok {
			c.Deleted = append(c.Deleted, p)
		}
	}
	sort.Strings(c.Modified)
	sort.Strings(c.Added)
	sort.Strings(c.Deleted)
	return c
}

func (m *mirror) changes() (changes, error) {
	local, err := m.files()
	if err != nil {
		return changes{}, err
	}
	return diff(local, m.snap.Hashes), nil
}

// cleanRel validates a project-relative path and returns it /-separated.
func cleanRel(rel string) (string, error) {
	r := strings.ReplaceAll(strings.TrimSpace(rel), "\\", "/")
	if r == "" {
		return "", invalid("path is required")
	}
	if strings.HasPrefix(r, "/") || filepath.VolumeName(r) != "" || strings.Contains(r, ":") {
		return "", invalid("path %q must be relative to the project", rel)
	}
	r = path.Clean(r)
	if r == "." || r == ".." || strings.HasPrefix(r, "../") {
		return "", invalid("path %q leaves the project", rel)
	}
	if skipped(r) && !strings.HasPrefix(r, outputDir+"/") {
		return "", invalid("path %q is reserved", rel)
	}
	return r, nil
}

func (m *mirror) abs(rel string) string {
	return filepath.Join(m.dir, filepath.FromSlash(rel))
}

// write replaces the mirror's project files with files (a fresh download) and
// records them as synced.
func (m *mirror) write(files map[string][]byte) error {
	for rel := range m.snap.Hashes {
		if _, ok := files[rel]; !ok {
			if err := os.Remove(m.abs(rel)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	hashes := map[string]string{}
	for rel, data := range files {
		r, err := cleanRel(rel)
		if err != nil || skipped(r) {
			continue
		}
		p := m.abs(r)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
		hashes[r] = hash(data)
	}
	m.snap.Hashes = hashes
	return m.save()
}
