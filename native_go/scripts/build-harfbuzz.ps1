param([int]$Jobs = 4)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$revision = 'c7a7457b7385f33178e8cf87615ca077a810bbe7'
$sha256 = '10dcea60bd85ff219490d55525630650ac8c57044c4efdc878162f286451775b'
$source = Join-Path $root "third_party\harfbuzz-$revision"
$downloads = Join-Path $root 'third_party\harfbuzz-download'
$archive = Join-Path $downloads 'harfbuzz-14.6.0.tar.gz'
$build = Join-Path $root 'third_party\harfbuzz-build'
$out = Join-Path $root 'bin'
$licenses = Join-Path $root 'native\harfbuzz\licenses'
New-Item -ItemType Directory -Force -Path $downloads,$build,$out,$licenses | Out-Null
if (!(Test-Path -LiteralPath $archive)) {
    & curl.exe --fail --location --retry 3 --output $archive "https://codeload.github.com/harfbuzz/harfbuzz/tar.gz/$revision"
    if ($LASTEXITCODE) { throw 'HarfBuzz source download failed.' }
}
if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $sha256) {
    throw 'HarfBuzz source archive SHA256 mismatch.'
}
if (!(Test-Path -LiteralPath (Join-Path $source 'CMakeLists.txt'))) {
    & tar.exe -xf $archive -C (Join-Path $root 'third_party')
    if ($LASTEXITCODE) { throw 'HarfBuzz source extraction failed.' }
}
$vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
$vs = & $vswhere -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
if (!$vs) { throw 'MSVC x64 build tools are required.' }
$vcvars = Join-Path $vs 'VC\Auxiliary\Build\vcvars64.bat'
& cmd.exe /d /c "call `"$vcvars`" >nul && set" | ForEach-Object {
    if ($_ -match '^([^=]+)=(.*)$') { [Environment]::SetEnvironmentVariable($matches[1], $matches[2], 'Process') }
}
if ($LASTEXITCODE) { throw 'MSVC environment setup failed.' }
$cmake = Join-Path $vs 'Common7\IDE\CommonExtensions\Microsoft\CMake\CMake\bin\cmake.exe'
$ninja = Join-Path $vs 'Common7\IDE\CommonExtensions\Microsoft\CMake\Ninja\ninja.exe'
$env:VSLANG = '1033'
# Capture the installed compiler's actual localized include prefix. CMake's
# probe can misdecode this prefix on Windows with a UTF-8 console code page.
$includeOutput = & cl.exe /nologo /utf-8 /showIncludes /EP /TP (Join-Path $root 'native\harfbuzz\bridge.h') 2>&1
if ($LASTEXITCODE) { throw 'MSVC include-prefix probe failed.' }
$includePrefix = $includeOutput | ForEach-Object {
    if ($_ -match '^(.*?:\s+.*?:\s+)(?:[A-Za-z]:[\\/])') { $matches[1] }
} | Select-Object -First 1
if (!$includePrefix) { throw 'Cannot detect the MSVC include prefix.' }
& $cmake -S (Join-Path $root 'native\harfbuzz') -B $build -G Ninja "-DCMAKE_MAKE_PROGRAM=$ninja" "-DHARFBUZZ_SOURCE=$source" "-DHD_HB_INCLUDE_PREFIX=$includePrefix" -DCMAKE_BUILD_TYPE=Release
if ($LASTEXITCODE) { throw 'HarfBuzz CMake configuration failed.' }
& $cmake --build $build --target hd2harfbuzz --parallel $Jobs
if ($LASTEXITCODE) { throw 'HarfBuzz native build failed.' }
Copy-Item -LiteralPath (Join-Path $source 'COPYING') -Destination (Join-Path $licenses 'HarfBuzz-COPYING.txt') -Force
Copy-Item -LiteralPath (Join-Path $source 'src\ms-use\COPYING') -Destination (Join-Path $licenses 'HarfBuzz-MS-USE-COPYING.txt') -Force
Copy-Item -LiteralPath (Join-Path $build 'hd2harfbuzz.dll') -Destination (Join-Path $out 'hd2harfbuzz.dll') -Force
& dumpbin.exe /nologo /dependents (Join-Path $out 'hd2harfbuzz.dll')
if ($LASTEXITCODE) { throw 'Native dependency inspection failed.' }
