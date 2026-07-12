package pagination

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// StrongETag returns a deterministic representation tag. Length-prefixing each
// value prevents ambiguous concatenations.
func StrongETag(parts ...any) string {
	digest := sha256.New()
	for _, part := range parts {
		value := fmt.Sprint(part)
		_, _ = fmt.Fprintf(digest, "%d:", len(value))
		_, _ = digest.Write([]byte(value))
	}
	return `"` + hex.EncodeToString(digest.Sum(nil)) + `"`
}

// SetConditionalHeaders writes representation metadata before status/body.
func SetConditionalHeaders(header http.Header, etag string, modified time.Time) {
	if strings.TrimSpace(etag) != "" {
		header.Set("ETag", etag)
	}
	if !modified.IsZero() {
		header.Set("Last-Modified", modified.UTC().Truncate(time.Second).Format(http.TimeFormat))
	}
}

// NotModified applies RFC 9110 precedence: If-None-Match wins over
// If-Modified-Since. Weak comparison is used for GET/HEAD cache validation.
func NotModified(r *http.Request, etag string, modified time.Time) bool {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	if raw := r.Header.Get("If-None-Match"); raw != "" {
		for _, candidate := range strings.Split(raw, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || (etag != "" && weakETag(candidate) == weakETag(etag)) {
				return true
			}
		}
		return false
	}
	if raw := r.Header.Get("If-Modified-Since"); raw != "" && !modified.IsZero() {
		since, err := http.ParseTime(raw)
		return err == nil && !modified.UTC().Truncate(time.Second).After(since.UTC())
	}
	return false
}

func weakETag(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && strings.EqualFold(value[:2], "W/") {
		value = strings.TrimSpace(value[2:])
	}
	return value
}

// WriteNotModified emits representation and rate headers with an empty 304
// response body. It returns false when the request needs a normal response.
func WriteNotModified(w http.ResponseWriter, r *http.Request, etag string, modified time.Time, rate Rate) bool {
	SetConditionalHeaders(w.Header(), etag, modified)
	SetRateHeaders(w.Header(), rate)
	if !NotModified(r, etag, modified) {
		return false
	}
	w.WriteHeader(http.StatusNotModified)
	return true
}

// Rate is the stable compatibility rate metadata emitted by a handler.
type Rate struct {
	Limit     int
	Remaining int
	Used      int
	Reset     time.Time
	Resource  string
}

// SetRateHeaders emits GitHub-compatible rate-limit metadata.
func SetRateHeaders(header http.Header, rate Rate) {
	if rate.Limit > 0 {
		header.Set("X-RateLimit-Limit", strconv.Itoa(rate.Limit))
	}
	if rate.Remaining >= 0 && rate.Limit > 0 {
		header.Set("X-RateLimit-Remaining", strconv.Itoa(rate.Remaining))
	}
	if rate.Used >= 0 && rate.Limit > 0 {
		header.Set("X-RateLimit-Used", strconv.Itoa(rate.Used))
	}
	if !rate.Reset.IsZero() {
		header.Set("X-RateLimit-Reset", strconv.FormatInt(rate.Reset.Unix(), 10))
	}
	if rate.Resource != "" {
		header.Set("X-RateLimit-Resource", rate.Resource)
	}
}

// SetRetryAfter rounds up to whole seconds and always emits at least one.
func SetRetryAfter(header http.Header, delay time.Duration) int {
	seconds := int((delay + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	header.Set("Retry-After", strconv.Itoa(seconds))
	return seconds
}
