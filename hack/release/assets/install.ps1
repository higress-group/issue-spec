param(
  [string]$AssetDir,
  [string]$Tag,
  [switch]$Latest,
  [string]$BaseUrl = "https://github.com/higress-group/issue-spec/releases",
  [string]$InstallDir,
  [ValidateSet("amd64", "arm64")]
  [string]$Architecture = $(if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" })
)
$ErrorActionPreference = "Stop"

if (-not $InstallDir) {
  if (-not $env:LOCALAPPDATA) { throw "LOCALAPPDATA is required when -InstallDir is not provided" }
  $InstallDir = Join-Path $env:LOCALAPPDATA "issue-spec\bin"
}
$modeCount = @($AssetDir, $Tag, $(if ($Latest) { "latest" } else { $null })) | Where-Object { $_ } | Measure-Object | Select-Object -ExpandProperty Count
if ($modeCount -ne 1) { throw "choose exactly one of -Tag, -Latest, or -AssetDir" }
if ($Tag -and $Tag -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-+][0-9A-Za-z.-]+)?$') {
  throw "-Tag must be a semantic version tag such as v1.2.3"
}

$asset = "issue-spec_windows_$Architecture.zip"
$downloadDir = $null
if ($Tag -or $Latest) {
  $downloadDir = Join-Path ([IO.Path]::GetTempPath()) ("issue-spec-download-" + [guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Path $downloadDir | Out-Null
  $AssetDir = $downloadDir
  $downloadUrl = if ($Tag) { $BaseUrl.TrimEnd('/') + "/download/$Tag" } else { $BaseUrl.TrimEnd('/') + "/latest/download" }
  try {
    foreach ($name in @("manifest.json", "SHA256SUMS", $asset)) {
      & curl.exe -fL --retry 2 --output (Join-Path $AssetDir $name) "$downloadUrl/$name"
      if ($LASTEXITCODE -ne 0) { throw "curl.exe failed to download $name" }
    }
  } catch {
    Remove-Item -LiteralPath $downloadDir -Recurse -Force -ErrorAction SilentlyContinue
    throw
  }
}
$stage = $null
$stagedBinary = $null
try {
$archive = Join-Path $AssetDir $asset
$manifestPath = Join-Path $AssetDir "manifest.json"
$checksumsPath = Join-Path $AssetDir "SHA256SUMS"
foreach ($required in @($archive, $manifestPath, $checksumsPath)) {
  if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "missing release file: $required" }
}

$manifestChecksumLines = @(Get-Content -LiteralPath $checksumsPath | Where-Object { $_ -match "^[0-9a-f]{64}  manifest.json$" })
if ($manifestChecksumLines.Count -ne 1) { throw "manifest is not uniquely covered by SHA256SUMS" }
$manifestExpected = ($manifestChecksumLines[0] -split "  ")[0]
$manifestActual = (Get-FileHash -LiteralPath $manifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($manifestActual -ne $manifestExpected) { throw "integrity verification failed for manifest.json" }
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
if ($manifest.schema -ne "issue-spec.release/v1") { throw "unsupported release manifest schema" }
$records = @($manifest.assets | Where-Object { $_.name -eq $asset })
if ($records.Count -ne 1) { throw "asset is not uniquely covered by manifest.json: $asset" }
$checksumLines = @(Get-Content -LiteralPath $checksumsPath | Where-Object { $_ -match "^[0-9a-f]{64}  $([regex]::Escape($asset))$" })
if ($checksumLines.Count -ne 1) { throw "asset is not uniquely covered by SHA256SUMS: $asset" }
$expected = ($checksumLines[0] -split "  ")[0]
if ($records[0].sha256 -ne $expected) { throw "manifest and SHA256SUMS disagree for $asset" }
$actual = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -ne $expected) { throw "integrity verification failed for $asset" }

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$stage = Join-Path $InstallDir (".issue-spec-stage-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $stage | Out-Null
  Expand-Archive -LiteralPath $archive -DestinationPath $stage
  $candidate = Join-Path $stage "issue-spec.exe"
  if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) { throw "verified archive does not contain issue-spec.exe" }
  $identity = (& $candidate version --json | Out-String) | ConvertFrom-Json
  if ($LASTEXITCODE -ne 0) { throw "verified issue-spec binary failed its version check" }
  if ($identity.version -ne $manifest.version -or $identity.revision -ne $manifest.revision) { throw "verified issue-spec identity does not match manifest.json" }
  $stagedBinary = Join-Path $InstallDir (".issue-spec.new." + [guid]::NewGuid().ToString("N") + ".exe")
  Copy-Item -LiteralPath $candidate -Destination $stagedBinary
  $destination = Join-Path $InstallDir "issue-spec.exe"
  if (Test-Path -LiteralPath $destination -PathType Leaf) {
    [System.IO.File]::Replace($stagedBinary, $destination, $null, $true)
  } else {
    [System.IO.File]::Move($stagedBinary, $destination)
  }
  $stagedBinary = $null
  Write-Output "installed $destination from $asset"
} finally {
  if ($null -ne $stagedBinary -and (Test-Path -LiteralPath $stagedBinary)) { Remove-Item -LiteralPath $stagedBinary -Force -ErrorAction SilentlyContinue }
  if ($null -ne $stage) { Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue }
  if ($null -ne $downloadDir) { Remove-Item -LiteralPath $downloadDir -Recurse -Force -ErrorAction SilentlyContinue }
}
