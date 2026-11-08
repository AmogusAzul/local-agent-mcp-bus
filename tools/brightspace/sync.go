package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const manifestName = ".brightspace-sync.json"

type manifestEntry struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

// job is a background course download.
type job struct {
	id, course, dest string
	done             chan struct{}

	mu       sync.Mutex
	total    int
	finished int // downloaded, unchanged, skipped or failed
	fetched  int
	same     int
	skipped  int
	failed   int
	bytes    int64
	failures []string
	err      error
}

func (j *job) summary(running bool) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	var b strings.Builder
	if running {
		fmt.Fprintf(&b, "JOB %s running: %d/%d files, %s (call brightspace_sync with job=%s to follow)", j.id, j.finished, j.total, size(j.bytes), j.id)
		return b.String()
	}
	if j.err != nil {
		return j.err.Error()
	}
	fmt.Fprintf(&b, "JOB %s done: %d files for %s in %s: %d downloaded (%s), %d unchanged", j.id, j.total, j.course, j.dest, j.fetched, size(j.bytes), j.same)
	if j.skipped > 0 {
		fmt.Fprintf(&b, ", %d skipped by filters or size", j.skipped)
	}
	if j.failed > 0 {
		fmt.Fprintf(&b, ", %d failed", j.failed)
	}
	for i, f := range j.failures {
		if i == 5 {
			b.WriteString("\n…")
			break
		}
		b.WriteString("\n" + f)
	}
	return b.String()
}

type jobs struct {
	mu   sync.Mutex
	n    int
	byID map[string]*job
}

func (js *jobs) add(j *job) {
	js.mu.Lock()
	defer js.mu.Unlock()
	js.n++
	j.id = "sync-" + strconv.Itoa(js.n)
	if js.byID == nil {
		js.byID = map[string]*job{}
	}
	js.byID[j.id] = j
}

func (js *jobs) get(id string) *job {
	js.mu.Lock()
	defer js.mu.Unlock()
	return js.byID[id]
}

// wait gives a job up to d to finish, then reports its state.
func (a *api) waitJob(ctx context.Context, j *job, d time.Duration) string {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-j.done:
		return j.summary(false)
	case <-timer.C:
	case <-ctx.Done():
	}
	return j.summary(true)
}

func (a *api) toolSync(ctx context.Context, args map[string]string) (string, error) {
	if id := args["job"]; id != "" {
		j := a.jobs.get(id)
		if j == nil {
			return "", fail("NOT_FOUND", "start one by calling brightspace_sync with a course.", "No job %q (jobs end when the server restarts)", id)
		}
		return a.waitJob(ctx, j, a.jobWait), nil
	}
	if args["course"] == "" {
		return "", invalid("pass course to start a sync, or job to follow one")
	}
	c, err := a.resolveCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	var maxBytes int64
	if v := args["max_mb"]; v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return "", invalid("max_mb must be a whole number of megabytes")
		}
		maxBytes = n << 20
	}
	types := map[string]bool{}
	for _, t := range strings.Split(args["types"], ",") {
		if t = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(t), ".")); t != "" {
			types[t] = true
		}
	}
	dest := args["dest"]
	if dest == "" {
		dest = defaultDest(c)
	}
	ms, err := a.toc(ctx, c.ID)
	if err != nil {
		return "", err
	}
	var flat []flatTopic
	flatten(ms, nil, &flat)

	modFilter := strings.ToLower(strings.TrimSpace(args["module"]))
	var work []flatTopic
	skipped := 0
	for _, t := range flat {
		if t.TypeIdentifier != "File" {
			continue
		}
		if modFilter != "" && !strings.Contains(strings.ToLower(strings.Join(t.path, "/")), modFilter) {
			continue
		}
		if len(types) > 0 && !types[strings.TrimPrefix(strings.ToLower(filepath.Ext(t.URL)), ".")] {
			skipped++
			continue
		}
		work = append(work, t)
	}
	j := &job{course: c.String(), dest: dest, done: make(chan struct{}), total: len(work) + skipped, skipped: skipped, finished: skipped}
	a.jobs.add(j)
	go a.runSync(j, c, work, dest, maxBytes)
	return a.waitJob(ctx, j, a.jobWait), nil
}

func (a *api) runSync(j *job, c course, work []flatTopic, dest string, maxBytes int64) {
	defer close(j.done)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		j.mu.Lock()
		j.err = fail("API_ERROR", "", "Couldn't create %s: %v", dest, err)
		j.mu.Unlock()
		return
	}
	manPath := filepath.Join(dest, manifestName)
	man := map[string]manifestEntry{}
	if b, err := os.ReadFile(manPath); err == nil {
		json.Unmarshal(b, &man)
	}
	var manMu sync.Mutex
	save := func() {
		manMu.Lock()
		defer manMu.Unlock()
		if b, err := json.MarshalIndent(man, "", " "); err == nil {
			os.WriteFile(manPath, b, 0o644)
		}
	}

	ctx := context.Background()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, t := range work {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			key := strconv.FormatInt(t.ID, 10)
			dir := dest
			for _, p := range t.path {
				dir = filepath.Join(dir, safeName(p))
			}
			manMu.Lock()
			prev, ok := man[key]
			manMu.Unlock()
			if ok && prev.Modified == t.LastModifiedDate {
				if st, err := os.Stat(prev.Path); err == nil && st.Size() == prev.Size {
					j.mu.Lock()
					j.same++
					j.finished++
					j.mu.Unlock()
					return
				}
			}
			fail1 := func(err error) {
				j.mu.Lock()
				j.failed++
				j.finished++
				j.failures = append(j.failures, fmt.Sprintf("%s: %v", t.Title, err))
				j.mu.Unlock()
			}
			resp, err := a.topicFile(ctx, c.ID, t.ID)
			if err != nil {
				fail1(err)
				return
			}
			defer resp.Body.Close()
			if maxBytes > 0 && resp.ContentLength > maxBytes {
				j.mu.Lock()
				j.skipped++
				j.finished++
				j.failures = append(j.failures, fmt.Sprintf("%s: %s is over the size limit", t.Title, size(resp.ContentLength)))
				j.mu.Unlock()
				return
			}
			path, n, err := saveTo(dir, remoteName(resp, t.Title), resp.Body)
			if err != nil {
				fail1(err)
				return
			}
			manMu.Lock()
			man[key] = manifestEntry{Path: path, Size: n, Modified: t.LastModifiedDate}
			manMu.Unlock()
			j.mu.Lock()
			j.fetched++
			j.finished++
			j.bytes += n
			every := j.fetched%20 == 0
			j.mu.Unlock()
			if every {
				save()
			}
		}()
	}
	wg.Wait()
	save()
}
