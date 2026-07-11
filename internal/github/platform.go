package github

import (
	"os"
	"strings"
)

const (
	PlatformGitHub  = "github"
	PlatformGitCode = "gitcode"
)

type PlatformConfig struct {
	Kind             string
	BaseURLTemplate  string
	AuthHeader       string
	AuthHeaderPrefix string
	AcceptHeader     string
	APIHeaders       map[string]string
	TokenEnvVars     []string
	Capabilities     PlatformCapabilities
}

type PlatformCapabilities struct {
	Notifications bool
	Reactions     bool
	Subscription  bool
	CommentEdit   bool
}

func PlatformForHost(host string) PlatformConfig {
	host = normalizeHost(host)

	switch {
	case strings.EqualFold(host, "gitcode.com"),
		strings.EqualFold(host, "atomgit.com"):
		return gitcodePlatform(host)
	default:
		return githubPlatform(host)
	}
}

func githubPlatform(host string) PlatformConfig {
	return PlatformConfig{
		Kind:             PlatformGitHub,
		BaseURLTemplate:  githubBaseURL(host),
		AuthHeader:       "Authorization",
		AuthHeaderPrefix: "Bearer ",
		AcceptHeader:     "application/vnd.github+json",
		APIHeaders: map[string]string{
			"X-GitHub-Api-Version": "2022-11-28",
		},
		TokenEnvVars: []string{"ISSUE_SPEC_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"},
		Capabilities: PlatformCapabilities{
			Notifications: true,
			Reactions:     true,
			Subscription:  true,
			CommentEdit:   true,
		},
	}
}

func gitcodePlatform(host string) PlatformConfig {
	return PlatformConfig{
		Kind:             PlatformGitCode,
		BaseURLTemplate:  gitcodeBaseURL(host),
		AuthHeader:       "PRIVATE-TOKEN",
		AuthHeaderPrefix: "",
		AcceptHeader:     "application/json",
		APIHeaders:       nil,
		TokenEnvVars:     []string{"ISSUE_SPEC_GITCODE_TOKEN", "GITCODE_TOKEN"},
		Capabilities: PlatformCapabilities{
			Notifications: false,
			Reactions:     false,
			Subscription:  false,
			CommentEdit:   false,
		},
	}
}

func githubBaseURL(host string) string {
	if host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

func gitcodeBaseURL(host string) string {
	if override := strings.TrimSpace(os.Getenv("ISSUE_SPEC_GITCODE_API_URL")); override != "" {
		return strings.TrimRight(override, "/")
	}
	return "https://" + host + "/api/v5"
}
