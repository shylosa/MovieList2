param([ValidateRange(1, 20)][int]$Count = 1, [string]$Cases = '')

$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$tmp = Join-Path ([IO.Path]::GetTempPath()) ('movielist-r12a-' + [guid]::NewGuid().ToString('N'))
$encoding = [Text.UTF8Encoding]::new($false)
$previousVariant = $env:MOVIELIST_R12A_VARIANT
$previousEncoding = [Console]::OutputEncoding
$paths = @('internal/tmdb/client.go', 'internal/tmdb/search.go')
$results = @{}

Push-Location $repo
try {
    [Console]::OutputEncoding = $encoding
    $mapping = @{}
    foreach ($path in $paths) {
        $source = Join-Path $repo $path
        $replacement = Join-Path $tmp $path
        [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($replacement)) | Out-Null
        [IO.File]::Copy($source, $replacement)
        $mapping[$source] = $replacement
    }
    & git -c core.autocrlf=false -C $tmp apply --unidiff-zero --whitespace=nowarn -- (Join-Path $PSScriptRoot 'prototype.diff')
    if ($LASTEXITCODE -ne 0) { throw 'Could not apply the R12-A prototype to the temporary sources.' }
    $overlay = Join-Path $tmp 'overlay.json'
    [IO.File]::WriteAllText($overlay, (@{ Replace = $mapping } | ConvertTo-Json -Compress), $encoding)

    $pattern = '^TestR12ATitleSearchAB$'
    if ($Cases) { $pattern += "/$Cases" }
    foreach ($variant in 'A', 'B') {
        $env:MOVIELIST_R12A_VARIANT = $variant
        $arguments = @('test', '.', '-run', $pattern, "-count=$Count", '-v')
        if ($variant -eq 'B') { $arguments += @('-overlay', $overlay) }
        $output = & go @arguments
        if ($LASTEXITCODE -ne 0) {
            $output
            throw "R12-A variant $variant failed."
        }
        $observations = @(
            foreach ($line in $output) {
                if ($line -match 'R12A (\{.*\})$') {
                    $Matches[1]
                }
            }
        )
        $results[$variant] = @($observations | ForEach-Object { $_ | ConvertFrom-Json })
        foreach ($observation in $observations) { "R12A $variant $observation" }
    }
    if (!$results.A.Count -or $results.A.Count -ne $results.B.Count) {
        throw 'Missing or incomplete A/B observations.'
    }
    for ($i = 0; $i -lt $results.A.Count; $i++) {
        $a = $results.A[$i]
        $b = $results.B[$i]
        if ($a.Name -cne $b.Name -or $a.Truth -cne $b.Truth) {
            throw "Different fixture or oracle at observation $i."
        }
        if ($a.Group -eq 'without_yo' -or $a.Name -in @('primary_success', 'imdb_first', 'confirmed', 'canceled', 'stop_between_attempts')) {
            if (($a | ConvertTo-Json -Depth 8 -Compress) -cne ($b | ConvertTo-Json -Depth 8 -Compress)) {
                throw "A/B parity failed for $($a.Name)."
            }
        }
        if ($a.Runs -ne $b.Runs) { throw "Scan count changed for $($a.Name)." }
        if ($a.Runs -eq 0) { continue }
        $normalPerRun = $a.Wire.Count / $a.Runs
        $experimentalPerRun = $b.Wire.Count / $b.Runs
        for ($run = 0; $run -lt $a.Runs; $run++) {
            for ($request = 0; $request -lt $normalPerRun; $request++) {
                if ($a.Wire[$run * $normalPerRun + $request] -cne $b.Wire[$run * $experimentalPerRun + $request]) {
                    throw "Baseline HTTP sequence changed for $($a.Name)."
                }
            }
        }
    }
} finally {
    $env:MOVIELIST_R12A_VARIANT = $previousVariant
    [Console]::OutputEncoding = $previousEncoding
    Pop-Location
    foreach ($path in $paths) { [IO.File]::Delete((Join-Path $tmp $path)) }
    [IO.File]::Delete((Join-Path $tmp 'overlay.json'))
    foreach ($path in 'internal/tmdb', 'internal', '.') {
        $directory = Join-Path $tmp $path
        if ([IO.Directory]::Exists($directory)) { [IO.Directory]::Delete($directory) }
    }
}
