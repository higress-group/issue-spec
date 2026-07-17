param(
  [string]$AssetDir = $PSScriptRoot,
  [string]$InstallDir = (Join-Path $HOME ".local/bin"),
  [ValidateSet("amd64", "arm64")]
  [string]$Architecture = $(if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" })
)
$ErrorActionPreference = "Stop"

$asset = "issue-spec_windows_$Architecture.zip"
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
$stagedBinary = $null
New-Item -ItemType Directory -Path $stage | Out-Null
try {
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
  Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
}
