$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$sourcePath = Join-Path $repo 'recognition_coordinator.go'
$source = [IO.File]::ReadAllText($sourcePath)
$guard = 'if fileSimilarity < geminiTMDBVerifyMinJW {'
if ([regex]::Matches($source, [regex]::Escape($guard)).Count -ne 1) {
    throw 'Expected exactly one R10-A filename guard'
}
$variants = [ordered]@{
    A = $guard
    B = 'if false && fileSimilarity < geminiTMDBVerifyMinJW {'
    C = @'
if tmdbInfo.AmbiguousExact || (fileSimilarity != 1 &&
            !r10bCrossScriptExact(parsed.CleanTitle, tmdbInfo)) {
'@
}

$crossScriptExact = @'

func r10bCrossScriptExact(fileTitle string, info *tmdb.MovieInfo) bool {
    for _, catalogueTitle := range []string{info.TitleEN, info.TitleUA, info.MatchedAlias} {
        if utils.HasCyrillic(fileTitle) != utils.HasCyrillic(catalogueTitle) &&
            tmdb.TitleSimilarity(utils.CyrillicToLatin(fileTitle), utils.CyrillicToLatin(catalogueTitle)) == 1 {
            return true
        }
    }
    return false
}
'@

$tmp = Join-Path ([IO.Path]::GetTempPath()) ('movielist-r10b-' + [guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($tmp) | Out-Null
$encoding = [Text.UTF8Encoding]::new($false)
$previousVariant = $env:MOVIELIST_R10B_VARIANT
$baselineCosts = @{}
Push-Location $repo
try {
    foreach ($variant in $variants.Keys) {
        $argsForGo = @('test')
        if ($variant -ne 'A') {
            $replacement = Join-Path $tmp "recognition-$variant.go"
            $overlay = Join-Path $tmp "overlay-$variant.json"
            $modified = $source.Replace($guard, $variants[$variant])
            if ($variant -eq 'C') {
                $modified += $crossScriptExact
            }
            [IO.File]::WriteAllText($replacement, $modified, $encoding)
            $mapping = @{ Replace = @{ $sourcePath = $replacement } } | ConvertTo-Json -Compress
            [IO.File]::WriteAllText($overlay, $mapping, $encoding)
            $argsForGo += @('-overlay', $overlay)
        }
        $env:MOVIELIST_R10B_VARIANT = $variant
        $argsForGo += @('.', '-run', '^TestR10BFilenameVerification$', '-count=1', '-v')
        $output = & go @argsForGo
        if ($LASTEXITCODE -ne 0) {
            $output
            throw "R10-B variant $variant failed"
        }
        $output | Select-String 'R10B .*oracle=|R10B .*group ' | ForEach-Object { $_.Line }
        $seen = 0
        foreach ($line in $output) {
            if ($line -match 'R10B [ABC] (\S+): oracle=.* \| \d+/\d+/\d+/\d+/\d+ \| (.+)$') {
                $seen++
                if ($variant -eq 'A') {
                    $baselineCosts[$Matches[1]] = $Matches[2]
                } elseif ($baselineCosts[$Matches[1]] -ne $Matches[2]) {
                    throw "Transport counts differ from A for $variant / $($Matches[1])"
                }
            }
        }
        if ($seen -eq 0 -or $seen -ne $baselineCosts.Count) {
            throw "Incomplete transport counts for $variant"
        }
    }
} finally {
    $env:MOVIELIST_R10B_VARIANT = $previousVariant
    Pop-Location
    foreach ($variant in 'B', 'C') {
        [IO.File]::Delete((Join-Path $tmp "recognition-$variant.go"))
        [IO.File]::Delete((Join-Path $tmp "overlay-$variant.json"))
    }
    [IO.Directory]::Delete($tmp)
}
