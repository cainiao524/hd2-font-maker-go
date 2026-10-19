param([string]$SkiaDll, [string]$SkiaRoot, [string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
if (!$SkiaDll) { $SkiaDll = Join-Path (Split-Path $root -Parent) '.venv\Lib\site-packages\pathops\skia.dll' }
if (!$SkiaRoot) { $SkiaRoot = Join-Path $root 'third_party\skia_pathops-0.9.2\src\cpp\skia-builder\skia' }
$vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
$vs = & $vswhere -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
if (!$vs) { throw 'MSVC x64 build tools are required.' }
$vcvars = Join-Path $vs 'VC\Auxiliary\Build\vcvars64.bat'
& cmd.exe /d /c "call `"$vcvars`" >nul && set" | ForEach-Object {
    if ($_ -match '^([^=]+)=(.*)$') { [Environment]::SetEnvironmentVariable($matches[1], $matches[2], 'Process') }
}
if ($LASTEXITCODE) { throw 'MSVC environment setup failed.' }
$out = if ($OutputDirectory) { [IO.Path]::GetFullPath($OutputDirectory) } else { Join-Path $root 'bin' }
$build = Join-Path $root 'build\skia'
New-Item -ItemType Directory -Force -Path $out,$build | Out-Null
$exports = & dumpbin.exe /nologo /exports $SkiaDll
if ($LASTEXITCODE) { throw 'Cannot inspect Skia DLL.' }
$names = $exports | ForEach-Object { if ($_ -match '^\s+\d+\s+[0-9A-F]+\s+[0-9A-F]+\s+(\S+)') { $matches[1] } }
@('LIBRARY skia.dll','EXPORTS') + $names | Set-Content -Encoding ascii -LiteralPath (Join-Path $build 'skia.def')
& lib.exe /nologo "/def:$build\skia.def" /machine:x64 "/out:$build\skia.lib"
if ($LASTEXITCODE) { throw 'Skia import library failed.' }
& cl.exe /nologo /std:c++17 /O2 /fp:precise /EHsc /MT /LD /DSKIA_DLL /DNDEBUG "/I$SkiaRoot" (Join-Path $root 'native\skia_bridge\bridge.cpp') "/Fo$build\bridge.obj" /link "$build\skia.lib" "/OUT:$out\hd2skia.dll" "/IMPLIB:$build\hd2skia.lib"
if ($LASTEXITCODE) { throw 'Native Skia bridge build failed.' }
Copy-Item -LiteralPath $SkiaDll -Destination (Join-Path $out 'skia.dll') -Force
& dumpbin.exe /nologo /dependents (Join-Path $out 'hd2skia.dll')
if ($LASTEXITCODE) { throw 'Native dependency inspection failed.' }
