param([string]$OutputDirectory = 'test-output/freeze')
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$nativeRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$env:GOPATH = Join-Path $projectRoot '.toolchains/gopath'
$env:GOCACHE = Join-Path $projectRoot '.toolchains/gocache'
$env:GOTMPDIR = $env:GOCACHE
$env:GOTOOLCHAIN = 'local'
$go = Join-Path $projectRoot '.toolchains/go1.27.2/go/bin/go.exe'
$output = if ([IO.Path]::IsPathRooted($OutputDirectory)) { $OutputDirectory } else { Join-Path $PSScriptRoot $OutputDirectory }
New-Item -ItemType Directory -Force -Path $output | Out-Null

function Write-JSON($Path, $Value) {
    $Value | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $Path -Encoding utf8NoBOM
}

function Source-Snapshot {
    $owned = @((Get-ChildItem -LiteralPath $PSScriptRoot -File | Where-Object { $_.Extension -in @('.go', '.ps1') -or $_.Name -in @('go.mod', 'go.sum') }).FullName)
    foreach ($folder in @('production', 'cmd/hd2font-mygo', 'locales', 'icons')) {
        $owned += (Get-ChildItem -LiteralPath (Join-Path $PSScriptRoot $folder) -File | Where-Object { $_.Extension -in @('.go', '.json', '.svg') }).FullName
    }
    $upstream = @((Get-ChildItem -LiteralPath (Join-Path $nativeRoot 'internal') -Filter '*.go' -File -Recurse | Where-Object { $_.Name -notlike '*_test.go' }).FullName)
    $upstream += (Join-Path $nativeRoot 'go.mod'), (Join-Path $nativeRoot 'go.sum')
    $rows = @()
    foreach ($group in @(@{ Kind='mygoapp'; Files=$owned }, @{ Kind='upstream-read-only'; Files=$upstream })) {
        foreach ($file in $group.Files | Sort-Object -Unique) {
            $rows += [ordered]@{ kind=$group.Kind; path=[IO.Path]::GetRelativePath($nativeRoot, $file).Replace('\','/'); sha256=(Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant() }
        }
    }
    return ,$rows
}

function Native-Input-Snapshot {
    $files = @()
    foreach ($dll in @('hd2harfbuzz.dll', 'hd2skia.dll', 'skia.dll')) {
        $files += @{ relative_path=$dll; path=(Join-Path $nativeRoot "bin/$dll") }
    }
    $files += @{ relative_path='vendor/msdf-atlas-gen/msdf-atlas-gen.exe'; path=(Join-Path $projectRoot 'vendor/msdf-atlas-gen/msdf-atlas-gen.exe') }
    return ,@($files | ForEach-Object { [ordered]@{ relative_path=$_.relative_path; path=$_.path; sha256=(Get-FileHash -LiteralPath $_.path -Algorithm SHA256).Hash.ToLowerInvariant() } })
}

function Invoke-Stage($Name, $Program, [string[]]$CommandArguments) {
    $stdout = Join-Path $output "$Name.stdout.txt"
    if ($Name -eq 'tests') { $stdout = Join-Path $output 'tests.raw.jsonl' }
    $stderr = Join-Path $output "$Name.stderr.txt"
    $started = [DateTime]::UtcNow
    $timer = [Diagnostics.Stopwatch]::StartNew()
    & $Program @CommandArguments 1> $stdout 2> $stderr
    $exitCode = $LASTEXITCODE
    $timer.Stop()
    return [ordered]@{ name=$Name; started_utc=$started.ToString('o'); wall_seconds=$timer.Elapsed.TotalSeconds; exit_code=$exitCode; arguments=$CommandArguments; stdout=$stdout; stderr=$stderr }
}

Push-Location $PSScriptRoot
try {
    $started = [DateTime]::UtcNow
    $before = Source-Snapshot
    Write-JSON (Join-Path $output 'sources.before.json') $before
    $nativeBefore = Native-Input-Snapshot
    Write-JSON (Join-Path $output 'native-inputs.before.json') $nativeBefore
    $toolchain = (& $go version | Out-String).Trim()
    $stages = @()
    $stages += Invoke-Stage 'tests' $go @('test', '-mod=readonly', '-count=1', '-json', '.', './production', './cmd/hd2font-mygo')
    $stages += Invoke-Stage 'vet' $go @('vet', '-mod=readonly', '.', './production', './cmd/hd2font-mygo')
    $stages += Invoke-Stage 'build' (Join-Path $PSHOME 'pwsh.exe') @('-NoProfile', '-File', (Join-Path $PSScriptRoot 'build.ps1'))
    $after = Source-Snapshot
    Write-JSON (Join-Path $output 'sources.after.json') $after
    $nativeAfter = Native-Input-Snapshot
    Write-JSON (Join-Path $output 'native-inputs.after.json') $nativeAfter
    $sourcesUnchanged = (($before | ConvertTo-Json -Compress -Depth 5) -ceq ($after | ConvertTo-Json -Compress -Depth 5))
    $nativeUnchanged = (($nativeBefore | ConvertTo-Json -Compress -Depth 5) -ceq ($nativeAfter | ConvertTo-Json -Compress -Depth 5))
    $events = @(Get-Content -LiteralPath (Join-Path $output 'tests.raw.jsonl') | Where-Object { $_.Trim() -ne '' } | ForEach-Object { $_ | ConvertFrom-Json })
    $testResults = @($events | Where-Object { $_.Test -and $_.Action -in @('pass', 'fail', 'skip') } | ForEach-Object { [ordered]@{ package=$_.Package; test=$_.Test; action=$_.Action; elapsed_seconds=$_.Elapsed } })
    $packageResults = @($events | Where-Object { -not $_.Test -and $_.Action -in @('pass', 'fail', 'skip') } | ForEach-Object { [ordered]@{ package=$_.Package; action=$_.Action; elapsed_seconds=$_.Elapsed } })
    $runtimeFiles = @()
    foreach ($relative in @('HD2FontMaker.exe', 'hd2harfbuzz.dll', 'hd2skia.dll', 'skia.dll', 'vendor/msdf-atlas-gen/msdf-atlas-gen.exe', 'HARFBUZZ-LICENSE.txt', 'NATIVE_THIRD_PARTY_NOTICES.txt', 'vendor/LICENSE-msdf-atlas-gen.txt', 'vendor/LICENSE-msdfgen.txt')) {
        $path = Join-Path (Join-Path $PSScriptRoot 'bin') $relative
        if (Test-Path -LiteralPath $path -PathType Leaf) {
            $file = Get-Item -LiteralPath $path
            $runtimeFiles += [ordered]@{ relative_path=$relative; path=$file.FullName; bytes=$file.Length; sha256=(Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() }
        }
    }
    $localeResults = @(Get-ChildItem -LiteralPath (Join-Path $PSScriptRoot 'locales') -Filter '*.json' -File | ForEach-Object {
        $source = Join-Path (Join-Path $projectRoot 'locales') $_.Name
        [ordered]@{ locale=$_.BaseName; identical_to_project=((Get-FileHash -LiteralPath $_.FullName).Hash -eq (Get-FileHash -LiteralPath $source).Hash) }
    })
    $passed = @($stages | Where-Object { $_.exit_code -ne 0 }).Count -eq 0
    $nativeCopiesMatch = $true
    foreach ($inputFile in $nativeBefore) {
        $copies = @($runtimeFiles | Where-Object { $_.relative_path -eq $inputFile.relative_path })
        if ($copies.Count -ne 1 -or $copies[0].sha256 -ne $inputFile.sha256) { $nativeCopiesMatch = $false }
    }
    $passed = $passed -and $sourcesUnchanged -and $runtimeFiles.Count -eq 9 -and $localeResults.Count -eq 14
    $passed = $passed -and $nativeUnchanged -and $nativeCopiesMatch
    $passed = $passed -and @($localeResults | Where-Object { -not $_.identical_to_project }).Count -eq 0
    $metrics = [ordered]@{
        schema_version=1; version='1.4.0'; started_utc=$started.ToString('o'); finished_utc=[DateTime]::UtcNow.ToString('o')
        verification_passed=$passed; release_ready=$false; release_quality_owner='parent'
        toolchain=$toolchain; go_toolchain_mode=$env:GOTOOLCHAIN; desktop_automation_performed=$false; executable_launched=$false
        actual_game_scanned=$false; parent_real_game_results='reported by parent; not reverified in this UI adapter freeze'
        sources_unchanged_during_verification=$sourcesUnchanged; source_files=$after.Count
        native_inputs_unchanged_during_verification=$nativeUnchanged; runtime_copies_match_tested_inputs=$nativeCopiesMatch
        stages=$stages; packages=$packageResults; tests=$testResults
        top_level_tests_passed=@($testResults | Where-Object { $_.action -eq 'pass' -and $_.test -notmatch '/' }).Count
        subtests_passed=@($testResults | Where-Object { $_.action -eq 'pass' -and $_.test -match '/' }).Count
        skipped_tests=@($testResults | Where-Object { $_.action -eq 'skip' }).Count
        locales=$localeResults; runtime_files=$runtimeFiles
        packaging_note='Runtime whitelist metadata only; parent supplies final license whitelist. Never package UserData, test-output, source-font copies, fixtures or game assets.'
    }
    Write-JSON (Join-Path $output 'metrics.json') $metrics
    [pscustomobject]@{ passed=$passed; source_stable=$sourcesUnchanged; tests=$metrics.top_level_tests_passed; subtests=$metrics.subtests_passed; metrics=(Join-Path $output 'metrics.json') }
    if (-not $passed) { throw 'Freeze verification failed; inspect metrics.json and raw stage outputs.' }
} finally {
    Pop-Location
}
