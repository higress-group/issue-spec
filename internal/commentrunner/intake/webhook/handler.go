package webhook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
)

const (
	Endpoint                 = "/api/v1/runner/webhooks"
	HeaderDeliveryID         = "X-Issue-Spec-Delivery"
	HeaderEventID            = "X-Issue-Spec-Event"
	HeaderEventType          = "X-Issue-Spec-Event-Type"
	HeaderTimestamp          = "X-Issue-Spec-Timestamp"
	HeaderSubscriptionID     = "X-Issue-Spec-Subscription"
	HeaderSchemaVersion      = "X-Issue-Spec-Schema-Version"
	HeaderEnvelopeBodySHA256 = "X-Issue-Spec-Body-Sha256"
	HeaderRequestID          = "X-Request-Id"
)

type Acceptor interface {
	Accept(context.Context, state.WebhookDelivery) (Acceptance, error)
}

type HandlerConfig struct {
	Credentials           *Credentials
	Queue                 Acceptor
	TimestampWindow       time.Duration
	MaxBodyBytes          int64
	MaxRawCommentBytes    int
	MaxConcurrentRequests int
	RetryAfter            time.Duration
	Clock                 func() time.Time
}

type Handler struct {
	config    HandlerConfig
	semaphore chan struct{}
	accepting atomic.Bool
}

func NewHandler(config HandlerConfig) (*Handler, error) {
	if config.Credentials == nil || config.Queue == nil {
		return nil, errors.New("webhook handler: credentials and durable queue are required")
	}
	if config.TimestampWindow <= 0 {
		config.TimestampWindow = 5 * time.Minute
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = 1 << 20
	}
	if config.MaxRawCommentBytes <= 0 {
		config.MaxRawCommentBytes = 256 << 10
	}
	if config.MaxRawCommentBytes > int(config.MaxBodyBytes) {
		return nil, errors.New("webhook handler: raw comment limit exceeds request body limit")
	}
	if config.MaxConcurrentRequests <= 0 {
		config.MaxConcurrentRequests = 32
	}
	if config.RetryAfter <= 0 {
		config.RetryAfter = 5 * time.Second
	}
	if config.Clock == nil {
		config.Clock = func() time.Time { return time.Now().UTC() }
	}
	if config.TimestampWindow > time.Hour || config.MaxBodyBytes > 16<<20 ||
		config.MaxConcurrentRequests > 1024 || config.RetryAfter > time.Hour {
		return nil, errors.New("webhook handler: limits exceed safe bounds")
	}
	handler := &Handler{config: config, semaphore: make(chan struct{}, config.MaxConcurrentRequests)}
	handler.accepting.Store(true)
	return handler, nil
}

func (h *Handler) StopAccepting() { h.accepting.Store(false) }
func (h *Handler) Ready() bool    { return h != nil && h.accepting.Load() }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.NewString()
	w.Header().Set(HeaderRequestID, requestID)
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/livez":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "live", "request_id": requestID})
		return
	case "/readyz":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
			return
		}
		if !h.Ready() {
			writeProblem(w, http.StatusServiceUnavailable, "not_ready", requestID)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "request_id": requestID})
		return
	case Endpoint:
	default:
		writeProblem(w, http.StatusNotFound, "not_found", requestID)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
		return
	}
	if !h.Ready() {
		writeRetry(w, h.config.RetryAfter)
		writeProblem(w, http.StatusServiceUnavailable, "not_ready", requestID)
		return
	}
	select {
	case h.semaphore <- struct{}{}:
		defer func() { <-h.semaphore }()
	default:
		writeRetry(w, h.config.RetryAfter)
		writeProblem(w, http.StatusServiceUnavailable, "busy", requestID)
		return
	}
	if !uniqueSecurityHeaders(r.Header) {
		writeProblem(w, http.StatusBadRequest, "invalid_headers", requestID)
		return
	}
	now := h.config.Clock().UTC()
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(w, http.StatusUnsupportedMediaType, "invalid_content_type", requestID)
		return
	}
	if !h.config.Credentials.Authenticate(r.Header.Get("Authorization"), now) {
		writeProblem(w, http.StatusUnauthorized, "invalid_credential", requestID)
		return
	}
	deliveryID, eventID, ok := validateIdentityHeaders(r.Header, h.config.Credentials.SubscriptionID())
	if !ok {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid_identity", requestID)
		return
	}
	timestamp, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get(HeaderTimestamp)), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusUnauthorized, "invalid_timestamp", requestID)
		return
	}
	requestTime := time.Unix(timestamp, 0).UTC()
	if requestTime.Before(now.Add(-h.config.TimestampWindow)) || requestTime.After(now.Add(h.config.TimestampWindow)) {
		writeProblem(w, http.StatusUnauthorized, "invalid_timestamp", requestID)
		return
	}
	if r.ContentLength > h.config.MaxBodyBytes {
		writeProblem(w, http.StatusRequestEntityTooLarge, "body_too_large", requestID)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.config.MaxBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeProblem(w, http.StatusRequestEntityTooLarge, "body_too_large", requestID)
		} else {
			writeProblem(w, http.StatusBadRequest, "invalid_body", requestID)
		}
		return
	}
	delivery, err := decodeDelivery(body, deliveryID, eventID, h.config.Credentials.SubscriptionID(), now,
		h.config.MaxRawCommentBytes, r.Header)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid_envelope", requestID)
		return
	}
	accepted, err := h.config.Queue.Accept(r.Context(), delivery)
	switch {
	case errors.Is(err, ErrConflict):
		writeProblem(w, http.StatusConflict, "delivery_conflict", requestID)
	case errors.Is(err, ErrCapacity):
		writeRetry(w, h.config.RetryAfter)
		writeProblem(w, http.StatusServiceUnavailable, "queue_full", requestID)
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "persistence_failed", requestID)
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "duplicate": accepted.Duplicate,
			"delivery_id": deliveryID, "request_id": requestID})
	}
}

func uniqueSecurityHeaders(header http.Header) bool {
	names := []string{"Content-Type", "Authorization", HeaderDeliveryID, HeaderEventID, HeaderTimestamp,
		HeaderSubscriptionID, HeaderSchemaVersion, HeaderEventType, HeaderEnvelopeBodySHA256}
	for _, name := range names {
		values := header.Values(name)
		if len(values) > 1 || (len(values) == 1 && strings.TrimSpace(values[0]) == "") {
			return false
		}
	}
	return true
}

func validateIdentityHeaders(header http.Header, subscriptionID string) (string, string, bool) {
	delivery, deliveryErr := uuid.Parse(strings.TrimSpace(header.Get(HeaderDeliveryID)))
	event, eventErr := uuid.Parse(strings.TrimSpace(header.Get(HeaderEventID)))
	if deliveryErr != nil || eventErr != nil || delivery == uuid.Nil || event == uuid.Nil {
		return "", "", false
	}
	if supplied := strings.TrimSpace(header.Get(HeaderSubscriptionID)); supplied != "" {
		parsed, err := uuid.Parse(supplied)
		if err != nil || parsed.String() != subscriptionID {
			return "", "", false
		}
	}
	return delivery.String(), event.String(), true
}

func decodeDelivery(body []byte, deliveryID, eventID, subscriptionID string, receivedAt time.Time,
	maxRawCommentBytes int, header http.Header) (state.WebhookDelivery, error) {
	var envelope outbox.Envelope
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return state.WebhookDelivery{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return state.WebhookDelivery{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return state.WebhookDelivery{}, errors.New("multiple JSON values")
	}
	if envelope.SchemaVersion != outbox.SchemaVersion || envelope.EventID.String() != eventID ||
		envelope.OrganizationID == uuid.Nil || envelope.RepositoryID == uuid.Nil || envelope.ActorUserID == uuid.Nil ||
		envelope.Issue.StableID == uuid.Nil || envelope.Issue.Number <= 0 || envelope.OccurredAt.IsZero() ||
		strings.TrimSpace(envelope.EventKey) == "" || len(envelope.EventKey) > 512 ||
		strings.TrimSpace(envelope.EventType) == "" || len(envelope.EventType) > 128 ||
		strings.TrimSpace(envelope.Action) == "" || len(envelope.Action) > 64 ||
		len(envelope.RawBody) > maxRawCommentBytes || len(envelope.Author.Login) > 128 {
		return state.WebhookDelivery{}, ErrInvalid
	}
	_, action, hasAction := strings.Cut(envelope.EventType, ".")
	if !hasAction || action != envelope.Action {
		return state.WebhookDelivery{}, ErrInvalid
	}
	if strings.HasPrefix(envelope.EventType, "issue_comment.") {
		if envelope.Comment == nil || envelope.Comment.StableID == uuid.Nil || envelope.Comment.NumericID <= 0 ||
			envelope.Comment.RepresentationVersion < 1 || envelope.Comment.CreatedAt.IsZero() || envelope.Comment.UpdatedAt.IsZero() ||
			strings.TrimSpace(envelope.Author.Login) == "" {
			return state.WebhookDelivery{}, ErrInvalid
		}
	} else if envelope.Comment != nil {
		return state.WebhookDelivery{}, ErrInvalid
	}
	if envelope.Author.UserID != nil && *envelope.Author.UserID == uuid.Nil {
		return state.WebhookDelivery{}, ErrInvalid
	}
	rawDigest := sha256.Sum256([]byte(envelope.RawBody))
	if len(envelope.BodyHash) != sha256.Size*2 || !strings.EqualFold(envelope.BodyHash, hex.EncodeToString(rawDigest[:])) {
		return state.WebhookDelivery{}, ErrInvalid
	}
	if value := strings.TrimSpace(header.Get(HeaderSchemaVersion)); value != "" && value != strconv.Itoa(envelope.SchemaVersion) {
		return state.WebhookDelivery{}, ErrInvalid
	}
	if value := strings.TrimSpace(header.Get(HeaderEventType)); value != "" && value != envelope.EventType {
		return state.WebhookDelivery{}, ErrInvalid
	}
	bodyDigest := sha256.Sum256(body)
	bodyDigestHex := hex.EncodeToString(bodyDigest[:])
	if value := strings.TrimSpace(header.Get(HeaderEnvelopeBodySHA256)); value != "" && !strings.EqualFold(value, bodyDigestHex) {
		return state.WebhookDelivery{}, ErrInvalid
	}
	delivery := state.WebhookDelivery{DeliveryID: deliveryID, EventID: eventID, SubscriptionID: subscriptionID,
		BodySHA256: bodyDigestHex, RawEnvelope: append([]byte(nil), body...), SchemaVersion: envelope.SchemaVersion,
		EventKey: envelope.EventKey, EventType: envelope.EventType, Action: envelope.Action,
		OrganizationID: envelope.OrganizationID.String(), RepositoryID: envelope.RepositoryID.String(),
		IssueID: envelope.Issue.StableID.String(), IssueNumber: envelope.Issue.Number,
		AuthorLogin: envelope.Author.Login, EnvelopeBodySHA256: strings.ToLower(envelope.BodyHash), ReceivedAt: receivedAt,
		Status: state.DeliveryPending}
	if envelope.Comment != nil {
		delivery.CommentID = envelope.Comment.StableID.String()
		delivery.CommentRevision = envelope.Comment.RepresentationVersion
	}
	return delivery, nil
}

func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func writeRetry(w http.ResponseWriter, delay time.Duration) {
	seconds := int(delay.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}

func writeProblem(w http.ResponseWriter, status int, code, requestID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status),
		"status": status, "code": code, "request_id": requestID})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
