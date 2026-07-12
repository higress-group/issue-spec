package pagination

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/server/publicurl"
)

func TestParseDefaultsBoundsAndSince(t *testing.T) {
	options, err := Parse(url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if options.Page != 1 || options.PerPage != 30 || options.Since != nil {
		t.Fatalf("defaults = %+v", options)
	}
	options, err = Parse(url.Values{"page": {"2"}, "per_page": {"100"}, "since": {"2026-07-03T10:00:00+08:00"}})
	if err != nil {
		t.Fatal(err)
	}
	if options.Page != 2 || options.PerPage != 100 || options.Since.Format(time.RFC3339) != "2026-07-03T02:00:00Z" {
		t.Fatalf("options = %+v", options)
	}
	for name, values := range map[string]url.Values{
		"zero page":     {"page": {"0"}},
		"too large":     {"per_page": {"101"}},
		"ambiguous":     {"page": {"1", "2"}},
		"invalid since": {"since": {"yesterday"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(values); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestBuildLinkHeaderPreservesFiltersAndCanonicalOrigin(t *testing.T) {
	origin, err := publicurl.ParseOrigin("api", "https://api.example.test")
	if err != nil {
		t.Fatal(err)
	}
	filters := url.Values{"state": {"open"}, "labels": {"bug", "p0"}, "page": {"evil"}, "per_page": {"999"}}
	got, err := BuildLinkHeader(origin, "/repos/o/r/issues", filters, 2, 25, 76)
	if err != nil {
		t.Fatal(err)
	}
	want := `<https://api.example.test/repos/o/r/issues?labels=bug&labels=p0&page=1&per_page=25&state=open>; rel="first", ` +
		`<https://api.example.test/repos/o/r/issues?labels=bug&labels=p0&page=1&per_page=25&state=open>; rel="prev", ` +
		`<https://api.example.test/repos/o/r/issues?labels=bug&labels=p0&page=3&per_page=25&state=open>; rel="next", ` +
		`<https://api.example.test/repos/o/r/issues?labels=bug&labels=p0&page=4&per_page=25&state=open>; rel="last"`
	if got != want {
		t.Fatalf("Link:\n got %s\nwant %s", got, want)
	}
	if filters.Get("page") != "evil" {
		t.Fatal("input filters were mutated")
	}
}

func TestConditionalResponseHeadersAndBodyless304(t *testing.T) {
	modified := time.Date(2026, 7, 3, 10, 0, 0, 987, time.UTC)
	etag := StrongETag("issue", 7, 4)
	req := httptest.NewRequest(http.MethodGet, "/repos/o/r/issues/7", nil)
	req.Header.Set("If-None-Match", "W/"+etag)
	res := httptest.NewRecorder()
	rate := Rate{Limit: 5000, Remaining: 4999, Used: 1, Reset: modified.Add(time.Hour), Resource: "core"}
	if !WriteNotModified(res, req, etag, modified, rate) {
		t.Fatal("request should be not modified")
	}
	if res.Code != http.StatusNotModified || res.Body.Len() != 0 {
		t.Fatalf("response = %d %q", res.Code, res.Body.String())
	}
	for _, name := range []string{"ETag", "Last-Modified", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if strings.TrimSpace(res.Header().Get(name)) == "" {
			t.Fatalf("missing %s", name)
		}
	}
}

func TestIfNoneMatchPrecedesIfModifiedSince(t *testing.T) {
	modified := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", `"different"`)
	req.Header.Set("If-Modified-Since", modified.Add(time.Hour).Format(http.TimeFormat))
	if NotModified(req, `"current"`, modified) {
		t.Fatal("If-Modified-Since incorrectly overrode If-None-Match")
	}
}

func TestRetryAfterRoundsUp(t *testing.T) {
	header := make(http.Header)
	if seconds := SetRetryAfter(header, 1200*time.Millisecond); seconds != 2 || header.Get("Retry-After") != "2" {
		t.Fatalf("Retry-After = %q (%d)", header.Get("Retry-After"), seconds)
	}
}
