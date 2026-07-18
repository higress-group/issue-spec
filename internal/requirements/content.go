// Package requirements owns the one canonical issue-spec requirements skill,
// its distribution identity, and conflict-safe installation primitives.
package requirements

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	SkillName           = "issue-spec-requirements"
	ManagedManifestName = "issue-spec-managed.json"
	ManifestSchema      = "issue-spec.requirements-skill/v1"

	canonicalAssetRoot = "assets/issue-spec-requirements"
	archiveSizeLimit   = 4 << 20
)

//go:embed assets/issue-spec-requirements/*
var canonicalAssets embed.FS

// Distribution identifies the CLI build carrying a canonical skill. It does
// not participate in ContentID: stable skill bytes have the same identity in
// the embedded CLI and the standalone archive for a release.
type Distribution struct {
	Channel        string
	SourceRevision string
	CLIBuild       string
}

// Manifest is the only managed state installed with the skill. It deliberately
// contains no server, repository, context, or credential fields.
type Manifest struct {
	Schema            string       `json:"schema"`
	Name              string       `json:"name"`
	ContentID         string       `json:"content_id"`
	MinimumCLIVersion string       `json:"minimum_cli_version"`
	Channel           string       `json:"channel"`
	SourceRevision    string       `json:"source_revision"`
	CLIBuild          string       `json:"cli_build"`
	Files             []FileRecord `json:"files"`
}

// FileRecord binds one installed file to the managed manifest.
type FileRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

// File is one canonical installed file. Paths are slash-separated and relative
// to the skill directory.
type File struct {
	Path string
	Data []byte
	Mode fs.FileMode
}

// Bundle is the complete installable tree, including its in-skill manifest.
type Bundle struct {
	Manifest Manifest
	Files    []File
}

// Canonical returns the embedded canonical tree with the supplied release
// identity. Supplying a zero Distribution returns the development identity
// checked into the source tree.
func Canonical(distribution Distribution) (Bundle, error) {
	manifestData, err := canonicalAssets.ReadFile(canonicalAssetRoot + "/" + ManagedManifestName)
	if err != nil {
		return Bundle{}, fmt.Errorf("read canonical manifest: %w", err)
	}
	manifest, err := decodeManifest(manifestData)
	if err != nil {
		return Bundle{}, fmt.Errorf("decode canonical manifest: %w", err)
	}
	if err := validateCanonicalAssetSet(manifest); err != nil {
		return Bundle{}, err
	}

	files := make([]File, 0, len(manifest.Files)+1)
	for _, record := range manifest.Files {
		data, err := canonicalAssets.ReadFile(canonicalAssetRoot + "/" + record.Path)
		if err != nil {
			return Bundle{}, fmt.Errorf("read canonical file %q: %w", record.Path, err)
		}
		files = append(files, File{Path: record.Path, Data: data, Mode: fs.FileMode(record.Mode)})
	}
	if err := validateManagedFiles(manifest, files); err != nil {
		return Bundle{}, fmt.Errorf("invalid canonical skill: %w", err)
	}

	if distribution != (Distribution{}) {
		manifest.Channel = strings.TrimSpace(distribution.Channel)
		manifest.SourceRevision = strings.TrimSpace(distribution.SourceRevision)
		manifest.CLIBuild = strings.TrimSpace(distribution.CLIBuild)
	}
	if err := validateDistribution(manifest); err != nil {
		return Bundle{}, err
	}
	manifestData, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Bundle{}, fmt.Errorf("encode managed manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')
	files = append(files, File{Path: ManagedManifestName, Data: manifestData, Mode: 0o644})
	sortFiles(files)
	return Bundle{Manifest: manifest, Files: files}, nil
}

// ContentID returns the stable identity of the canonical skill content.
func ContentID() (string, error) {
	bundle, err := Canonical(Distribution{})
	if err != nil {
		return "", err
	}
	return bundle.Manifest.ContentID, nil
}

// BuildArchive produces the deterministic standalone ZIP from the same Bundle
// used by CLI installation.
func BuildArchive(distribution Distribution) ([]byte, error) {
	bundle, err := Canonical(distribution)
	if err != nil {
		return nil, err
	}
	return ArchiveBundle(bundle)
}

// ArchiveBundle serializes a validated bundle as a deterministic ZIP.
func ArchiveBundle(bundle Bundle) ([]byte, error) {
	if err := validateBundle(bundle); err != nil {
		return nil, err
	}
	files := cloneFiles(bundle.Files)
	sortFiles(files)
	var output bytes.Buffer
	zw := zip.NewWriter(&output)
	for _, file := range files {
		header := &zip.FileHeader{
			Name:     SkillName + "/" + file.Path,
			Method:   zip.Deflate,
			Modified: time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC),
		}
		header.SetMode(file.Mode)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("create archive entry %q: %w", file.Path, err)
		}
		if _, err := writer.Write(file.Data); err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("write archive entry %q: %w", file.Path, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close skill archive: %w", err)
	}
	return output.Bytes(), nil
}

// VerifyArchive validates a standalone ZIP against an optional release SHA-256
// and the canonical content embedded in this CLI. The archive may carry a
// different release identity, but not different skill bytes.
func VerifyArchive(raw []byte, expectedSHA256 string) (Bundle, error) {
	if len(raw) == 0 || len(raw) > archiveSizeLimit {
		return Bundle{}, fmt.Errorf("requirements skill archive size must be between 1 and %d bytes", archiveSizeLimit)
	}
	if expectedSHA256 != "" {
		want, err := normalizeSHA256(expectedSHA256)
		if err != nil {
			return Bundle{}, fmt.Errorf("invalid expected archive checksum: %w", err)
		}
		got := sha256.Sum256(raw)
		if hex.EncodeToString(got[:]) != want {
			return Bundle{}, errors.New("requirements skill archive checksum mismatch")
		}
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return Bundle{}, fmt.Errorf("open requirements skill archive: %w", err)
	}
	files := make([]File, 0, len(zr.File))
	seen := map[string]struct{}{}
	var total uint64
	for _, archived := range zr.File {
		if archived.FileInfo().IsDir() {
			continue
		}
		if archived.UncompressedSize64 > archiveSizeLimit || total+archived.UncompressedSize64 > archiveSizeLimit {
			return Bundle{}, errors.New("requirements skill archive expands beyond the size limit")
		}
		total += archived.UncompressedSize64
		prefix := SkillName + "/"
		if !strings.HasPrefix(archived.Name, prefix) {
			return Bundle{}, fmt.Errorf("archive entry %q is outside %s", archived.Name, SkillName)
		}
		relative := strings.TrimPrefix(archived.Name, prefix)
		if err := validateRelativePath(relative); err != nil {
			return Bundle{}, fmt.Errorf("invalid archive entry %q: %w", archived.Name, err)
		}
		if _, ok := seen[relative]; ok {
			return Bundle{}, fmt.Errorf("duplicate archive entry %q", relative)
		}
		seen[relative] = struct{}{}
		mode := archived.Mode()
		if !mode.IsRegular() {
			return Bundle{}, fmt.Errorf("archive entry %q is not a regular file", relative)
		}
		reader, err := archived.Open()
		if err != nil {
			return Bundle{}, fmt.Errorf("open archive entry %q: %w", relative, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, archiveSizeLimit+1))
		closeErr := reader.Close()
		if readErr != nil {
			return Bundle{}, fmt.Errorf("read archive entry %q: %w", relative, readErr)
		}
		if closeErr != nil {
			return Bundle{}, fmt.Errorf("close archive entry %q: %w", relative, closeErr)
		}
		if len(data) > archiveSizeLimit {
			return Bundle{}, fmt.Errorf("archive entry %q exceeds the size limit", relative)
		}
		files = append(files, File{Path: relative, Data: data, Mode: mode.Perm()})
	}
	manifestFile, ok := fileNamed(files, ManagedManifestName)
	if !ok {
		return Bundle{}, errors.New("requirements skill archive is missing its managed manifest")
	}
	manifest, err := decodeManifest(manifestFile.Data)
	if err != nil {
		return Bundle{}, fmt.Errorf("decode archived managed manifest: %w", err)
	}
	bundle := Bundle{Manifest: manifest, Files: files}
	if err := validateBundle(bundle); err != nil {
		return Bundle{}, fmt.Errorf("invalid requirements skill archive: %w", err)
	}
	canonicalID, err := ContentID()
	if err != nil {
		return Bundle{}, err
	}
	if manifest.ContentID != canonicalID {
		return Bundle{}, fmt.Errorf("archive content ID %q does not match embedded content ID %q", manifest.ContentID, canonicalID)
	}
	sortFiles(bundle.Files)
	return bundle, nil
}

// CheckCompatibility reports whether cliVersion satisfies the bundle's minimum
// supported CLI version. Development builds are accepted only for development
// skill bundles.
func (bundle Bundle) CheckCompatibility(cliVersion string) error {
	cliVersion = strings.TrimSpace(cliVersion)
	if cliVersion == "development" {
		if bundle.Manifest.Channel == "development" {
			return nil
		}
		return errors.New("a development CLI cannot install a published requirements skill")
	}
	current, err := parseVersion(cliVersion)
	if err != nil {
		return fmt.Errorf("parse CLI version: %w", err)
	}
	minimum, err := parseVersion(bundle.Manifest.MinimumCLIVersion)
	if err != nil {
		return fmt.Errorf("parse skill minimum CLI version: %w", err)
	}
	if compareVersion(current, minimum) < 0 {
		return fmt.Errorf("CLI %s is older than the skill minimum %s", cliVersion, bundle.Manifest.MinimumCLIVersion)
	}
	return nil
}

func validateBundle(bundle Bundle) error {
	if err := validateDistribution(bundle.Manifest); err != nil {
		return err
	}
	manifestFile, ok := fileNamed(bundle.Files, ManagedManifestName)
	if !ok {
		return errors.New("bundle is missing its managed manifest")
	}
	decoded, err := decodeManifest(manifestFile.Data)
	if err != nil {
		return fmt.Errorf("decode bundle manifest: %w", err)
	}
	if !manifestsEqual(decoded, bundle.Manifest) {
		return errors.New("bundle manifest metadata does not match its manifest file")
	}
	managed := make([]File, 0, len(bundle.Files)-1)
	for _, file := range bundle.Files {
		if file.Path != ManagedManifestName {
			managed = append(managed, file)
		}
	}
	return validateManagedFiles(bundle.Manifest, managed)
}

func validateManagedFiles(manifest Manifest, files []File) error {
	if manifest.Schema != ManifestSchema {
		return fmt.Errorf("unsupported managed manifest schema %q", manifest.Schema)
	}
	if manifest.Name != SkillName {
		return fmt.Errorf("unexpected skill name %q", manifest.Name)
	}
	if len(manifest.Files) == 0 || len(manifest.Files) != len(files) {
		return errors.New("managed manifest file set does not match the skill tree")
	}
	records := append([]FileRecord(nil), manifest.Files...)
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	actual := cloneFiles(files)
	sortFiles(actual)
	for index, record := range records {
		if err := validateRelativePath(record.Path); err != nil {
			return fmt.Errorf("invalid managed path %q: %w", record.Path, err)
		}
		if record.Path == ManagedManifestName {
			return errors.New("managed manifest must not hash itself")
		}
		file := actual[index]
		if file.Path != record.Path {
			return fmt.Errorf("managed file mismatch: manifest has %q, tree has %q", record.Path, file.Path)
		}
		if file.Mode.Perm() != fs.FileMode(record.Mode).Perm() || !file.Mode.IsRegular() {
			return fmt.Errorf("managed file %q mode mismatch", record.Path)
		}
		hash := sha256.Sum256(file.Data)
		if int64(len(file.Data)) != record.Size || hex.EncodeToString(hash[:]) != record.SHA256 {
			return fmt.Errorf("managed file %q content mismatch", record.Path)
		}
	}
	computed := contentID(records)
	if manifest.ContentID != computed {
		return fmt.Errorf("managed content ID mismatch: manifest has %q, computed %q", manifest.ContentID, computed)
	}
	return nil
}

func validateDistribution(manifest Manifest) error {
	switch manifest.Channel {
	case "development":
	case "stable", "prerelease", "rolling":
		if strings.TrimSpace(manifest.SourceRevision) == "" || strings.TrimSpace(manifest.CLIBuild) == "" {
			return fmt.Errorf("%s skill identity requires source revision and CLI build", manifest.Channel)
		}
		if !isFullRevision(manifest.SourceRevision) {
			return fmt.Errorf("%s skill identity requires a full hexadecimal source revision", manifest.Channel)
		}
	default:
		return fmt.Errorf("unsupported requirements skill channel %q", manifest.Channel)
	}
	if strings.TrimSpace(manifest.MinimumCLIVersion) == "" {
		return errors.New("managed manifest is missing minimum CLI version")
	}
	return nil
}

func validateCanonicalAssetSet(manifest Manifest) error {
	expected := map[string]struct{}{ManagedManifestName: {}}
	for _, record := range manifest.Files {
		expected[record.Path] = struct{}{}
	}
	return fs.WalkDir(canonicalAssets, canonicalAssetRoot, func(assetPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(assetPath, canonicalAssetRoot), "/")
		if _, ok := expected[relative]; !ok {
			return fmt.Errorf("canonical skill asset %q is not declared by its managed manifest", relative)
		}
		delete(expected, relative)
		return nil
	})
}

func isFullRevision(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func contentID(records []FileRecord) string {
	records = append([]FileRecord(nil), records...)
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	hash := sha256.New()
	for _, record := range records {
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%04o\n", record.Path, record.SHA256, record.Size, record.Mode)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func decodeManifest(raw []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Manifest{}, errors.New("managed manifest has trailing JSON data")
	}
	return manifest, nil
}

func manifestsEqual(left, right Manifest) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func validateRelativePath(value string) error {
	if value == "" || value != path.Clean(value) || path.IsAbs(value) || strings.HasPrefix(value, "../") || strings.Contains(value, "\\") {
		return errors.New("path must be a clean slash-separated relative path")
	}
	return nil
}

func fileNamed(files []File, name string) (File, bool) {
	for _, file := range files {
		if file.Path == name {
			return file, true
		}
	}
	return File{}, false
}

func sortFiles(files []File) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}

func cloneFiles(files []File) []File {
	cloned := make([]File, len(files))
	for index, file := range files {
		cloned[index] = File{Path: file.Path, Data: append([]byte(nil), file.Data...), Mode: file.Mode}
	}
	return cloned
}

func normalizeSHA256(value string) (string, error) {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("checksum must be a 64-character SHA-256 hex value")
	}
	return value, nil
}

type semanticVersion [3]int

func parseVersion(value string) (semanticVersion, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if cut, _, ok := strings.Cut(value, "+"); ok {
		value = cut
	}
	if cut, _, ok := strings.Cut(value, "-"); ok {
		value = cut
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return semanticVersion{}, errors.New("version must contain major.minor.patch")
	}
	var result semanticVersion
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return semanticVersion{}, errors.New("version components must be non-negative integers")
		}
		result[index] = parsed
	}
	return result, nil
}

func compareVersion(left, right semanticVersion) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}
