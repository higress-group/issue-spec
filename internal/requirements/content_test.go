package requirements

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

func TestCanonicalContentIdentityAndManifest(t *testing.T) {
	bundle, err := Canonical(Distribution{})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.ContentID != "sha256:428d97c177f061b8eda343e5b08761323d9dd9b8d3007bcddc2dbc35732db41a" {
		t.Fatalf("content ID = %q", bundle.Manifest.ContentID)
	}
	if got, err := ContentID(); err != nil || got != bundle.Manifest.ContentID {
		t.Fatalf("ContentID() = %q, %v", got, err)
	}
	if err := validateBundle(bundle); err != nil {
		t.Fatalf("canonical bundle invalid: %v", err)
	}
	if len(bundle.Files) != 2 {
		t.Fatalf("canonical file count = %d, want 2", len(bundle.Files))
	}
	managed, ok := fileNamed(bundle.Files, ManagedManifestName)
	if !ok {
		t.Fatal("managed manifest is not installed inside the skill")
	}
	if !bytes.HasSuffix(managed.Data, []byte("\n")) {
		t.Fatal("managed manifest is not canonically newline-terminated")
	}
	manifestJSON, err := json.Marshal(bundle.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"server_url", "repository", "credential", "access_token", "\"pat\""} {
		if strings.Contains(strings.ToLower(string(manifestJSON)), forbidden) {
			t.Fatalf("managed manifest contains forbidden state field %q: %s", forbidden, manifestJSON)
		}
	}
}

func TestArchiveIsDeterministicAndMatchesEmbeddedBundle(t *testing.T) {
	distribution := Distribution{Channel: "rolling", SourceRevision: strings.Repeat("a", 40), CLIBuild: "0.2.0-main.1+gaaaaaaa"}
	first, err := BuildArchive(distribution)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildArchive(distribution)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same distribution produced different archive bytes")
	}
	hash := sha256.Sum256(first)
	verified, err := VerifyArchive(first, "sha256:"+hex.EncodeToString(hash[:]))
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := Canonical(distribution)
	if err != nil {
		t.Fatal(err)
	}
	if !filesEqual(verified.Files, embedded.Files) || !manifestsEqual(verified.Manifest, embedded.Manifest) {
		t.Fatal("verified standalone tree differs from embedded tree")
	}
	if _, err := VerifyArchive(first, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("wrong checksum error = %v", err)
	}
}

func TestArchiveRejectsTraversalAndNonCanonicalContent(t *testing.T) {
	var traversal bytes.Buffer
	zw := zip.NewWriter(&traversal)
	entry, err := zw.Create(SkillName + "/../context.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(`{"token":"not-a-real-secret"}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArchive(traversal.Bytes(), ""); err == nil || !strings.Contains(err.Error(), "invalid archive entry") {
		t.Fatalf("traversal archive error = %v", err)
	}

	modified := testBundle(t, "modified skill\n")
	archive, err := ArchiveBundle(modified)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArchive(archive, ""); err == nil || !strings.Contains(err.Error(), "does not match embedded content ID") {
		t.Fatalf("non-canonical archive error = %v", err)
	}
}

func TestCompatibility(t *testing.T) {
	development, err := Canonical(Distribution{})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"development", "0.1.0", "v0.2.0", "1.0.0-rc.1"} {
		if err := development.CheckCompatibility(version); err != nil {
			t.Errorf("version %q rejected: %v", version, err)
		}
	}
	for _, version := range []string{"0.0.9", "1.2", "not-a-version"} {
		if err := development.CheckCompatibility(version); err == nil {
			t.Errorf("version %q unexpectedly accepted", version)
		}
	}
	published, err := Canonical(Distribution{Channel: "stable", SourceRevision: strings.Repeat("b", 40), CLIBuild: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := published.CheckCompatibility("development"); err == nil {
		t.Fatal("development CLI unexpectedly accepted a published skill")
	}
	if _, err := Canonical(Distribution{Channel: "rolling", CLIBuild: "0.1.0"}); err == nil {
		t.Fatal("rolling distribution without source revision was accepted")
	}
	if _, err := Canonical(Distribution{Channel: "rolling", SourceRevision: "short", CLIBuild: "0.1.0"}); err == nil {
		t.Fatal("rolling distribution with abbreviated source revision was accepted")
	}
}

func filesEqual(left, right []File) bool {
	if len(left) != len(right) {
		return false
	}
	left = cloneFiles(left)
	right = cloneFiles(right)
	sortFiles(left)
	sortFiles(right)
	for index := range left {
		if left[index].Path != right[index].Path || left[index].Mode.Perm() != right[index].Mode.Perm() || !bytes.Equal(left[index].Data, right[index].Data) {
			return false
		}
	}
	return true
}

func testBundle(t *testing.T, skill string) Bundle {
	t.Helper()
	skillData := []byte(skill)
	hash := sha256.Sum256(skillData)
	record := FileRecord{Path: "SKILL.md", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(skillData)), Mode: uint32(0o644)}
	manifest := Manifest{
		Schema: ManifestSchema, Name: SkillName, ContentID: contentID([]FileRecord{record}), MinimumCLIVersion: "0.1.0",
		Channel: "development", CLIBuild: "development", Files: []FileRecord{record},
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestData = append(manifestData, '\n')
	bundle := Bundle{Manifest: manifest, Files: []File{
		{Path: "SKILL.md", Data: skillData, Mode: fs.FileMode(0o644)},
		{Path: ManagedManifestName, Data: manifestData, Mode: fs.FileMode(0o644)},
	}}
	if err := validateBundle(bundle); err != nil {
		t.Fatalf("test bundle invalid: %v", err)
	}
	return bundle
}
