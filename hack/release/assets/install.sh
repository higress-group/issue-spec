#!/bin/sh
set -eu

asset_dir=
install_dir=$HOME/.local/bin
target_os=
target_arch=
release_tag=
latest=false
base_url=https://github.com/higress-group/issue-spec/releases

while [ "$#" -gt 0 ]; do
  case "$1" in
    --asset-dir) asset_dir=$2; shift 2 ;;
    --tag) release_tag=$2; shift 2 ;;
    --latest) latest=true; shift ;;
    --base-url) base_url=$2; shift 2 ;;
    --install-dir) install_dir=$2; shift 2 ;;
    --os) target_os=$2; shift 2 ;;
    --arch) target_arch=$2; shift 2 ;;
    --help)
      echo "usage: install.sh (--tag vMAJOR.MINOR.PATCH | --latest | --asset-dir DIR) [--install-dir DIR] [--os linux|darwin] [--arch amd64|arm64]"
      exit 0
      ;;
    *) echo "install.sh: unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$target_os" ]; then
  case $(uname -s) in
    Linux) target_os=linux ;;
    Darwin) target_os=darwin ;;
    *) echo "install.sh: unsupported operating system" >&2; exit 1 ;;
  esac
fi
if [ -z "$target_arch" ]; then
  case $(uname -m) in
    x86_64|amd64) target_arch=amd64 ;;
    arm64|aarch64) target_arch=arm64 ;;
    *) echo "install.sh: unsupported architecture" >&2; exit 1 ;;
  esac
fi
case "$target_os/$target_arch" in
  linux/amd64|linux/arm64|darwin/amd64|darwin/arm64) ;;
  *) echo "install.sh: unsupported target $target_os/$target_arch" >&2; exit 1 ;;
esac

asset="issue-spec_"$target_os"_"$target_arch".tar.gz"
download_dir=
cleanup_download() {
  if [ -n "$download_dir" ] && [ -d "$download_dir" ]; then rm -R "$download_dir"; fi
}
trap cleanup_download EXIT HUP INT TERM

mode_count=0
if [ -n "$asset_dir" ]; then mode_count=$((mode_count + 1)); fi
if [ -n "$release_tag" ]; then mode_count=$((mode_count + 1)); fi
if [ "$latest" = true ]; then mode_count=$((mode_count + 1)); fi
if [ "$mode_count" -ne 1 ]; then
  echo "install.sh: choose exactly one of --tag, --latest, or --asset-dir" >&2
  exit 2
fi
if [ -n "$release_tag" ]; then
  if ! printf '%s\n' "$release_tag" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-+][0-9A-Za-z.-]+)?$'; then
    echo "install.sh: --tag must be a semantic version tag such as v1.2.3" >&2
    exit 2
  fi
  download_url=${base_url%/}/download/$release_tag
elif [ "$latest" = true ]; then
  download_url=${base_url%/}/latest/download
fi
if [ -n "$release_tag" ] || [ "$latest" = true ]; then
  download_dir=$(mktemp -d "${TMPDIR:-/tmp}/issue-spec-download.XXXXXX")
  asset_dir=$download_dir
  if ! command -v curl >/dev/null 2>&1; then
    echo "install.sh: curl is required for release downloads" >&2
    exit 1
  fi
  for name in manifest.json SHA256SUMS "$asset"; do
    curl -fL --retry 2 --output "$asset_dir/$name" "$download_url/$name"
  done
fi

archive=$asset_dir/$asset
manifest=$asset_dir/manifest.json
checksums=$asset_dir/SHA256SUMS
for required in "$archive" "$manifest" "$checksums"; do
  if [ ! -f "$required" ]; then
    echo "install.sh: missing release file: $required" >&2
    exit 1
  fi
done

expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$checksums")
if [ -z "$expected" ] || [ "$(printf '%s\n' "$expected" | wc -l | tr -d ' ')" -ne 1 ]; then
  echo "install.sh: asset is not uniquely covered by SHA256SUMS" >&2
  exit 1
fi
manifest_expected=$(awk '$2 == "manifest.json" { print $1 }' "$checksums")
if [ -z "$manifest_expected" ] || [ "$(printf '%s\n' "$manifest_expected" | wc -l | tr -d ' ')" -ne 1 ]; then
  echo "install.sh: manifest is not uniquely covered by SHA256SUMS" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  manifest_actual=$(sha256sum "$manifest" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  manifest_actual=$(shasum -a 256 "$manifest" | awk '{ print $1 }')
else
  echo "install.sh: sha256sum or shasum is required" >&2
  exit 1
fi
if [ "$manifest_actual" != "$manifest_expected" ]; then
  echo "install.sh: integrity verification failed for manifest.json" >&2
  exit 1
fi
if ! grep -F '"schema": "issue-spec.release/v1"' "$manifest" >/dev/null; then
  echo "install.sh: unsupported or invalid release manifest" >&2
  exit 1
fi
manifest_hash=$(awk -v name="$asset" '
  index($0, "\"name\": \"" name "\"") { found=1; next }
  found && index($0, "\"sha256\":") {
    value=$0
    sub(/^.*\"sha256\": \"/, "", value)
    sub(/\".*$/, "", value)
    print value
    exit
  }
' "$manifest")
if [ "$manifest_hash" != "$expected" ]; then
  echo "install.sh: manifest and SHA256SUMS disagree for $asset" >&2
  exit 1
fi
manifest_version=$(awk 'index($0, "\"version\":") { value=$0; sub(/^.*\"version\": \"/, "", value); sub(/\".*$/, "", value); print value; exit }' "$manifest")
manifest_revision=$(awk 'index($0, "\"revision\":") { value=$0; sub(/^.*\"revision\": \"/, "", value); sub(/\".*$/, "", value); print value; exit }' "$manifest")
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$archive" | awk '{ print $1 }')
else
  echo "install.sh: sha256sum or shasum is required" >&2
  exit 1
fi
if [ "$actual" != "$expected" ]; then
  echo "install.sh: integrity verification failed for $asset" >&2
  exit 1
fi

mkdir -p "$install_dir"
stage=$(mktemp -d "$install_dir/.issue-spec-stage.XXXXXX")
staged_binary=
cleanup() {
  if [ -d "$stage" ]; then rm -R "$stage"; fi
  if [ -n "$staged_binary" ] && [ -f "$staged_binary" ]; then rm -f "$staged_binary"; fi
  cleanup_download
}
trap cleanup EXIT HUP INT TERM
tar -xzf "$archive" -C "$stage"
candidate=$stage/issue-spec
if [ ! -f "$candidate" ]; then
  echo "install.sh: verified archive does not contain issue-spec" >&2
  exit 1
fi
chmod 755 "$candidate"
version_output=$("$candidate" version --json 2>/dev/null) || {
  echo "install.sh: verified issue-spec binary failed its version check" >&2
  exit 1
}
if ! printf '%s\n' "$version_output" | grep -F "\"version\": \"$manifest_version\"" >/dev/null ||
   ! printf '%s\n' "$version_output" | grep -F "\"revision\": \"$manifest_revision\"" >/dev/null; then
  echo "install.sh: verified issue-spec identity does not match manifest.json" >&2
  exit 1
fi
staged_binary=$install_dir/.issue-spec.new.$$
cp "$candidate" "$staged_binary"
chmod 755 "$staged_binary"
mv -f "$staged_binary" "$install_dir/issue-spec"
staged_binary=
echo "installed $install_dir/issue-spec from $asset"
