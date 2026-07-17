// Package release implements the repository's single CLI release packaging path.
package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/requirements"
)

const ManifestSchema = "issue-spec.release/v1"

var (
	semanticTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	hexRevision = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)
	sha256Hex   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

//go:embed assets/install.sh assets/install.ps1
var installerAssets embed.FS

type Plan struct {
	Ref             string `json:"ref"`
	Tag             string `json:"tag"`
	Version         string `json:"version"`
	Channel         string `json:"channel"`
	Revision        string `json:"revision"`
	SourceDateEpoch int64  `json:"source_date_epoch"`
	Prerelease      bool   `json:"prerelease"`
	Latest          bool   `json:"latest"`
	Immutable       bool   `json:"immutable"`
}

type Options struct {
	Root            string
	Output          string
	Ref             string
	Revision        string
	SourceDateEpoch int64
}

type target struct {
	OS   string
	Arch string
}

func (target target) key() string { return target.OS + "/" + target.Arch }

var targets = []target{
	{OS: "linux", Arch: "amd64"},
	{OS: "linux", Arch: "arm64"},
	{OS: "darwin", Arch: "amd64"},
	{OS: "darwin", Arch: "arm64"},
	{OS: "windows", Arch: "amd64"},
	{OS: "windows", Arch: "arm64"},
}

type Asset struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	OS              string `json:"os,omitempty"`
	Arch            string `json:"arch,omitempty"`
	Size            int64  `json:"size"`
	SHA256          string `json:"sha256"`
	ExecutableEntry string `json:"executable_entry,omitempty"`
}

type Provenance struct {
	Required bool   `json:"required"`
	Kind     string `json:"kind"`
	Coverage string `json:"coverage"`
}

type Manifest struct {
	Schema                        string     `json:"schema"`
	Channel                       string     `json:"channel"`
	Version                       string     `json:"version"`
	Revision                      string     `json:"revision"`
	SourceDateEpoch               int64      `json:"source_date_epoch"`
	CLICompatibility              string     `json:"cli_compatibility"`
	RequirementsSkillContentID    string     `json:"requirements_skill_content_id"`
	RequirementsMinimumCLIVersion string     `json:"requirements_minimum_cli_version"`
	PublishedAssets               []string   `json:"published_assets"`
	Provenance                    Provenance `json:"provenance"`
	Assets                        []Asset    `json:"assets"`
}

type payload struct {
	asset Asset
	data  []byte
	mode  fs.FileMode
}

// PlanPublication is the fail-closed publication guard shared by packaging and
// workflow tests. Only main and strict semantic-version tags are eligible.
func PlanPublication(ref, revision string, sourceDateEpoch int64) (Plan, error) {
	ref = strings.TrimSpace(ref)
	revision = strings.TrimSpace(revision)
	if !hexRevision.MatchString(revision) {
		return Plan{}, errors.New("release revision must be a full 40- or 64-character lowercase hexadecimal revision")
	}
	if sourceDateEpoch < 315532800 {
		return Plan{}, errors.New("SOURCE_DATE_EPOCH must be a positive timestamp on or after 1980-01-01")
	}
	plan := Plan{Ref: ref, Revision: revision, SourceDateEpoch: sourceDateEpoch}
	switch {
	case ref == "refs/heads/main":
		plan.Tag = "rolling-" + revision
		plan.Version = fmt.Sprintf("0.0.0-main.%d+g%s", sourceDateEpoch, revision[:12])
		plan.Channel = "rolling"
		plan.Latest = true
		plan.Immutable = true
	case strings.HasPrefix(ref, "refs/tags/"):
		tag := strings.TrimPrefix(ref, "refs/tags/")
		if !validSemanticTag(tag) {
			return Plan{}, fmt.Errorf("unsupported release tag %q; expected vMAJOR.MINOR.PATCH with optional SemVer prerelease", tag)
		}
		plan.Tag = tag
		plan.Version = tag
		plan.Prerelease = strings.Contains(strings.SplitN(tag, "+", 2)[0], "-")
		if plan.Prerelease {
			plan.Channel = "prerelease"
		} else {
			plan.Channel = "stable"
		}
		plan.Immutable = true
	default:
		return Plan{}, fmt.Errorf("ref %q is not trusted for release publication", ref)
	}
	return plan, nil
}

// Package builds all six target binaries and atomically activates one verified
// release directory. There is no alternate release assembly path.
func Package(ctx context.Context, options Options) (Plan, error) {
	return packageWithGo(ctx, options, "go")
}

func packageWithGo(ctx context.Context, options Options, goBinary string) (Plan, error) {
	plan, err := PlanPublication(options.Ref, options.Revision, options.SourceDateEpoch)
	if err != nil {
		return Plan{}, err
	}
	root, err := filepath.Abs(strings.TrimSpace(options.Root))
	if err != nil || strings.TrimSpace(options.Root) == "" {
		return Plan{}, errors.New("repository root is required")
	}
	output, err := boundedOutputPath(options.Output)
	if err != nil {
		return Plan{}, err
	}
	if err := validateOutputLocation(root, output); err != nil {
		return Plan{}, err
	}
	contentID, err := requirements.ContentID()
	if err != nil {
		return Plan{}, err
	}
	buildRoot, err := os.MkdirTemp("", "issue-spec-release-build-")
	if err != nil {
		return Plan{}, err
	}
	defer os.RemoveAll(buildRoot)
	binaries := make(map[string][]byte, len(targets))
	for _, target := range targets {
		binary, err := buildCLI(ctx, root, buildRoot, goBinary, plan, contentID, target)
		if err != nil {
			return Plan{}, err
		}
		binaries[target.key()] = binary
	}
	stage, err := os.MkdirTemp(filepath.Dir(output), ".issue-spec-release-stage-")
	if err != nil {
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			return Plan{}, err
		}
		stage, err = os.MkdirTemp(filepath.Dir(output), ".issue-spec-release-stage-")
	}
	if err != nil {
		return Plan{}, err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := assemble(stage, plan, binaries); err != nil {
		return Plan{}, err
	}
	if _, err := VerifyDirectory(stage); err != nil {
		return Plan{}, fmt.Errorf("verify assembled release: %w", err)
	}
	if err := activateDirectory(stage, output); err != nil {
		return Plan{}, err
	}
	keepStage = true
	return plan, nil
}

func buildCLI(ctx context.Context, root, buildRoot, goBinary string, plan Plan, contentID string, target target) ([]byte, error) {
	name := "issue-spec-" + target.OS + "-" + target.Arch
	if target.OS == "windows" {
		name += ".exe"
	}
	output := filepath.Join(buildRoot, name)
	ldflags := strings.Join([]string{
		"-s", "-w", "-buildid=",
		"-X=github.com/higress-group/issue-spec/internal/buildinfo.version=" + plan.Version,
		"-X=github.com/higress-group/issue-spec/internal/buildinfo.channel=" + plan.Channel,
		"-X=github.com/higress-group/issue-spec/internal/buildinfo.revision=" + plan.Revision,
		"-X=github.com/higress-group/issue-spec/internal/buildinfo.sourceDateEpoch=" + strconv.FormatInt(plan.SourceDateEpoch, 10),
		"-X=github.com/higress-group/issue-spec/internal/buildinfo.requirementsSkillContentID=" + contentID,
	}, " ")
	command := exec.CommandContext(ctx, goBinary, "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", output, "./cmd/issue-spec")
	command.Dir = root
	command.Env = releaseEnvironment(os.Environ(), target, plan.SourceDateEpoch)
	combined, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("build %s: %w: %s", target.key(), err, strings.TrimSpace(string(combined)))
	}
	binary, err := os.ReadFile(output)
	if err != nil {
		return nil, fmt.Errorf("read %s binary: %w", target.key(), err)
	}
	return binary, nil
}

func assemble(root string, plan Plan, binaries map[string][]byte) error {
	payloads := make([]payload, 0, len(targets)+3)
	for _, target := range targets {
		binary, ok := binaries[target.key()]
		if !ok || len(binary) == 0 {
			return fmt.Errorf("missing built binary for %s", target.key())
		}
		archive, name, entry, err := platformArchive(target, binary, plan.SourceDateEpoch)
		if err != nil {
			return err
		}
		payloads = append(payloads, newPayload(name, "cli", target.OS, target.Arch, entry, archive, 0o644))
	}
	distribution := requirements.Distribution{Channel: plan.Channel, SourceRevision: plan.Revision, CLIBuild: plan.Version}
	skillArchive, err := requirements.BuildArchive(distribution)
	if err != nil {
		return fmt.Errorf("build requirements skill archive: %w", err)
	}
	skillHash := sha256.Sum256(skillArchive)
	skillBundle, err := requirements.VerifyArchive(skillArchive, hex.EncodeToString(skillHash[:]))
	if err != nil {
		return fmt.Errorf("verify requirements skill archive: %w", err)
	}
	payloads = append(payloads, newPayload("issue-spec-requirements.zip", "requirements-skill", "", "", requirements.SkillName+"/SKILL.md", skillArchive, 0o644))
	for _, installer := range []struct {
		path string
		name string
		mode fs.FileMode
	}{
		{path: "assets/install.sh", name: "install.sh", mode: 0o755},
		{path: "assets/install.ps1", name: "install.ps1", mode: 0o644},
	} {
		data, err := installerAssets.ReadFile(installer.path)
		if err != nil {
			return err
		}
		payloads = append(payloads, newPayload(installer.name, "installer", "", "", installer.name, data, installer.mode))
	}
	sort.Slice(payloads, func(i, j int) bool { return payloads[i].asset.Name < payloads[j].asset.Name })
	publish := filepath.Join(root, "publish")
	if err := os.MkdirAll(publish, 0o755); err != nil {
		return err
	}
	assets := make([]Asset, 0, len(payloads))
	for _, item := range payloads {
		if err := os.WriteFile(filepath.Join(publish, item.asset.Name), item.data, item.mode); err != nil {
			return err
		}
		assets = append(assets, item.asset)
	}
	publishedNames := publishAssetNames()
	manifest := Manifest{
		Schema: ManifestSchema, Channel: plan.Channel, Version: plan.Version, Revision: plan.Revision,
		SourceDateEpoch: plan.SourceDateEpoch, CLICompatibility: plan.Version,
		RequirementsSkillContentID:    skillBundle.Manifest.ContentID,
		RequirementsMinimumCLIVersion: skillBundle.Manifest.MinimumCLIVersion,
		PublishedAssets:               publishedNames,
		Provenance:                    Provenance{Required: true, Kind: "github-artifact-attestation", Coverage: "all-published-assets"},
		Assets:                        assets,
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(filepath.Join(publish, "manifest.json"), manifestData, 0o644); err != nil {
		return err
	}
	checksums := checksumText(payloads, manifestData)
	if err := os.WriteFile(filepath.Join(publish, "SHA256SUMS"), []byte(checksums), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "release-notes.md"), []byte(releaseNotes(plan)), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "release.env"), []byte(releaseEnvironmentFile(plan)), 0o644)
}

func VerifyDirectory(root string) (Manifest, error) {
	publish := filepath.Join(root, "publish")
	manifestData, err := os.ReadFile(filepath.Join(publish, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Manifest{}, errors.New("release manifest contains trailing JSON data")
	}
	if manifest.Schema != ManifestSchema || !hexRevision.MatchString(manifest.Revision) || manifest.SourceDateEpoch < 315532800 {
		return Manifest{}, errors.New("release manifest identity is invalid")
	}
	if err := validateManifestIdentity(manifest); err != nil {
		return Manifest{}, err
	}
	if !equalStrings(manifest.PublishedAssets, publishAssetNames()) {
		return Manifest{}, errors.New("release manifest published asset set is incomplete")
	}
	if !manifest.Provenance.Required || manifest.Provenance.Kind != "github-artifact-attestation" || manifest.Provenance.Coverage != "all-published-assets" {
		return Manifest{}, errors.New("release manifest does not require complete provenance coverage")
	}
	entries, err := os.ReadDir(publish)
	if err != nil {
		return Manifest{}, err
	}
	actualNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return Manifest{}, fmt.Errorf("unexpected directory in published assets: %s", entry.Name())
		}
		actualNames = append(actualNames, entry.Name())
	}
	sort.Strings(actualNames)
	if !equalStrings(actualNames, publishAssetNames()) {
		return Manifest{}, fmt.Errorf("published asset set mismatch: %v", actualNames)
	}
	wantPayloadNames := payloadAssetNames()
	if len(manifest.Assets) != len(wantPayloadNames) {
		return Manifest{}, errors.New("release manifest payload asset count is incomplete")
	}
	assetByName := make(map[string]Asset, len(manifest.Assets))
	var payloads []payload
	for _, asset := range manifest.Assets {
		if _, duplicate := assetByName[asset.Name]; duplicate {
			return Manifest{}, fmt.Errorf("duplicate manifest asset %q", asset.Name)
		}
		assetByName[asset.Name] = asset
		if err := validateAssetMetadata(asset); err != nil {
			return Manifest{}, err
		}
		data, err := os.ReadFile(filepath.Join(publish, asset.Name))
		if err != nil {
			return Manifest{}, err
		}
		hash := sha256.Sum256(data)
		if asset.Size != int64(len(data)) || asset.SHA256 != hex.EncodeToString(hash[:]) {
			return Manifest{}, fmt.Errorf("asset %s does not match manifest", asset.Name)
		}
		payloads = append(payloads, payload{asset: asset, data: data})
	}
	for _, name := range wantPayloadNames {
		if _, ok := assetByName[name]; !ok {
			return Manifest{}, fmt.Errorf("manifest is missing asset %s", name)
		}
	}
	if got, err := os.ReadFile(filepath.Join(publish, "SHA256SUMS")); err != nil || string(got) != checksumText(payloads, manifestData) {
		return Manifest{}, errors.New("SHA256SUMS does not match the release payload and manifest")
	}
	for _, target := range targets {
		asset := assetByName[platformAssetName(target)]
		data, err := os.ReadFile(filepath.Join(publish, asset.Name))
		if err != nil {
			return Manifest{}, err
		}
		if err := verifyPlatformArchive(target, data); err != nil {
			return Manifest{}, err
		}
	}
	skillAsset := assetByName["issue-spec-requirements.zip"]
	skillData, err := os.ReadFile(filepath.Join(publish, skillAsset.Name))
	if err != nil {
		return Manifest{}, err
	}
	skillBundle, err := requirements.VerifyArchive(skillData, skillAsset.SHA256)
	if err != nil {
		return Manifest{}, err
	}
	if skillBundle.Manifest.ContentID != manifest.RequirementsSkillContentID || skillBundle.Manifest.CLIBuild != manifest.CLICompatibility || skillBundle.Manifest.SourceRevision != manifest.Revision {
		return Manifest{}, errors.New("requirements skill compatibility identity does not match release manifest")
	}
	return manifest, nil
}

func platformArchive(target target, binary []byte, epoch int64) ([]byte, string, string, error) {
	entry := "issue-spec"
	if target.OS == "windows" {
		entry += ".exe"
		var output bytes.Buffer
		zw := zip.NewWriter(&output)
		header := &zip.FileHeader{Name: entry, Method: zip.Deflate, Modified: time.Unix(epoch, 0).UTC()}
		header.SetMode(0o755)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return nil, "", "", err
		}
		if _, err := writer.Write(binary); err != nil {
			return nil, "", "", err
		}
		if err := zw.Close(); err != nil {
			return nil, "", "", err
		}
		return output.Bytes(), platformAssetName(target), entry, nil
	}
	var output bytes.Buffer
	gz, err := gzip.NewWriterLevel(&output, gzip.BestCompression)
	if err != nil {
		return nil, "", "", err
	}
	gz.Header.ModTime = time.Unix(epoch, 0).UTC()
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	header := &tar.Header{Name: entry, Mode: 0o755, Size: int64(len(binary)), ModTime: time.Unix(epoch, 0).UTC(), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
	if err := tw.WriteHeader(header); err != nil {
		return nil, "", "", err
	}
	if _, err := tw.Write(binary); err != nil {
		return nil, "", "", err
	}
	if err := tw.Close(); err != nil {
		return nil, "", "", err
	}
	if err := gz.Close(); err != nil {
		return nil, "", "", err
	}
	return output.Bytes(), platformAssetName(target), entry, nil
}

func verifyPlatformArchive(target target, raw []byte) error {
	wantEntry := "issue-spec"
	if target.OS == "windows" {
		wantEntry += ".exe"
		reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil || len(reader.File) != 1 || reader.File[0].Name != wantEntry || reader.File[0].Mode().Perm() != 0o755 || reader.File[0].UncompressedSize64 == 0 {
			return fmt.Errorf("invalid %s release archive", target.key())
		}
		entry, err := reader.File[0].Open()
		if err != nil {
			return fmt.Errorf("invalid %s release archive: %w", target.key(), err)
		}
		_, copyErr := io.Copy(io.Discard, entry)
		closeErr := entry.Close()
		if copyErr != nil || closeErr != nil {
			return fmt.Errorf("invalid %s release archive payload", target.key())
		}
		return nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("invalid %s release archive: %w", target.key(), err)
	}
	defer gz.Close()
	tarReader := tar.NewReader(gz)
	header, err := tarReader.Next()
	if err != nil || header.Name != wantEntry || header.Mode != 0o755 || header.Typeflag != tar.TypeReg || header.Size == 0 {
		return fmt.Errorf("invalid %s release archive", target.key())
	}
	if _, err := io.Copy(io.Discard, tarReader); err != nil {
		return err
	}
	if _, err := tarReader.Next(); err != io.EOF {
		return fmt.Errorf("%s release archive contains unexpected entries", target.key())
	}
	return nil
}

func newPayload(name, kind, targetOS, arch, entry string, data []byte, mode fs.FileMode) payload {
	hash := sha256.Sum256(data)
	return payload{asset: Asset{Name: name, Kind: kind, OS: targetOS, Arch: arch, Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), ExecutableEntry: entry}, data: data, mode: mode}
}

func checksumText(payloads []payload, manifest []byte) string {
	records := make(map[string]string, len(payloads)+1)
	for _, item := range payloads {
		hash := sha256.Sum256(item.data)
		records[item.asset.Name] = hex.EncodeToString(hash[:])
	}
	hash := sha256.Sum256(manifest)
	records["manifest.json"] = hex.EncodeToString(hash[:])
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	sort.Strings(names)
	var output strings.Builder
	for _, name := range names {
		fmt.Fprintf(&output, "%s  %s\n", records[name], name)
	}
	return output.String()
}

func platformAssetName(target target) string {
	extension := ".tar.gz"
	if target.OS == "windows" {
		extension = ".zip"
	}
	return "issue-spec_" + target.OS + "_" + target.Arch + extension
}

func payloadAssetNames() []string {
	names := make([]string, 0, len(targets)+3)
	for _, target := range targets {
		names = append(names, platformAssetName(target))
	}
	names = append(names, "issue-spec-requirements.zip", "install.ps1", "install.sh")
	sort.Strings(names)
	return names
}

func publishAssetNames() []string {
	names := append(payloadAssetNames(), "manifest.json", "SHA256SUMS")
	sort.Strings(names)
	return names
}

func releaseNotes(plan Plan) string {
	channelDescription := "immutable stable semantic-version release"
	if plan.Channel == "prerelease" {
		channelDescription = "immutable semantic-version prerelease"
	} else if plan.Channel == "rolling" {
		channelDescription = "rolling latest snapshot from main; the tagged snapshot remains immutable"
	}
	return fmt.Sprintf(`# issue-spec %s

Channel: **%s** (%s)

Source revision: **%s**

Download `+"`manifest.json`"+`, `+"`SHA256SUMS`"+`, the installer, and the archive for your operating system into one directory. Then run:

`+"```sh"+`
chmod +x install.sh
./install.sh --asset-dir .
issue-spec version --json
`+"```"+`

`+"```powershell"+`
./install.ps1 -AssetDir .
issue-spec.exe version --json
`+"```"+`

Both installers verify the selected archive against `+"`manifest.json`"+` and `+"`SHA256SUMS`"+` before replacing an existing binary. GitHub artifact attestations cover every published asset; verify provenance with `+"`gh attestation verify <file> --repo higress-group/issue-spec`"+`.

After installation, connect a self-hosted requirements workspace with:

`+"```sh"+`
issue-spec requirements setup --server https://issue-spec.example.com --repo owner/repository --agent codex
`+"```"+`
`, plan.Version, plan.Channel, channelDescription, plan.Revision)
}

func releaseEnvironmentFile(plan Plan) string {
	return fmt.Sprintf("RELEASE_TAG=%s\nRELEASE_VERSION=%s\nRELEASE_CHANNEL=%s\nRELEASE_REVISION=%s\nRELEASE_PRERELEASE=%t\nRELEASE_LATEST=%t\n",
		plan.Tag, plan.Version, plan.Channel, plan.Revision, plan.Prerelease, plan.Latest)
}

func releaseEnvironment(base []string, target target, epoch int64) []string {
	blocked := map[string]bool{"CGO_ENABLED": true, "GOOS": true, "GOARCH": true, "SOURCE_DATE_EPOCH": true}
	result := make([]string, 0, len(base)+4)
	for _, value := range base {
		name, _, _ := strings.Cut(value, "=")
		if !blocked[name] {
			result = append(result, value)
		}
	}
	return append(result, "CGO_ENABLED=0", "GOOS="+target.OS, "GOARCH="+target.Arch, "SOURCE_DATE_EPOCH="+strconv.FormatInt(epoch, 10))
}

func boundedOutputPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("release output directory is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if absolute == filepath.Dir(absolute) {
		return "", errors.New("release output must be a bounded directory")
	}
	return absolute, nil
}

func validateOutputLocation(root, output string) error {
	relative, err := filepath.Rel(output, root)
	if err != nil {
		return err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("release output must not be the repository root or one of its ancestors")
	}
	return nil
}

func validSemanticTag(tag string) bool {
	matches := semanticTag.FindStringSubmatch(tag)
	if matches == nil || matches[4] == "" {
		return matches != nil
	}
	for _, identifier := range strings.Split(strings.TrimPrefix(matches[4], "-"), ".") {
		if len(identifier) > 1 && identifier[0] == '0' {
			allDigits := true
			for _, character := range identifier {
				if character < '0' || character > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return false
			}
		}
	}
	return true
}

func validateManifestIdentity(manifest Manifest) error {
	if manifest.Version == "" || manifest.CLICompatibility != manifest.Version || manifest.RequirementsMinimumCLIVersion == "" || !strings.HasPrefix(manifest.RequirementsSkillContentID, "sha256:") {
		return errors.New("release manifest compatibility identity is incomplete")
	}
	switch manifest.Channel {
	case "rolling":
		want := fmt.Sprintf("0.0.0-main.%d+g%s", manifest.SourceDateEpoch, manifest.Revision[:12])
		if manifest.Version != want {
			return errors.New("rolling release manifest version does not match revision and source time")
		}
	case "stable", "prerelease":
		if !validSemanticTag(manifest.Version) {
			return errors.New("semantic release manifest version is invalid")
		}
		isPrerelease := strings.Contains(strings.SplitN(manifest.Version, "+", 2)[0], "-")
		if (manifest.Channel == "prerelease") != isPrerelease {
			return errors.New("semantic release manifest channel does not match its version")
		}
	default:
		return fmt.Errorf("unsupported release manifest channel %q", manifest.Channel)
	}
	return nil
}

func validateAssetMetadata(asset Asset) error {
	if asset.Size <= 0 || !sha256Hex.MatchString(asset.SHA256) {
		return fmt.Errorf("manifest asset %q has invalid size or digest", asset.Name)
	}
	if asset.Name == "issue-spec-requirements.zip" {
		if asset.Kind != "requirements-skill" || asset.OS != "" || asset.Arch != "" || asset.ExecutableEntry != requirements.SkillName+"/SKILL.md" {
			return errors.New("requirements skill asset metadata is invalid")
		}
		return nil
	}
	if asset.Name == "install.sh" || asset.Name == "install.ps1" {
		if asset.Kind != "installer" || asset.OS != "" || asset.Arch != "" || asset.ExecutableEntry != asset.Name {
			return fmt.Errorf("installer asset metadata is invalid for %s", asset.Name)
		}
		return nil
	}
	for _, target := range targets {
		if asset.Name == platformAssetName(target) {
			entry := "issue-spec"
			if target.OS == "windows" {
				entry += ".exe"
			}
			if asset.Kind != "cli" || asset.OS != target.OS || asset.Arch != target.Arch || asset.ExecutableEntry != entry {
				return fmt.Errorf("CLI asset metadata is invalid for %s", asset.Name)
			}
			return nil
		}
	}
	return fmt.Errorf("manifest contains unsupported asset %q", asset.Name)
}

func activateDirectory(stage, target string) error {
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return os.Rename(stage, target)
	} else if err != nil {
		return err
	}
	backup, err := os.MkdirTemp(filepath.Dir(target), ".issue-spec-release-backup-")
	if err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	if err := os.Rename(target, backup); err != nil {
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	return os.RemoveAll(backup)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
