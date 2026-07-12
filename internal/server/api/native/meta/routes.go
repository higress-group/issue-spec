// Package meta exposes non-sensitive server capability discovery. The final
// composition owner injects flags from the RouteSets it actually mounts.
package meta

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/higress-group/issue-spec/internal/codereview"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
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
	TransportPosture publicurl.TransportPosture       `json:"transport_posture"`
	Providers        []codereview.ProviderDescription `json:"providers"`
}

type Dependencies struct {
	Features Features
	Metadata ServerMetadata
}

func NewServerMetadata(instanceID, apiOrigin, webOrigin string, providers []codereview.ProviderDescription) (ServerMetadata, error) {
	posture := publicurl.TransportHTTPS
	if strings.HasPrefix(strings.TrimSpace(apiOrigin), "http://") {
		parsed, err := url.Parse(strings.TrimSpace(apiOrigin))
		if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") {
			return ServerMetadata{}, errors.New("meta public HTTP requires an explicit trusted-internal-http posture")
		}
		posture = publicurl.TransportTrustedInternalHTTP
	}
	return NewServerMetadataWithPosture(instanceID, apiOrigin, webOrigin, providers, posture)
}

func NewServerMetadataWithPosture(instanceID, apiOrigin, webOrigin string, providers []codereview.ProviderDescription, posture publicurl.TransportPosture) (ServerMetadata, error) {
	if !posture.Valid() {
		return ServerMetadata{}, errors.New("meta transport posture is invalid")
	}
	instanceID = strings.TrimSpace(instanceID)
	if !strings.HasPrefix(instanceID, "issue-spec:") || len(instanceID) <= len("issue-spec:") {
		return ServerMetadata{}, errors.New("meta server instance identity is invalid")
	}
	origins, err := publicurl.NewWithPosture(strings.TrimSuffix(apiOrigin, "/"), strings.TrimSuffix(webOrigin, "/"), nil, posture)
	if err != nil {
		return ServerMetadata{}, fmt.Errorf("meta public origins: %w", err)
	}
	api, web := origins.API.String(), origins.Web.String()
	mode := string(posture)
	if posture == publicurl.TransportTrustedInternalHTTP {
		if parsed, _ := url.Parse(api); parsed != nil && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1") {
			mode = "loopback-http"
		}
	}
	return ServerMetadata{
		ServerInstanceID: instanceID,
		APIURL:           api, NativeAPIURL: api + "/api/v1", WebURL: web,
		Transport:        Transport{Mode: mode, Secure: posture.SecureCookies()},
		TransportPosture: posture,
		Providers:        append([]codereview.ProviderDescription(nil), providers...),
	}, nil
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
		"transport": h.metadata.Transport, "transport_posture": h.metadata.TransportPosture,
		"providers": h.metadata.Providers})
	// transport_posture is the operator-selected policy; transport remains the
	// wire-level mode for backward-compatible clients.
}
