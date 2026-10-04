# Helm plugin install/update hook (Windows): download the forge release binary
# that matches plugin.yaml's version and verify it against checksums.txt.
# HELM_FORGE_PLUGIN_URL overrides the release base URL (mirrors, CI smoke test).
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$dir = if ($env:HELM_PLUGIN_DIR) { $env:HELM_PLUGIN_DIR } else { Split-Path -Parent $PSScriptRoot }
$line = Select-String -Path (Join-Path $dir 'plugin.yaml') -Pattern '^version:\s*"?([^"\s]+)"?\s*$' | Select-Object -First 1
if (-not $line) { Write-Error "forge: no version in $dir\plugin.yaml" }
$version = $line.Matches[0].Groups[1].Value

$base = if ($env:HELM_FORGE_PLUGIN_URL) { $env:HELM_FORGE_PLUGIN_URL } else { 'https://github.com/mmpyro/forge/releases/download' }
$base = $base.TrimEnd('/') + "/v$version"

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { Write-Error "forge: unsupported architecture $env:PROCESSOR_ARCHITECTURE" }
}
$asset = "forge-windows-$arch.exe"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "forge: downloading $asset v$version"
  Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset)
  Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

  $want = $null
  foreach ($l in Get-Content (Join-Path $tmp 'checksums.txt')) {
    $f = $l -split '\s+'
    if ($f.Count -ge 2 -and ($f[1] -eq $asset -or $f[1] -eq "*$asset")) { $want = $f[0].ToLower() }
  }
  if (-not $want) { Write-Error "forge: $asset is not listed in checksums.txt" }
  $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash.ToLower()
  if ($got -ne $want) { Write-Error "forge: checksum mismatch for ${asset}: got $got, want $want" }

  $bin = Join-Path $dir 'bin'
  New-Item -ItemType Directory -Force -Path $bin | Out-Null
  Move-Item -Force (Join-Path $tmp $asset) (Join-Path $bin 'forge.exe')
  Write-Host "forge: installed $(& (Join-Path $bin 'forge.exe') --version)"
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
