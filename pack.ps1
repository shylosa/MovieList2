# pack.ps1 — pack source code for audit

$output = "movielist-app.zip"
$maxSizeKB = 200

$include = @(
    "app.go", "main.go", "go.mod",
    "app_getaimodels_test.go", "app_updatemovie_test.go",
    "wails.json", ".cursorrules", ".editorconfig",
    ".gitattributes", "AGENTS.md", "CHECKLIST.md"
)

# Collect files
$files = @()
foreach ($f in $include) {
    if (Test-Path $f) {
        $files += (Get-Item $f).FullName
    }
    else {
        Write-Warning "Not found: $f"
    }
}

# Collect from internal, strict bypass for build folders and exe binaries
$files += (Get-ChildItem -Path "internal" -Recurse -File | Where-Object {
        $_.FullName -notmatch "internal\\build" -and $_.Extension -ne ".exe"
    }).FullName

# Pack
if (Test-Path $output) { Remove-Item $output }
Compress-Archive -Path $files -DestinationPath $output

# Check size
$sizeKB = [math]::Round((Get-Item $output).Length / 1KB, 1)
$status = if ($sizeKB -le $maxSizeKB) { "OK" } else { "TOO LARGE" }

Write-Host ""
Write-Host "  Archive : $output"
Write-Host "  Size    : $sizeKB KB / $maxSizeKB KB [$status]"
Write-Host "  Files   : $($files.Count)"
Write-Host ""
