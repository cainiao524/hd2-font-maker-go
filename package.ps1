param([string]$Go, [switch]$SkipBuild)
$ErrorActionPreference = 'Stop'
$projectRoot = $PSScriptRoot
$versionText = [IO.File]::ReadAllText((Join-Path $projectRoot 'native_go/mygoapp/axes.go'))
$version = [regex]::Match($versionText, 'const Version = "([0-9]+\.[0-9]+\.[0-9]+)"').Groups[1].Value
if (!$version) { throw 'Application version was not found' }
if (!$SkipBuild) {
    if (!$Go) { $Go = Join-Path $projectRoot '.toolchains/go1.27.3/go/bin/go.exe' }
    if (!(Test-Path -LiteralPath $Go)) { $Go = (Get-Command go.exe -ErrorAction Stop).Source }
    & (Join-Path $projectRoot 'test.ps1') -Go $Go
}

$bin = Join-Path $projectRoot 'native_go/mygoapp/bin'
$exe = Get-Item -LiteralPath (Join-Path $bin 'HD2FontMaker.exe')
if ($exe.VersionInfo.FileVersion -ne "$version.0" -or $exe.VersionInfo.ProductVersion -ne "$version.0") {
    throw 'The executable version does not match the source; rebuild resources and the application'
}
$dist = Join-Path $projectRoot 'dist'
$stage = Join-Path $projectRoot ('.local-snapshot/package-' + [guid]::NewGuid().ToString('N'))
$windowsStage = Join-Path $stage 'Windows'
$sourceStage = Join-Path $stage 'Source'
New-Item -ItemType Directory -Force -Path $dist,$windowsStage,$sourceStage | Out-Null

function Copy-PackageFile([string]$Source, [string]$Destination) {
    New-Item -ItemType Directory -Force -Path ([IO.Path]::GetDirectoryName($Destination)) | Out-Null
    Copy-Item -LiteralPath $Source -Destination $Destination
}

foreach ($relative in @('HD2FontMaker.exe','hd2harfbuzz.dll','hd2skia.dll','skia.dll',
    'NATIVE_THIRD_PARTY_NOTICES.txt','HARFBUZZ-LICENSE.txt',
    'vendor/msdf-atlas-gen/msdf-atlas-gen.exe','vendor/LICENSE-msdf-atlas-gen.txt','vendor/LICENSE-msdfgen.txt')) {
    Copy-PackageFile (Join-Path $bin $relative) (Join-Path $windowsStage $relative)
}
foreach ($relative in @('LICENSE','RELEASE_NOTES.md','VALIDATION-2.1.0.zh-CN.txt')) {
    Copy-PackageFile (Join-Path $projectRoot $relative) (Join-Path $windowsStage $relative)
}
Copy-Item -LiteralPath (Join-Path $projectRoot 'native_go/LICENSES') -Destination (Join-Path $windowsStage 'licenses') -Recurse
$instructions = @"
HD2 Font Maker $version - Windows x64

Extract the entire archive to a writable folder outside the game directory.
Run HD2FontMaker.exe and keep the DLLs, vendor folder and license notices beside it.
No Python, Go or WebView installation is required to run this application.

Select the game folder, import your font, choose resource groups and output a ZIP.
Put same-family weight files together before importing to discover their faces.
Weight matching and outline processing are separate options.
Install the generated font ZIP with your mod manager. The tool does not install it.
Use only one font mod replacing the same game resources at a time.

Existing users may keep their UserData folder. This archive contains no UserData,
personal fonts, game resources or generated font mods.
See RELEASE_NOTES.md and VALIDATION-2.1.0.zh-CN.txt for changes and test limits.
"@
[IO.File]::WriteAllText((Join-Path $windowsStage 'README.txt'), $instructions, [Text.UTF8Encoding]::new($false))

# Export only repository source inputs, including the reviewed uncommitted files.
$sourceFiles = @(git -C $projectRoot -c core.quotepath=false ls-files --cached --others --exclude-standard)
if ($LASTEXITCODE -or !$sourceFiles.Count) { throw 'A Git checkout is required to collect the source package' }
foreach ($relative in ($sourceFiles | Sort-Object -Unique)) {
    if ($relative -match '(^|/)(\.git|\.toolchains|\.local-snapshot|UserData|dist|test-output)(/|$)') {
        throw "Private/build state cannot be packaged: $relative"
    }
    $path = [IO.Path]::GetFullPath((Join-Path $projectRoot $relative))
    if (!$path.StartsWith($projectRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Source path leaves the checkout: $relative"
    }
    $item = Get-Item -LiteralPath $path
    if ($item.LinkType) { throw "Source links need explicit packaging support: $relative" }
    Copy-PackageFile $path (Join-Path $sourceStage $relative)
}

Add-Type -AssemblyName System.IO.Compression.FileSystem
function Write-VerifiedZip([string]$Directory, [string]$Output) {
    $temporary = $Output + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    [IO.Compression.ZipFile]::CreateFromDirectory($Directory, $temporary, [IO.Compression.CompressionLevel]::Optimal, $false)
    $archive = [IO.Compression.ZipFile]::OpenRead($temporary)
    try {
        $expected = @(Get-ChildItem -LiteralPath $Directory -Recurse -File)
        $entries = @($archive.Entries | Where-Object { $_.Name })
        if ($entries.Count -ne $expected.Count) { throw 'ZIP member count does not match staging' }
        foreach ($entry in $entries) {
            $source = Join-Path $Directory $entry.FullName
            $stream = $entry.Open()
            $sha = [Security.Cryptography.SHA256]::Create()
            try { $digest = [BitConverter]::ToString($sha.ComputeHash($stream)).Replace('-','') }
            finally { $stream.Dispose(); $sha.Dispose() }
            if ($digest -ne (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash) {
                throw "ZIP readback differs: $($entry.FullName)"
            }
        }
    } finally { $archive.Dispose() }
    Move-Item -LiteralPath $temporary -Destination $Output -Force
}

$windowsZip = Join-Path $dist "HD2-Font-Maker-$version-Windows.zip"
$sourceZip = Join-Path $dist "HD2-Font-Maker-$version-Source.zip"
Write-VerifiedZip $windowsStage $windowsZip
Write-VerifiedZip $sourceStage $sourceZip
$checksums = foreach ($path in @($windowsZip,$sourceZip)) {
    (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() + '  ' + [IO.Path]::GetFileName($path)
}
[IO.File]::WriteAllText((Join-Path $dist "HD2-Font-Maker-$version-SHA256SUMS.txt"), ($checksums -join "`n") + "`n", [Text.UTF8Encoding]::new($false))
Get-Item -LiteralPath $windowsZip,$sourceZip | Select-Object Name,Length,FullName
