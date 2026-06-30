# pack.ps1 — pack source code for audit

$output = "movielist-app.zip"
$maxSizeKB = 200

$include = @(
    "app.go", "main.go", "go.mod",
    "app_getaimodels_test.go", "app_updatemovie_test.go",
    "wails.json", ".cursorrules", ".editorconfig",
    ".gitattributes", "AGENTS.md", "CHECKLIST.md"
)

# Working staging folder — preserves relative paths inside the zip
$stageDir = Join-Path $env:TEMP "movielist-pack-stage"
if (Test-Path $stageDir) { Remove-Item $stageDir -Recurse -Force }
New-Item -ItemType Directory -Path $stageDir | Out-Null

function Copy-ToStage {
    param([string]$RelativePath)
    $dest = Join-Path $stageDir $RelativePath
    $destDir = Split-Path $dest -Parent
    if (-not (Test-Path $destDir)) {
        New-Item -ItemType Directory -Path $destDir -Force | Out-Null
    }
    Copy-Item -Path $RelativePath -Destination $dest -Force
}

# Top-level files
$copiedCount = 0
foreach ($f in $include) {
    if (Test-Path $f) {
        Copy-ToStage -RelativePath $f
        $copiedCount++
    }
    else {
        Write-Warning "Not found: $f"
    }
}

# internal/ — preserve full relative structure, skip build folders and binaries
$internalFiles = Get-ChildItem -Path "internal" -Recurse -File | Where-Object {
    $_.FullName -notmatch "internal\\build" -and $_.Extension -ne ".exe"
}
foreach ($file in $internalFiles) {
    # Relative path from current directory (e.g. internal\ai\gemini.go)
    $relPath = Resolve-Path -Relative $file.FullName
    $relPath = $relPath -replace "^\.\\", ""
    Copy-ToStage -RelativePath $relPath
    $copiedCount++
}

# Pack — Compress-Archive on the staging folder preserves directory structure
if (Test-Path $output) { Remove-Item $output }
Compress-Archive -Path (Join-Path $stageDir "*") -DestinationPath $output

# Cleanup staging
Remove-Item $stageDir -Recurse -Force

# Check size
$sizeKB = [math]::Round((Get-Item $output).Length / 1KB, 1)
$status = if ($sizeKB -le $maxSizeKB) { "OK" } else { "TOO LARGE" }

Write-Host ""
Write-Host "  Archive : $output"
Write-Host "  Size    : $sizeKB KB / $maxSizeKB KB [$status]"
Write-Host "  Files   : $copiedCount"
Write-Host ""
