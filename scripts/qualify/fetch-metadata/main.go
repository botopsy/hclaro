// fetch-metadata fetches release metadata for M27 F-6 (tasks/M27/FEASIBILITY-CHECKLIST.md) and
// nothing else: index pages, SHA256SUMS, signatures, registry JSON and changelogs from the
// sources the owner approved in OD-1a. It never downloads a package:
//   - before a request: only https URLs on the allowlist (host + path prefix); archive and
//     binary extensions are refused;
//   - redirects: followed only to another allowlisted URL, otherwise refused;
//   - response: a non-text Content-Type or a body over 5 MiB is refused and discarded.
//
// Owner-approved exceptions (2026-11-14), each with the same size limit, extension ban and log:
//   - E1: application/octet-stream is accepted for SHA256SUMS signature files (*_SHA256SUMS.sig,
//     *_SHA256SUMS.<KEYID>.sig, *_SHA256SUMS.gpgsig) up to 64 KiB, and for *_SHA256SUMS files
//     up to 64 KiB whose every line is "<sha256>  <name>";
//   - E2: a redirect from a GitHub release download in an allowlisted repo to
//     release-assets.githubusercontent.com is followed, only for SHA256SUMS or signature files;
//   - E4: github.com/opentofu/terraform-provider-{random,local} release downloads (OpenTofu's
//     rebuilds, which registry.opentofu.org serves), only for SHA256SUMS and signature files,
//     with E1 and E2 applying;
//   - E3: application/octet-stream from registry.opentofu.org provider metadata is accepted
//     when the body is valid JSON.
//
// Every attempt, refused ones included, is appended as one JSON line to <out>/fetch-log.jsonl;
// bodies go to <out>/bodies/<sha256>. No credentials, cookies or identifying headers are sent.
//
// Usage: go run ./scripts/qualify/fetch-metadata -out <dir outside the repo> <url>...
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxBody = 5 << 20

type rule struct{ source, host, prefix string }

// OD-1a sources (approved 2026-11-14). Provider SHA256SUMS on releases.hashicorp.com are the
// shasums_url that registry.terraform.io's download metadata names.
var od1a = []rule{
	{"hashicorp-releases", "releases.hashicorp.com", "/terraform/"},
	{"hashicorp-releases", "releases.hashicorp.com", "/terraform-provider-random/"},
	{"hashicorp-releases", "releases.hashicorp.com", "/terraform-provider-local/"},
	{"opentofu-github", "github.com", "/opentofu/opentofu/releases"},
	{"get-opentofu", "get.opentofu.org", "/"},
	{"registry-terraform", "registry.terraform.io", "/v1/providers/hashicorp/random/"},
	{"registry-terraform", "registry.terraform.io", "/v1/providers/hashicorp/local/"},
	{"registry-terraform", "registry.terraform.io", "/v2/providers/hashicorp/random"},
	{"registry-terraform", "registry.terraform.io", "/v2/providers/hashicorp/local"},
	{"registry-opentofu", "registry.opentofu.org", "/v1/providers/hashicorp/random/"},
	{"registry-opentofu", "registry.opentofu.org", "/v1/providers/hashicorp/local/"},
	{"provider-github", "github.com", "/hashicorp/terraform-provider-random/"},
	{"provider-github", "github.com", "/hashicorp/terraform-provider-local/"},
	{"opentofu-provider-github", "github.com", "/opentofu/terraform-provider-random/releases/download/"}, // E4
	{"opentofu-provider-github", "github.com", "/opentofu/terraform-provider-local/releases/download/"},  // E4
	{"key-docs", "www.hashicorp.com", "/.well-known/pgp-key.txt"},
	{"key-docs", "www.hashicorp.com", "/security"},
}

var (
	sumsFile = regexp.MustCompile(`_SHA256SUMS$`)
	sumsLine = regexp.MustCompile(`^[0-9a-f]{64}  [^\s/]+$`)
	sigFile  = regexp.MustCompile(`_SHA256SUMS\.([0-9A-Fa-f]{8}\.)?sig$|_SHA256SUMS\.gpgsig$`)
)

const (
	maxSig      = 64 << 10
	assetHost   = "release-assets.githubusercontent.com" // E2
	tofuRegHost = "registry.opentofu.org"                // E3
)

var refusedExt = []string{".zip", ".tar.gz", ".tgz", ".exe", ".deb", ".rpm", ".apk", ".pkg", ".msi", ".dmg", ".gz", ".xz", ".bz2"}

type entry struct {
	URL           string `json:"url"`
	Source        string `json:"source,omitempty"`
	Time          string `json:"time"`
	Result        string `json:"result"` // fetched | refused:<reason> | http:<status> | error:<text>
	FinalURL      string `json:"finalUrl,omitempty"`
	ContentType   string `json:"contentType,omitempty"`
	ContentLength int64  `json:"contentLength,omitempty"`
	ETag          string `json:"etag,omitempty"`
	LastModified  string `json:"lastModified,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	Body          string `json:"body,omitempty"`
}

type fetcher struct {
	allow  []rule
	client *http.Client
	out    string
	log    io.Writer
}

func main() {
	out := flag.String("out", "", "evidence directory (outside the repository)")
	flag.Parse()
	if *out == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: fetch-metadata -out <dir> <url>...")
		os.Exit(2)
	}
	if err := os.MkdirAll(filepath.Join(*out, "bodies"), 0o700); err != nil {
		fail(err)
	}
	logf, err := os.OpenFile(filepath.Join(*out, "fetch-log.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail(err)
	}
	defer logf.Close()
	f := newFetcher(od1a, &http.Client{Timeout: 30 * time.Second}, *out, logf)
	for _, u := range flag.Args() {
		e := f.fetch(u)
		fmt.Printf("%s\t%s\t%s\n", e.Result, e.Body, u)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func newFetcher(allow []rule, c *http.Client, out string, log io.Writer) *fetcher {
	f := &fetcher{allow: allow, out: out, log: log}
	cc := *c
	cc.Jar = nil
	cc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("refused:redirects")
		}
		if releaseAssetRedirect(via[0].URL, req.URL) {
			return nil
		}
		if _, reason := f.check(req.URL); reason != "" {
			return errors.New("refused:redirect " + reason + " " + req.URL.Host)
		}
		return nil
	}
	f.client = &cc
	return f
}

// check returns the source name, or a refusal reason.
func (f *fetcher) check(u *url.URL) (string, string) {
	if u.Scheme != "https" || u.User != nil {
		return "", "scheme"
	}
	p := strings.ToLower(u.Path)
	for _, ext := range refusedExt {
		if strings.HasSuffix(p, ext) {
			return "", "archive"
		}
	}
	for _, r := range f.allow {
		if strings.EqualFold(u.Host, r.host) && strings.HasPrefix(u.Path, r.prefix) {
			if r.source == "opentofu-provider-github" && !sumsFile.MatchString(path.Base(u.Path)) && !sigFile.MatchString(path.Base(u.Path)) {
				return "", "file" // E4 covers checksum and signature files only
			}
			return r.source, ""
		}
	}
	return "", "host"
}

// releaseAssetRedirect is exception E2: an allowlisted GitHub release download of a SHA256SUMS
// or signature file may redirect to GitHub's release-asset host, and nowhere else.
func releaseAssetRedirect(orig, to *url.URL) bool {
	if to.Scheme != "https" || !strings.EqualFold(to.Host, assetHost) || !strings.EqualFold(orig.Host, "github.com") {
		return false
	}
	name := path.Base(orig.Path)
	if !sumsFile.MatchString(name) && !sigFile.MatchString(name) {
		return false
	}
	for _, prefix := range []string{"/opentofu/opentofu/releases/download/", "/hashicorp/terraform-provider-random/releases/download/", "/hashicorp/terraform-provider-local/releases/download/",
		"/opentofu/terraform-provider-random/releases/download/", "/opentofu/terraform-provider-local/releases/download/"} {
		if strings.HasPrefix(orig.Path, prefix) {
			return true
		}
	}
	return false
}

// octetAllowed reports exceptions E1 and E3 for an application/octet-stream body.
func octetAllowed(orig *url.URL, body []byte) bool {
	name := path.Base(orig.Path)
	if sigFile.MatchString(name) {
		return len(body) <= maxSig // E1
	}
	if sumsFile.MatchString(name) {
		return len(body) <= maxSig && sumsText(body) // E1 extension
	}
	return strings.EqualFold(orig.Host, tofuRegHost) && json.Valid(body) // E3
}

func sumsText(body []byte) bool {
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	for _, l := range lines {
		if !sumsLine.MatchString(l) {
			return false
		}
	}
	return len(lines) > 0
}

func isOctet(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "application/octet-stream"
}

func textType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mt, "text/") || mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		mt == "application/pgp-signature" || mt == "application/pgp-keys"
}

func (f *fetcher) fetch(raw string) entry {
	e := entry{URL: raw, Time: time.Now().UTC().Format(time.RFC3339)}
	defer func() {
		line, _ := json.Marshal(e)
		f.log.Write(append(line, '\n'))
	}()
	u, err := url.Parse(raw)
	if err != nil {
		e.Result = "refused:url"
		return e
	}
	source, reason := f.check(u)
	if reason != "" {
		e.Result = "refused:" + reason
		return e
	}
	e.Source = source
	req, _ := http.NewRequest(http.MethodGet, raw, nil)
	req.Header.Set("User-Agent", "metadata-fetch/1")
	resp, err := f.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && strings.HasPrefix(ue.Err.Error(), "refused:") {
			e.Result = ue.Err.Error()
		} else {
			e.Result = "error:" + err.Error()
		}
		return e
	}
	defer resp.Body.Close()
	e.FinalURL = resp.Request.URL.String()
	e.ContentType = resp.Header.Get("Content-Type")
	e.ContentLength = resp.ContentLength
	e.ETag = resp.Header.Get("ETag")
	e.LastModified = resp.Header.Get("Last-Modified")
	if resp.StatusCode != http.StatusOK {
		e.Result = fmt.Sprintf("http:%d", resp.StatusCode)
		return e
	}
	octet := isOctet(e.ContentType)
	if !textType(e.ContentType) && !octet {
		e.Result = "refused:content-type"
		return e
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		e.Result = "error:" + err.Error()
		return e
	}
	if len(body) > maxBody {
		e.Result = "refused:size"
		return e
	}
	if octet && !textType(e.ContentType) && !octetAllowed(u, body) {
		e.Result = "refused:content-type"
		return e
	}
	sum := sha256.Sum256(body)
	e.SHA256 = hex.EncodeToString(sum[:])
	e.Body = filepath.Join("bodies", e.SHA256)
	if err := os.WriteFile(filepath.Join(f.out, e.Body), body, 0o600); err != nil {
		e.Result, e.Body = "error:"+err.Error(), ""
		return e
	}
	e.Result = "fetched"
	return e
}
