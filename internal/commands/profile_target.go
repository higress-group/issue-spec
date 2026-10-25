package commands

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
)

// resolveProfile preserves profile precedence. An explicit hostname is a target
// assertion, never permission to select different credentials automatically.
func (a *app) resolveProfile(name, host string) (auth.Profile, string, error) {
	profile, source, err := auth.ResolveProfile(name, host)
	if err != nil {
		return profile, source, err
	}
	return profile, source, a.validateProfileTarget(profile, source)
}

func (a *app) validateProfileTarget(profile auth.Profile, source string) error {
	if !a.hostnameExplicit {
		return nil
	}
	requested, err := parseExplicitHostname(a.explicitHostname)
	if err != nil {
		return err
	}
	endpoint, _ := url.Parse(profile.APIURL)
	if profile.Kind == auth.ProfileKindGitHub {
		endpoint, _ = url.Parse("https://" + profile.Hostname)
	}
	matches := endpoint != nil && strings.EqualFold(requested.Hostname(), endpoint.Hostname())
	if matches && requested.Port() != "" {
		port := endpoint.Port()
		if port == "" {
			port = "443"
			if endpoint.Scheme == "http" {
				port = "80"
			}
		}
		matches = requested.Port() == port
	}
	if matches {
		return nil
	}
	remedy := "select the intended backend with --profile <name>, or omit --hostname to use the resolved profile"
	if strings.EqualFold(requested.Host, "github.com") && profile.Name != auth.DefaultProfileName {
		remedy = "use --profile github for GitHub, or omit --hostname to use the resolved profile"
	}
	return fmt.Errorf("--hostname %q conflicts with resolved profile %q (selection_source=profile:%s, API origin=%s); %s",
		requested.Host, profile.Name, source, profile.APIOrigin(), remedy)
}

func parseExplicitHostname(host string) (*url.URL, error) {
	parsed, err := url.Parse("//" + auth.NormalizeHost(host))
	if strings.TrimSpace(host) == "" || err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		// Do not echo malformed input: it may contain URL credentials.
		return nil, fmt.Errorf("--hostname must be a hostname with an optional port")
	}
	return parsed, nil
}
