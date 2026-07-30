package answers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	githubissues "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
)

type answerCall struct {
	owner, repo, webOrigin string
	issue                  int64
	subject                authz.Subject
	intent                 githubissues.AnswerIntent
}

type fakeAnswerService struct {
	createCalls []answerCall
	getCalls    []answerCall
	createErr   error
	getErr      error
	principal   serverauth.Principal
}

func (s *fakeAnswerService) GetQuestion(_ context.Context, owner, repo string, issue int64,
	subject authz.Subject, webOrigin, questionID string) (models.RepositoryResource,
	githubissues.QuestionAuthority, error) {
	s.getCalls = append(s.getCalls, answerCall{owner: owner, repo: repo, issue: issue,
		subject: subject, webOrigin: webOrigin, intent: githubissues.AnswerIntent{QuestionID: questionID}})
	return testResource(), testQuestion(), s.getErr
}

func (s *fakeAnswerService) CreateAnswer(_ context.Context, owner, repo string, issue int64,
	subject authz.Subject, webOrigin string, intent githubissues.AnswerIntent) (models.RepositoryResource,
	models.CommentSnapshot, githubissues.QuestionAuthority, error) {
	s.createCalls = append(s.createCalls, answerCall{owner: owner, repo: repo, issue: issue,
		subject: subject, webOrigin: webOrigin, intent: intent})
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	commentID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	return testResource(), models.CommentSnapshot{
		Comment: models.Comment{
			ID: commentID, AuthorID: &s.principal.User.ID, Body: "canonical server ANSWER",
			RepresentationVersion: 1, CreatedAt: now, UpdatedAt: now,
		},
		IssueNumber: 7, AuthorLogin: s.principal.User.Login, AuthorDisplayName: "Alice",
	}, testQuestion(), s.createErr
}

func TestAnswerCreateAdmitsSessionAndPATWhilePreservingBrowserProtections(t *testing.T) {
	principal := testPrincipal(serverauth.CredentialSession)
	service := &fakeAnswerService{principal: principal}
	handler := answerMux(t, service, principal)
	body := answerIntentJSON()
	tests := []struct {
		name       string
		cookie     bool
		origin     string
		csrf       string
		bearer     string
		wantStatus int
	}{
		{name: "no credential", origin: "https://web.example.test", csrf: "valid-csrf", wantStatus: http.StatusUnauthorized},
		{name: "invalid bearer", bearer: "invalid-bearer", wantStatus: http.StatusUnauthorized},
		{name: "missing origin", cookie: true, csrf: "valid-csrf", wantStatus: http.StatusForbidden},
		{name: "wrong origin", cookie: true, origin: "https://evil.example", csrf: "valid-csrf", wantStatus: http.StatusForbidden},
		{name: "missing csrf", cookie: true, origin: "https://web.example.test", wantStatus: http.StatusForbidden},
		{name: "trusted browser", cookie: true, origin: "https://web.example.test", csrf: "valid-csrf", wantStatus: http.StatusCreated},
		{name: "trusted PAT ignores browser state", cookie: true, origin: "https://evil.example",
			csrf: "invalid-csrf", bearer: "valid-bearer", wantStatus: http.StatusCreated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost,
				"/api/v1/repos/acme/widgets/issues/7/answers", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			if test.cookie {
				request.AddCookie(&http.Cookie{Name: "session", Value: "valid-session"})
			}
			if test.bearer != "" {
				request.Header.Set("Authorization", "Bearer "+test.bearer)
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.csrf != "" {
				request.Header.Set("X-CSRF-Token", test.csrf)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if len(service.createCalls) != 2 {
		t.Fatalf("untrusted requests reached service: %+v", service.createCalls)
	}
	for index, kind := range []serverauth.CredentialKind{
		serverauth.CredentialSession, serverauth.CredentialPAT,
	} {
		call := service.createCalls[index]
		if call.owner != "acme" || call.repo != "widgets" || call.issue != 7 ||
			call.webOrigin != "https://web.example.test" || call.intent.QuestionID != "QUESTION-007" ||
			call.intent.QuestionDigest != strings.Repeat("a", 64) ||
			len(call.intent.OptionIDs) != 1 || call.intent.OptionIDs[0] != "safe" ||
			call.subject.Principal == nil || call.subject.Principal.User.ID != principal.User.ID ||
			call.subject.Principal.Kind != kind {
			t.Fatalf("trusted %s call=%+v", kind, call)
		}
	}
}

func TestQuestionConfirmationAdmitsPATWithoutBrowserState(t *testing.T) {
	session := testPrincipal(serverauth.CredentialSession)
	service := &fakeAnswerService{principal: session}
	handler := answerMux(t, service, session)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/repos/acme/widgets/issues/7/questions/QUESTION-007", nil)
	request.Header.Set("Authorization", "Bearer valid-bearer")
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("X-CSRF-Token", "invalid-csrf")
	request.AddCookie(&http.Cookie{Name: "session", Value: "invalid-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(service.getCalls) != 1 ||
		service.getCalls[0].subject.Principal == nil ||
		service.getCalls[0].subject.Principal.Kind != serverauth.CredentialPAT {
		t.Fatalf("PAT question response=%d calls=%+v body=%s",
			response.Code, service.getCalls, response.Body.String())
	}
}

func TestAnswerRoutesRejectDelegatedAndRecoveryCredentials(t *testing.T) {
	session := testPrincipal(serverauth.CredentialSession)
	for _, kind := range []serverauth.CredentialKind{
		serverauth.CredentialDelegated, serverauth.CredentialRecovery,
	} {
		t.Run(string(kind), func(t *testing.T) {
			service := &fakeAnswerService{principal: session}
			handler := answerMuxWithPrincipals(t, service, session, testPrincipal(kind),
				"https://web.example.test")
			for _, request := range []*http.Request{
				httptest.NewRequest(http.MethodGet,
					"/api/v1/repos/acme/widgets/issues/7/questions/QUESTION-007", nil),
				httptest.NewRequest(http.MethodPost,
					"/api/v1/repos/acme/widgets/issues/7/answers", strings.NewReader(answerIntentJSON())),
			} {
				request.Header.Set("Authorization", "Bearer valid-bearer")
				if request.Method == http.MethodPost {
					request.Header.Set("Content-Type", "application/json")
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusForbidden ||
					!strings.Contains(response.Body.String(), "trusted_answer_credential_required") {
					t.Fatalf("%s %s response=%d body=%s",
						kind, request.Method, response.Code, response.Body.String())
				}
			}
			if len(service.getCalls) != 0 || len(service.createCalls) != 0 {
				t.Fatalf("%s reached service: get=%+v create=%+v",
					kind, service.getCalls, service.createCalls)
			}
		})
	}
}

func TestAnswerCreateIsAppendOnlyForRepeatedIntentAndBoundsClientInput(t *testing.T) {
	principal := testPrincipal(serverauth.CredentialSession)
	service := &fakeAnswerService{principal: principal}
	handler := answerMux(t, service, principal)
	body := answerIntentJSON()
	for range 2 {
		response := trustedAnswerRequest(handler, body)
		if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"canonical server ANSWER"`) ||
			response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	}
	if len(service.createCalls) != 2 {
		t.Fatalf("identical submissions were collapsed: calls=%d", len(service.createCalls))
	}
	before := len(service.createCalls)
	oversized := `{"question_id":"QUESTION-007","question_digest":"` + strings.Repeat("a", 64) +
		`","custom":"` + strings.Repeat("x", maxAnswerIntentBytes) + `"}`
	response := trustedAnswerRequest(handler, oversized)
	if response.Code != http.StatusBadRequest || len(service.createCalls) != before {
		t.Fatalf("oversized response=%d calls=%d/%d body=%s", response.Code,
			before, len(service.createCalls), response.Body.String())
	}
	response = trustedAnswerRequest(handler, `{"question_id":"QUESTION-007","question_digest":"`+
		strings.Repeat("a", 64)+`","body":"forged typed markdown"}`)
	if response.Code != http.StatusBadRequest || len(service.createCalls) != before {
		t.Fatalf("client Markdown response=%d calls=%d/%d body=%s", response.Code,
			before, len(service.createCalls), response.Body.String())
	}
}

func TestQuestionConfirmationReloadAndAnswerErrorsFailClosed(t *testing.T) {
	principal := testPrincipal(serverauth.CredentialSession)
	service := &fakeAnswerService{principal: principal}
	handler := answerMux(t, service, principal)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/repos/acme/widgets/issues/7/questions/QUESTION-007", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: "valid-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"QUESTION-007"`) ||
		len(service.getCalls) != 1 {
		t.Fatalf("question response=%d body=%s calls=%+v", response.Code, response.Body.String(), service.getCalls)
	}

	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{err: githubissues.ErrInvalidAnswerIntent, status: http.StatusUnprocessableEntity, code: "invalid_answer_intent"},
		{err: githubissues.ErrInvalidQuestionAuthority, status: http.StatusConflict, code: "question_invalid"},
		{err: githubissues.ErrQuestionChanged, status: http.StatusConflict, code: "question_changed"},
		{err: &githubissues.DecisionError{Decision: authz.Decision{Visible: true}}, status: http.StatusForbidden, code: "forbidden"},
		{err: errors.New("failed"), status: http.StatusInternalServerError, code: "internal_error"},
	} {
		service.createErr = test.err
		response := trustedAnswerRequest(handler, answerIntentJSON())
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) ||
			strings.Contains(response.Body.String(), "canonical server ANSWER") {
			t.Fatalf("err=%v response=%d body=%s", test.err, response.Code, response.Body.String())
		}
	}
}

func TestHTTPWebOriginReachesTrustedQuestionAndAnswerService(t *testing.T) {
	principal := testPrincipal(serverauth.CredentialSession)
	service := &fakeAnswerService{principal: principal}
	const webOrigin = "http://web.example.test"
	handler := answerMuxWithOrigin(t, service, principal, webOrigin)

	questionRequest := httptest.NewRequest(http.MethodGet,
		"/api/v1/repos/acme/widgets/issues/7/questions/QUESTION-007", nil)
	questionRequest.Header.Set("Origin", webOrigin)
	questionRequest.AddCookie(&http.Cookie{Name: "session", Value: "valid-session"})
	questionResponse := httptest.NewRecorder()
	handler.ServeHTTP(questionResponse, questionRequest)
	if questionResponse.Code != http.StatusOK || len(service.getCalls) != 1 ||
		service.getCalls[0].webOrigin != webOrigin {
		t.Fatalf("HTTP question response=%d calls=%+v body=%s",
			questionResponse.Code, service.getCalls, questionResponse.Body.String())
	}

	answerResponse := trustedAnswerRequestAtOrigin(handler, answerIntentJSON(), webOrigin)
	if answerResponse.Code != http.StatusCreated || len(service.createCalls) != 1 ||
		service.createCalls[0].webOrigin != webOrigin {
		t.Fatalf("HTTP answer response=%d calls=%+v body=%s",
			answerResponse.Code, service.createCalls, answerResponse.Body.String())
	}
}

func TestAnswerRoutesRejectNonHTTPAbsoluteOrCredentialBearingWebOrigins(t *testing.T) {
	service := &fakeAnswerService{}
	authenticate := func(next http.Handler) http.Handler { return next }
	for _, webOrigin := range []string{
		"",
		"/relative",
		"web.example.test",
		"javascript:alert(1)",
		"data:text/html,hostile",
		"file:///tmp/question",
		"http:///empty-host",
		"https://alice:secret@web.example.test",
	} {
		if _, err := NewRouteSet(Dependencies{
			Service: service, Authenticate: authenticate, WebOrigin: webOrigin,
		}); err == nil {
			t.Fatalf("invalid Web origin %q accepted", webOrigin)
		}
	}
}

func answerIntentJSON() string {
	return `{"question_id":"QUESTION-007","question_digest":"` + strings.Repeat("a", 64) +
		`","option_ids":["safe"],"custom":""}`
}

func trustedAnswerRequest(handler http.Handler, body string) *httptest.ResponseRecorder {
	return trustedAnswerRequestAtOrigin(handler, body, "https://web.example.test")
}

func trustedAnswerRequestAtOrigin(handler http.Handler, body, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/repos/acme/widgets/issues/7/answers", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	request.Header.Set("X-CSRF-Token", "valid-csrf")
	request.AddCookie(&http.Cookie{Name: "session", Value: "valid-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func answerMux(t *testing.T, service Service, principal serverauth.Principal) http.Handler {
	t.Helper()
	return answerMuxWithOrigin(t, service, principal, "https://web.example.test")
}

func answerMuxWithOrigin(t *testing.T, service Service, principal serverauth.Principal, webOrigin string) http.Handler {
	t.Helper()
	return answerMuxWithPrincipals(t, service, principal, testPrincipal(serverauth.CredentialPAT), webOrigin)
}

func answerMuxWithPrincipals(t *testing.T, service Service, session, bearer serverauth.Principal,
	webOrigin string) http.Handler {
	t.Helper()
	apiOrigin := strings.Replace(webOrigin, "web.", "api.", 1)
	origins, err := publicurl.New(apiOrigin, webOrigin, nil)
	if err != nil {
		t.Fatal(err)
	}
	middleware := serverauth.Middleware{
		SessionCookieName: "session",
		AllowedOrigins:    map[string]struct{}{webOrigin: {}},
		Sessions:          answerSessions{principal: session},
		Bearer:            answerBearer{principal: bearer},
	}
	set, err := NewRouteSet(Dependencies{
		Service: service, Presenter: codec.Presenter{Origins: origins},
		Authenticate: adminapi.NativeAuthenticate(middleware), WebOrigin: webOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.SelfHostedPolicy(), set)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

type answerSessions struct{ principal serverauth.Principal }

func (s answerSessions) Authenticate(_ context.Context, token string) (serverauth.Principal, error) {
	if token != "valid-session" {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	return s.principal, nil
}

func (answerSessions) ValidateCSRF(_ serverauth.Principal, token string) error {
	if token != "valid-csrf" {
		return serverauth.ErrInvalidCSRF
	}
	return nil
}

type answerBearer struct{ principal serverauth.Principal }

func (b answerBearer) AuthenticateBearer(_ context.Context, token string) (serverauth.Principal, error) {
	if token != "valid-bearer" {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	return b.principal, nil
}

func testPrincipal(kind serverauth.CredentialKind) serverauth.Principal {
	return serverauth.Principal{
		User: serverauth.User{
			ID: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), Login: "alice", Status: "active",
		},
		Kind: kind, CredentialID: uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
	}
}

func testResource() models.RepositoryResource {
	return models.RepositoryResource{Owner: "acme", Name: "widgets"}
}

func testQuestion() githubissues.QuestionAuthority {
	return githubissues.QuestionAuthority{
		Snapshot: model.QuestionSnapshot{
			ID: "QUESTION-007", Question: "Choose?", Blocking: true,
			DefaultAssumption: "Safe", IssueURL: "https://web.example.test/acme/widgets/issues/7",
			SourceURL: "https://web.example.test/acme/widgets/issues/7#issuecomment-42",
			ChoiceModel: model.ChoiceModel{
				Version: 1, Mode: model.ChoiceModeSingle,
				Options: []model.ChoiceOption{{ID: "safe", Label: "Safe"}},
			},
		},
		RepresentationVersion: 1, BodyDigest: strings.Repeat("a", 64),
	}
}
