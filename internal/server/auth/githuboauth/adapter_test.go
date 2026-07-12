package githuboauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"
)

func TestNormalizeUserURLBindsExpectedProductionOrigin(t *testing.T) {
	tests := []struct {
		name, issuer, userURL, want string
		production                  bool
		wantErr                     bool
	}{
		{name: "github default", issuer: "https://github.com", production: true, want: DefaultUserURL},
		{name: "enterprise default", issuer: "https://ghe.example", production: true, want: "https://ghe.example/api/v3/user"},
		{name: "enterprise explicit", issuer: "https://ghe.example", userURL: "https://ghe.example/custom/user", production: true, want: "https://ghe.example/custom/user"},
		{name: "production http", issuer: "https://ghe.example", userURL: "http://ghe.example/user", production: true, wantErr: true},
		{name: "cross origin", issuer: "https://ghe.example", userURL: "https://evil.example/user", production: true, wantErr: true},
		{name: "query", issuer: "https://ghe.example", userURL: "https://ghe.example/user?token=x", production: true, wantErr: true},
		{name: "noncanonical port", issuer: "https://github.com", userURL: "https://api.github.com:443/user", production: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeUserURL(test.issuer, test.userURL, test.production)
			if test.wantErr {
				if err == nil {
					t.Fatalf("NormalizeUserURL() = %q, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("NormalizeUserURL() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestProfileClientDoesNotForwardTokenAcrossRedirectOrProxy(t *testing.T) {
	var redirected, proxied atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer transient" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		http.Redirect(w, r, target.URL+"/stolen", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxied.Add(1) }))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	client := newProfileClient(&oauth2.Token{AccessToken: "transient", TokenType: "Bearer"})
	response, err := client.Get(redirect.URL + "/user")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusFound || redirected.Load() != 0 || proxied.Load() != 0 {
		t.Fatalf("status=%d redirected=%d proxied=%d", response.StatusCode, redirected.Load(), proxied.Load())
	}
}

func TestProfileResponseIsStrictlyBounded(t *testing.T) {
	valid := `{"id":1,"login":"octocat"}`
	if user, err := decodeProfileUser(strings.NewReader(valid)); err != nil || user.ID != 1 {
		t.Fatalf("valid user = %+v, %v", user, err)
	}
	if _, err := decodeProfileUser(strings.NewReader(valid + strings.Repeat(" ", maxUserResponseBytes))); err == nil {
		t.Fatal("oversized response accepted")
	}
	if _, err := decodeProfileUser(strings.NewReader(valid + valid)); err == nil {
		t.Fatal("multiple JSON values accepted")
	}
}
