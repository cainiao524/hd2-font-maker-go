param([switch]$Test, [string]$Go)
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$nativeRoot = Split-Path $PSScriptRoot -Parent
$cacheRoot = $projectRoot
if (!(Test-Path -LiteralPath (Join-Path $projectRoot '.toolchains'))) { $cacheRoot = $nativeRoot }
$vendorRoot = Join-Path $projectRoot 'vendor'
if (!(Test-Path -LiteralPath $vendorRoot)) { $vendorRoot = Join-Path $nativeRoot 'vendor' }
$env:GOPATH = Join-Path $cacheRoot '.toolchains/gopath'
$env:GOCACHE = Join-Path $cacheRoot '.toolchains/gocache'
$env:GOTMPDIR = $env:GOCACHE
New-Item -ItemType Directory -Force -Path $env:GOPATH,$env:GOCACHE | Out-Null
$env:GOTOOLCHAIN = 'local'
if (!$Go) { $Go = Join-Path $projectRoot '.toolchains/go1.27.2/go/bin/go.exe' }
if (!(Test-Path -LiteralPath $Go)) { $Go = (Get-Command go.exe -ErrorAction Stop).Source }
$go = $Go
$env:CGO_ENABLED = '0'
Push-Location $PSScriptRoot
try {
    $outputBin = Join-Path $PSScriptRoot 'bin'
    New-Item -ItemType Directory -Force -Path $outputBin | Out-Null
    foreach ($dll in @('hd2harfbuzz.dll', 'hd2skia.dll', 'skia.dll')) {
        Copy-Item -LiteralPath (Join-Path $PSScriptRoot "../bin/$dll") -Destination (Join-Path $outputBin $dll)
    }
    $atlasDir = Join-Path $outputBin 'vendor/msdf-atlas-gen'
    New-Item -ItemType Directory -Force -Path $atlasDir | Out-Null
    Copy-Item -LiteralPath (Join-Path $vendorRoot 'msdf-atlas-gen/msdf-atlas-gen.exe') -Destination $atlasDir
    foreach ($license in @('LICENSE-msdf-atlas-gen.txt', 'LICENSE-msdfgen.txt')) {
        Copy-Item -LiteralPath (Join-Path $vendorRoot $license) -Destination (Join-Path $outputBin 'vendor')
    }
    $notices = Join-Path $nativeRoot 'THIRD_PARTY_RELEASE_NOTICES.txt'
    if (!(Test-Path -LiteralPath $notices)) { $notices = Join-Path $nativeRoot 'THIRD-PARTY-NOTICES.txt' }
    Copy-Item -LiteralPath $notices -Destination (Join-Path $outputBin 'NATIVE_THIRD_PARTY_NOTICES.txt')
    $hbLicense = Get-ChildItem -LiteralPath (Join-Path $nativeRoot 'native/harfbuzz/licenses') -File | Where-Object { $_.Name -match 'COPYING' } | Select-Object -First 1
    Copy-Item -LiteralPath $hbLicense.FullName -Destination (Join-Path $outputBin 'HARFBUZZ-LICENSE.txt')
    if ($Test) {
        & $go test . ./production ./cmd/hd2font-mygo
        if ($LASTEXITCODE) { throw 'MyGo tests failed' }
        & $go vet . ./production ./cmd/hd2font-mygo
        if ($LASTEXITCODE) { throw 'MyGo static checks failed' }
    }
    & $go build -trimpath -ldflags '-H=windowsgui' -o bin/HD2FontMaker.exe ./cmd/hd2font-mygo
    if ($LASTEXITCODE) { throw 'MyGo build failed' }
} finally {
    Pop-Location
}
