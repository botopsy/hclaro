// Package update tells the UI that a newer Hclaro release exists (M35). It makes one bounded,
// unauthenticated GET of the public release metadata, sends no user, project or machine data,
// and never fails the caller: any problem reads as "no update known".
package update

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LatestURL is the public API for the latest published (not draft, not pre-release) release.
const LatestURL = "https://api.github.com/repos/botopsy/hclaro/releases/latest"

const (
	releasePrefix = "https://github.com/botopsy/hclaro/releases/"
	maxBody       = 256 << 10
	timeout       = 5 * time.Second
)

// Status is what the UI is told.
type Status struct {
	Enabled   bool      `json:"enabled"`
	Current   string    `json:"current,omitempty"`
	Latest    string    `json:"latest,omitempty"`
	Available bool      `json:"available"`
	URL       string    `json:"url,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitempty"`
}

// Checker caches one lookup for Every. The zero Every means 24 hours.
type Checker struct {
	Current string
	URL     string
	Client  *http.Client
	Every   time.Duration

	mu     sync.Mutex
	last   Status
	loaded bool
	at     time.Time
}

// Status returns the cached result, looking again when it is older than Every. Concurrent
// callers wait on the same lookup (the mutex is held across it, bounded by the timeout).
func (c *Checker) Status(ctx context.Context) Status {
	if c == nil {
		return Status{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	every := c.Every
	if every == 0 {
		every = 24 * time.Hour
	}
	if c.loaded && time.Since(c.at) < every {
		return c.last
	}
	c.last, c.loaded, c.at = c.lookup(ctx), true, time.Now()
	return c.last
}

func (c *Checker) lookup(ctx context.Context) Status {
	s := Status{Enabled: true, Current: c.Current, CheckedAt: time.Now().UTC()}
	if _, ok := parse(c.Current); !ok {
		return s // a dev build is never "behind"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := c.URL
	if url == "" {
		url = LatestURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return s
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hclaro/"+c.Current)
	client := c.Client
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return s
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		URL        string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&rel) != nil || rel.Draft || rel.Prerelease {
		return s
	}
	latest, ok := parse(rel.Tag)
	cur, _ := parse(c.Current)
	if !ok || !less(cur, latest) {
		return s
	}
	s.Latest, s.Available = strings.TrimPrefix(rel.Tag, "v"), true
	if strings.HasPrefix(rel.URL, releasePrefix) {
		s.URL = rel.URL
	}
	return s
}

type semver struct {
	n   [3]int
	pre string
}

// parse reads [v]MAJOR.MINOR.PATCH[-pre]; build metadata is ignored.
func parse(v string) (semver, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "+")
	v, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	out := semver{pre: pre}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return semver{}, false
		}
		out.n[i] = n
	}
	return out, true
}

// less reports a < b. A pre-release sorts before its release; two pre-releases compare as
// text, enough for Hclaro's own tags (shortcut: not full semver precedence).
func less(a, b semver) bool {
	for i := range a.n {
		if a.n[i] != b.n[i] {
			return a.n[i] < b.n[i]
		}
	}
	switch {
	case a.pre == b.pre:
		return false
	case a.pre == "":
		return false
	case b.pre == "":
		return true
	}
	return a.pre < b.pre
}
