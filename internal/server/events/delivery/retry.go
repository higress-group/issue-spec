package delivery

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/server/events/subscriptions"
)

func retryable(status int, err error) bool {
	if err != nil {
		return true
	}
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func successful(status int) bool { return status >= 200 && status < 300 }

func nextRetry(now time.Time, policy subscriptions.RetryPolicy, attempt int, header http.Header) time.Time {
	delay := policy.InitialBackoff
	for index := 1; index < attempt && delay < policy.MaxBackoff; index++ {
		if delay > policy.MaxBackoff/2 {
			delay = policy.MaxBackoff
			break
		}
		delay *= 2
	}
	if retryAfter, ok := parseRetryAfter(now, header.Get("Retry-After")); ok && retryAfter > delay {
		delay = retryAfter
	}
	if delay > policy.MaxBackoff {
		delay = policy.MaxBackoff
	}
	return now.Add(delay)
}

func parseRetryAfter(now time.Time, value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		const maxDuration = time.Duration(1<<63 - 1)
		if seconds > int64(maxDuration/time.Second) {
			return maxDuration, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil || when.Before(now) {
		return 0, false
	}
	return when.Sub(now), true
}
