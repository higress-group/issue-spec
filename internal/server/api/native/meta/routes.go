// Package meta exposes non-sensitive server capability discovery. The final
// composition owner injects flags from the RouteSets it actually mounts.
package meta

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/higress-group/issue-spec/internal/codereview"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
)

type Features struct {
	Bootstrap            bool `json:"bootstrap"`
	PersonalAccessTokens bool `json:"personal_access_tokens"`
	Organizations        bool `json:"organizations"`
	SourceBindings       bool `json:"source_bindings"`
	Webhooks             bool `json:"webhooks"`
	ChangeBoards         bool `json:"change_boards"`
	Runner               bool `json:"runner"`
	RecoveryExchange     bool `json:"recovery_exchange"`
}

type Transport struct {
	Mode   string `json:"mode"`
	Secure bool   `json:"secure"`
}

type ServerMetadata struct {
	ServerInstanceID string                           `json:"server_instance_id"`
	APIURL           string                           `json:"api_url"`
	NativeAPIURL     string                           `json:"native_api_url"`
	WebURL           string                           `json:"web_url"`
	Transport        Transport                        `json:"transport"`
	Providers        []codereview.ProviderDescription `json:"providers"`
}

type Dependencies struct {
	Features Features
	Metadata ServerMetadata
}

func NewServerMetadata(apiOrigin, webOrigin string, providers []codereview.ProviderDescription) (ServerMetadata, error) {
	api, loopback, err := canonicalPublicOrigin(apiOrigin)
	if err != nil {
		return ServerMetadata{}, fmt.Errorf("meta API origin: %w", err)
	}
	web, webLoopback, err := canonicalPublicOrigin(webOrigin)
	if err != nil {
		return ServerMetadata{}, fmt.Errorf("meta web origin: %w", err)
	}
	if loopback != webLoopback {
		return ServerMetadata{}, errors.New("meta public origins must use a consistent transport mode")
	}
	digest := sha256.Sum256([]byte(api))
	return ServerMetadata{
		ServerInstanceID: "issue-spec:" + hex.EncodeToString(digest[:16]),
		APIURL:           api + "/api/v3", NativeAPIURL: api + "/api/v1", WebURL: web,
		Transport: Transport{Mode: map[bool]string{true: "loopback-http", false: "https"}[loopback], Secure: !loopback},
		Providers: append([]codereview.ProviderDescription(nil), providers...),
	}, nil
}

func canonicalPublicOrigin(raw string) (string, bool, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", false, errors.New("must be an absolute origin without path, credentials, query, or fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme == "https" {
		return strings.TrimSuffix(u.String(), "/"), false, nil
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "http" || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
		return "", false, errors.New("must use https, except loopback development origins may use http")
	}
	return strings.TrimSuffix(u.String(), "/"), true, nil
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if strings.TrimSpace(deps.Metadata.ServerInstanceID) == "" {
		return routeset.RouteSet{}, errors.New("native meta: server metadata is required")
	}
	h := handlers{features: deps.Features, metadata: deps.Metadata}
	set := routeset.RouteSet{Name: "native-meta", Routes: []routeset.Route{{
		Name: "native.meta.get", Method: http.MethodGet, Pattern: "/api/v1/meta",
		Handler: adminapi.WithRequestID(http.HandlerFunc(h.get)),
	}}}
	return set, set.Validate()
}

type handlers struct {
	features Features
	metadata ServerMetadata
}

func (h handlers) get(w http.ResponseWriter, _ *http.Request) {
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"api_version": "v1", "features": h.features,
		"server_instance_id": h.metadata.ServerInstanceID, "api_url": h.metadata.APIURL,
		"native_api_url": h.metadata.NativeAPIURL, "web_url": h.metadata.WebURL,
		"transport": h.metadata.Transport, "providers": h.metadata.Providers})
}
