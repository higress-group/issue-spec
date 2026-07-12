package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestConditionalCommentClientSuccessAndConflict(t *testing.T) {
	var version atomic.Int64
	version.Store(3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderConditionalCommentMutation, ConditionalCommentMutationVersion)
		w.Header().Set(HeaderRepresentationVersion, strconv.FormatInt(version.Load(), 10))
		w.Header().Set("ETag", `"comment"`)
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":7,"body":"before"}`))
		case http.MethodPatch:
			expected, _ := strconv.ParseInt(r.Header.Get(HeaderExpectedRepresentationVersion), 10, 64)
			if expected != version.Load() {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"Conflict"}`))
				return
			}
			version.Add(1)
			w.Header().Set(HeaderRepresentationVersion, strconv.FormatInt(version.Load(), 10))
			_, _ = w.Write([]byte(`{"id":7,"body":"after"}`))
		}
	}))
	defer server.Close()
	client := NewClientWithBaseURL("self.test", server.URL, "token", server.Client())
	result, err := client.UpdateCommentConditional(t.Context(), "o/r", 7, 3, "after")
	if err != nil || result.RepresentationVersion != 4 || result.Guarantee != CommentMutationStrictConditional {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	_, err = client.UpdateCommentConditional(t.Context(), "o/r", 7, 3, "stale")
	var conflict *CommentMutationConflictError
	if !errors.As(err, &conflict) || conflict.Expected != 3 || conflict.Current != 4 || !errors.Is(err, ErrCommentMutationConflict) {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestConditionalCommentClientFailsBeforeMutationWithoutCapability(t *testing.T) {
	patches := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patches.Add(1)
		}
		w.Header().Set(HeaderRepresentationVersion, "1")
		_, _ = w.Write([]byte(`{"id":7,"body":"before"}`))
	}))
	defer server.Close()
	client := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
	_, err := client.UpdateCommentConditional(t.Context(), "o/r", 7, 1, "after")
	if !errors.Is(err, ErrConditionalCommentMutationUnsupported) || patches.Load() != 0 {
		t.Fatalf("err=%v patches=%d", err, patches.Load())
	}
}

func TestConditionalCommentClientReturnsServerRaceIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderConditionalCommentMutation, ConditionalCommentMutationVersion)
		if r.Method == http.MethodGet {
			w.Header().Set(HeaderRepresentationVersion, "3")
			_, _ = w.Write([]byte(`{"id":7,"body":"before"}`))
			return
		}
		if r.Header.Get(HeaderExpectedRepresentationVersion) != "3" {
			t.Fatalf("expected header = %q", r.Header.Get(HeaderExpectedRepresentationVersion))
		}
		w.Header().Set(HeaderRepresentationVersion, "4")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"Conflict"}`))
	}))
	defer server.Close()
	client := NewClientWithBaseURL("self.test", server.URL, "token", server.Client())
	_, err := client.UpdateCommentConditional(t.Context(), "o/r", 7, 3, "after")
	var conflict *CommentMutationConflictError
	if !errors.As(err, &conflict) || conflict.Expected != 3 || conflict.Current != 4 {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestGHBackendDoesNotClaimConditionalCommentMutation(t *testing.T) {
	backend := &GHBackend{}
	if _, ok := any(backend).(ConditionalCommentBackend); ok {
		t.Fatal("GH backend must not claim strict conditional mutation")
	}
	if _, err := RequireConditionalCommentBackend(backend); !errors.Is(err, ErrConditionalCommentMutationUnsupported) {
		t.Fatalf("err=%v", err)
	}
	if CommentMutationNonAtomicSingleWriter != "non-atomic-single-writer" {
		t.Fatal("non-atomic compatibility guarantee changed")
	}
}

func TestConditionalCommentClientRejectsInvalidExpectedVersion(t *testing.T) {
	client := &Client{}
	if _, err := client.UpdateCommentConditional(context.Background(), "o/r", 7, 0, "body"); err == nil {
		t.Fatal("zero expected version should fail")
	}
}
