// Package mentions exposes the session-only site-wide mention directory.
package mentions

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	serverstore "github.com/higress-group/issue-spec/internal/server/store"
)

type Directory interface {
	MentionCandidates(context.Context, uuid.UUID, string, int) ([]serverstore.MentionCandidate, error)
}

type Dependencies struct {
	Directory    Directory
	Authenticate adminapi.Authenticate
	WebOrigin    string
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	origin, err := url.Parse(strings.TrimSpace(deps.WebOrigin))
	if deps.Directory == nil || deps.Authenticate == nil || err != nil || origin.Scheme == "" || origin.Host == "" ||
		(origin.Scheme != "http" && origin.Scheme != "https") || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" {
		return routeset.RouteSet{}, errors.New("native mentions: directory, authentication and web origin are required")
	}
	origin.Path = strings.TrimRight(origin.Path, "/")
	h := handlers{directory: deps.Directory, origin: origin.String(), limiter: newLimiter()}
	protected := adminapi.WithRequestID(deps.Authenticate(http.HandlerFunc(h.candidates)))
	set := routeset.RouteSet{Name: "native-mentions", Routes: []routeset.Route{{
		Name: "native.mentions.candidates", Method: http.MethodGet,
		Pattern: "/api/v1/mentions/candidates", Handler: protected,
	}}}
	return set, set.Validate()
}

type handlers struct {
	directory Directory
	origin    string
	limiter   *tokenBucket
}

func (h handlers) candidates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal := adminapi.Principal(r)
	if principal.Kind != serverauth.CredentialSession {
		adminapi.WriteProblem(w, http.StatusForbidden, "browser_session_required", "Browser session required")
		return
	}
	if !h.limiter.Allow(principal.User.ID) {
		w.Header().Set("Retry-After", "1")
		adminapi.WriteProblem(w, http.StatusTooManyRequests, "mention_search_rate_limited", "Mention search rate limit exceeded")
		return
	}
	query := r.URL.Query()
	values, valid := query["q"]
	if !valid || len(query) != 1 || len(values) != 1 {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_query", "Mention prefix is invalid")
		return
	}
	prefix := strings.TrimSpace(values[0])
	if prefix == "" || !utf8.ValidString(prefix) || utf8.RuneCountInString(prefix) > 64 {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_query", "Mention prefix is invalid")
		return
	}
	candidates, err := h.directory.MentionCandidates(r.Context(), principal.User.ID, prefix, serverstore.MaxMentionCandidates)
	if errors.Is(err, serverstore.ErrMentionCallerIneligible) {
		adminapi.WriteProblem(w, http.StatusForbidden, "active_human_required", "Active human account required")
		return
	}
	if err != nil {
		adminapi.WriteProblem(w, http.StatusServiceUnavailable, "mention_search_unavailable", "Mention search is temporarily unavailable")
		return
	}
	type responseCandidate struct {
		Login       string `json:"login"`
		DisplayName string `json:"display_name"`
		AvatarURL   string `json:"avatar_url"`
	}
	response := make([]responseCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		response = append(response, responseCandidate{Login: candidate.Login, DisplayName: candidate.DisplayName,
			AvatarURL: h.origin + "/api/v1/avatars/" + url.PathEscape(candidate.Login)})
	}
	adminapi.WriteJSON(w, http.StatusOK, response)
}

const maxLimiterCallers = 4096

type tokenBucket struct {
	mu      sync.Mutex
	now     func() time.Time
	callers map[uuid.UUID]callerTokens
}

type callerTokens struct {
	tokens float64
	last   time.Time
}

func newLimiter() *tokenBucket {
	return &tokenBucket{now: func() time.Time { return time.Now().UTC() }, callers: make(map[uuid.UUID]callerTokens)}
}

// Allow implements a bounded per-user bucket of 60 requests with one token
// replenished per second (60 requests per minute at steady state).
func (l *tokenBucket) Allow(userID uuid.UUID) bool {
	if l == nil || userID == uuid.Nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	caller, exists := l.callers[userID]
	if !exists {
		if len(l.callers) >= maxLimiterCallers {
			var oldestID uuid.UUID
			var oldest time.Time
			for id, candidate := range l.callers {
				if oldestID == uuid.Nil || candidate.last.Before(oldest) {
					oldestID, oldest = id, candidate.last
				}
			}
			delete(l.callers, oldestID)
		}
		caller = callerTokens{tokens: 60, last: now}
	}
	caller.tokens += now.Sub(caller.last).Seconds()
	if caller.tokens > 60 {
		caller.tokens = 60
	}
	caller.last = now
	allowed := caller.tokens >= 1
	if allowed {
		caller.tokens--
	}
	l.callers[userID] = caller
	return allowed
}
