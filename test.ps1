param([string]$Go)
$ErrorActionPreference='Stop'
if(!$Go){$Go=(Get-Command go.exe -ErrorAction Stop).Source}
$env:GOTOOLCHAIN='local'
$env:CGO_ENABLED='0'
$env:GOPATH=Join-Path $PSScriptRoot '.toolchains\gopath'
$env:GOCACHE=Join-Path $PSScriptRoot '.toolchains\gocache'
# Native atlas dependencies use legacy Windows file APIs. Keeping temporary
# test paths out of a deeply nested checkout avoids exceeding their path limit.
$env:GOTMPDIR=[IO.Path]::GetTempPath()
$env:HD2_NATIVE_PIPELINE_TEST='1'
New-Item -ItemType Directory -Force -Path $env:GOPATH,$env:GOCACHE|Out-Null
Push-Location (Join-Path $PSScriptRoot 'native_go')
try {
    & $Go test ./... -count=1
    if($LASTEXITCODE){throw 'Native tests failed'}
    & $Go vet ./...
    if($LASTEXITCODE){throw 'Native static checks failed'}
} finally {Pop-Location}
& (Join-Path $PSScriptRoot 'build.ps1') -Go $Go -Test
