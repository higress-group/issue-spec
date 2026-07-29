package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

func nativeAnswerQuestionSnapshot() model.QuestionSnapshot {
	return model.QuestionSnapshot{
		ID: "QUESTION-701", Question: "Which behavior?", Blocking: true,
		DefaultAssumption: "Keep.", IssueURL: "https://issues.test/owner/repo/issues/7",
		SourceURL: "https://issues.test/owner/repo/issues/7#issuecomment-70",
		ChoiceModel: model.ChoiceModel{
			Version: model.ChoiceModelVersion, Mode: model.ChoiceModeSingle, AllowCustom: true,
			Options: []model.ChoiceOption{{ID: "keep", Label: "Keep", Description: "Keep behavior"}},
		},
	}
}

func TestNativeQuestionAnswerOperationsEncodeBoundedIntentAndDecodeCanonicalResponse(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	question := nativeAnswerQuestionSnapshot()
	payload, err := model.BuildAnswerPayload(question, []string{"keep"}, "")
	if err != nil {
		t.Fatal(err)
	}
	answerBody, err := templates.AnswerComment(templates.AnswerOptions{ID: "ANSWER-7099", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer native-answer-token" {
			t.Fatalf("authorization=%q", got)
		}
		switch requests {
		case 1:
			if r.Method != http.MethodGet ||
				r.URL.EscapedPath() != "/api/v1/repos/owner%20name/repo%23name/issues/7/questions/QUESTION-701" {
				t.Fatalf("GET %s", r.URL.EscapedPath())
			}
			_ = json.NewEncoder(w).Encode(NativeQuestionAuthority{
				Question: question, RepresentationVersion: 4, BodyDigest: digest,
			})
		case 2:
			if r.Method != http.MethodPost ||
				r.URL.EscapedPath() != "/api/v1/repos/owner%20name/repo%23name/issues/7/answers" {
				t.Fatalf("POST %s", r.URL.EscapedPath())
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 4 || body["question_id"] != "QUESTION-701" || body["question_digest"] != digest ||
				body["custom"] != "" {
				t.Fatalf("answer intent=%#v", body)
			}
			options, ok := body["option_ids"].([]any)
			if !ok || len(options) != 1 || options[0] != "keep" {
				t.Fatalf("option_ids=%#v", body["option_ids"])
			}
			for _, forbidden := range []string{"id", "answer_id", "body", "actor", "timestamp"} {
				if _, exists := body[forbidden]; exists {
					t.Fatalf("answer intent contains %q: %#v", forbidden, body)
				}
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(NativeAnswerResult{
				Comment: Comment{
					ID: 71, HTMLURL: "https://issues.test/owner/repo/issues/7#issuecomment-71",
					URL: "https://issues.test/api/v3/repos/owner/repo/issues/comments/71", Body: answerBody,
				},
				Question: question, QuestionRepresentationVersion: 4, QuestionBodyDigest: digest,
			})
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	client, err := NewClientWithOptions(ClientOptions{
		Host: "issues.test", BaseURL: server.URL + "/api/v1", Token: "native-answer-token",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := client.GetNativeQuestion(t.Context(), "owner name/repo#name", 7, "QUESTION-701")
	if err != nil {
		t.Fatal(err)
	}
	if authority.BodyDigest != digest || authority.RepresentationVersion != 4 || authority.Question.ID != "QUESTION-701" {
		t.Fatalf("authority=%+v", authority)
	}
	result, err := client.CreateNativeAnswer(t.Context(), "owner name/repo#name", 7, NativeAnswerIntent{
		QuestionID: "QUESTION-701", QuestionDigest: authority.BodyDigest, OptionIDs: []string{"keep"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Comment.ID != 71 || model.ParseTypedComment(result.Comment.Body).ID != "ANSWER-7099" ||
		result.QuestionBodyDigest != digest || requests != 2 {
		t.Fatalf("result=%+v requests=%d", result, requests)
	}
}

func TestNativeQuestionAnswerOperationsRejectIncompleteCanonicalResponse(t *testing.T) {
	const digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	question := nativeAnswerQuestionSnapshot()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(NativeAnswerResult{
			Question: question, QuestionRepresentationVersion: 2, QuestionBodyDigest: digest,
		})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{
		Host: "issues.test", BaseURL: server.URL, Token: "token", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateNativeAnswer(t.Context(), "owner/repo", 7, NativeAnswerIntent{
		QuestionID: "QUESTION-701", QuestionDigest: digest, Custom: "Use the safe path",
	})
	if err == nil || !strings.Contains(err.Error(), "canonical comment is incomplete") {
		t.Fatalf("error=%v", err)
	}
}
