package main

import (
	"testing"

	"github.com/higress-group/issue-spec/internal/server/config"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
)

func TestConfiguredOriginsBindsCookieSecurityToEffectivePosture(t *testing.T) {
	httpOrigins, err := configuredOrigins(config.Config{Environment: config.EnvironmentProduction,
		TransportPosture: config.TransportTrustedInternalHTTP, APIPublicURL: "http://10.0.0.8", WebPublicURL: "http://issues.internal"})
	if err != nil || httpOrigins.Posture != publicurl.TransportTrustedInternalHTTP || httpOrigins.Posture.SecureCookies() {
		t.Fatalf("HTTP origins=%+v err=%v", httpOrigins, err)
	}
	httpsOrigins, err := configuredOrigins(config.Config{Environment: config.EnvironmentProduction,
		TransportPosture: config.TransportHTTPS, APIPublicURL: "https://api.example", WebPublicURL: "https://issues.example"})
	if err != nil || !httpsOrigins.Posture.SecureCookies() {
		t.Fatalf("HTTPS origins=%+v err=%v", httpsOrigins, err)
	}
}
