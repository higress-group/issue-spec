package github

import "testing"

func TestPlatformForHostGitHub(t *testing.T) {
	platform := PlatformForHost("github.com")
	if platform.Kind != PlatformGitHub {
		t.Fatalf("Kind = %q, want %q", platform.Kind, PlatformGitHub)
	}
	if platform.BaseURLTemplate != "https://api.github.com" {
		t.Fatalf("BaseURLTemplate = %q, want https://api.github.com", platform.BaseURLTemplate)
	}
	if platform.AuthHeader != "Authorization" {
		t.Fatalf("AuthHeader = %q", platform.AuthHeader)
	}
	if platform.AuthHeaderPrefix != "Bearer " {
		t.Fatalf("AuthHeaderPrefix = %q", platform.AuthHeaderPrefix)
	}
	if !platform.Capabilities.Notifications {
		t.Fatal("GitHub should support notifications")
	}
	if !platform.Capabilities.Reactions {
		t.Fatal("GitHub should support reactions")
	}
	if !platform.Capabilities.Subscription {
		t.Fatal("GitHub should support subscription")
	}
	if !platform.Capabilities.CommentEdit {
		t.Fatal("GitHub should support comment edit")
	}
}

func TestPlatformForHostGitHubEnterprise(t *testing.T) {
	platform := PlatformForHost("git.example.com")
	if platform.Kind != PlatformGitHub {
		t.Fatalf("Kind = %q, want %q", platform.Kind, PlatformGitHub)
	}
	if platform.BaseURLTemplate != "https://git.example.com/api/v3" {
		t.Fatalf("BaseURLTemplate = %q", platform.BaseURLTemplate)
	}
}

func TestPlatformForHostGitCode(t *testing.T) {
	platform := PlatformForHost("gitcode.com")
	if platform.Kind != PlatformGitCode {
		t.Fatalf("Kind = %q, want %q", platform.Kind, PlatformGitCode)
	}
	if platform.BaseURLTemplate != "https://gitcode.com/api/v5" {
		t.Fatalf("BaseURLTemplate = %q, want https://gitcode.com/api/v5", platform.BaseURLTemplate)
	}
	if platform.AuthHeader != "PRIVATE-TOKEN" {
		t.Fatalf("AuthHeader = %q", platform.AuthHeader)
	}
	if platform.AuthHeaderPrefix != "" {
		t.Fatalf("AuthHeaderPrefix = %q, want empty", platform.AuthHeaderPrefix)
	}
	if platform.AcceptHeader != "application/json" {
		t.Fatalf("AcceptHeader = %q", platform.AcceptHeader)
	}
	if platform.Capabilities.Notifications {
		t.Fatal("GitCode should not support notifications")
	}
	if platform.Capabilities.Reactions {
		t.Fatal("GitCode should not support reactions")
	}
	if platform.Capabilities.Subscription {
		t.Fatal("GitCode should not support subscription")
	}
	if platform.Capabilities.CommentEdit {
		t.Fatal("GitCode should not support comment edit")
	}
}

func TestPlatformForHostAtomGit(t *testing.T) {
	platform := PlatformForHost("atomgit.com")
	if platform.Kind != PlatformGitCode {
		t.Fatalf("Kind = %q, want %q", platform.Kind, PlatformGitCode)
	}
	if platform.BaseURLTemplate != "https://atomgit.com/api/v5" {
		t.Fatalf("BaseURLTemplate = %q", platform.BaseURLTemplate)
	}
}

func TestPlatformForHostNormalizesHost(t *testing.T) {
	platform := PlatformForHost("https://GITCODE.com")
	if platform.Kind != PlatformGitCode {
		t.Fatalf("Kind = %q, want %q for https://GITCODE.com", platform.Kind, PlatformGitCode)
	}
}
