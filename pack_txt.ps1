# pack_txt.ps1 — Рекурсивный обход структуры проекта с верификацией

$outputFile = "source_code.txt"
if (Test-Path $outputFile) { Remove-Item $outputFile }

# Эталонный список файлов для проверки полноты обхода папок
$expectedFiles = @(
    ".cursorrules", ".editorconfig", ".gitattributes", "app.go",
    "app_getaimodels_test.go", "app_updatemovie_test.go", "client.go",
    "config.go", "details.go", "gemini.go", "gemini_test.go",
    "generator.go", "grok.go", "grok_test.go", "logger.go",
    "main.go", "models.go", "parser.go", "parser_test.go",
    "scanner.go", "search.go", "search_test.go", "sheets.go",
    "storage.go", "storage_test.go", "system.go", "translit_test.go",
    "wails.json", "AGENTS.md", "CHECKLIST.md", "go.mod", "lang.go"
)

# Словарь для трекинга найденных файлов
$foundTracker = @{}
foreach ($name in $expectedFiles) { $foundTracker[$name] = $false }

function Append-FileToReport ($filePath, $shortName, $displayPath) {
    if (Test-Path $filePath) {
        $content = Get-Content -Raw -Path $filePath -ErrorAction SilentlyContinue
        if ($content) {
            Add-Content -Path $outputFile -Value "========================================"
            Add-Content -Path $outputFile -Value "FILE: $shortName ($displayPath)"
            Add-Content -Path $outputFile -Value "========================================"
            Add-Content -Path $outputFile -Value $content
            Add-Content -Path $outputFile -Value "`n`n"
            return $true
        }
    }
    return $false
}

Write-Host "Scanning directories..." -ForegroundColor Cyan
$packedCount = 0

# 1. Сканируем корень проекта
$rootFiles = Get-ChildItem -Path "." -File
foreach ($file in $rootFiles) {
    if ($foundTracker.ContainsKey($file.Name)) {
        if (Append-FileToReport -filePath $file.FullName -shortName $file.Name -displayPath "./$($file.Name)") {
            $foundTracker[$file.Name] = $true
            $packedCount++
        }
    }
}

# 2. Обходим папку internal (игнорируя build и exe)
if (Test-Path "internal") {
    $internalItems = Get-ChildItem -Path "internal" -Recurse -File | Where-Object {
        $_.FullName -notmatch "\\build\\" -and $_.Extension -ne ".exe"
    }

    foreach ($file in $internalItems) {
        if ($foundTracker.ContainsKey($file.Name)) {
            $relativePath = $file.FullName.Replace((Get-Location).FullName + "\", "").Replace("\", "/")
            if (Append-FileToReport -filePath $file.FullName -shortName $file.Name -displayPath $relativePath) {
                $foundTracker[$file.Name] = $true
                $packedCount++
            }
        }
    }
}

# 3. Верификация результатов обхода
Write-Host ""
Write-Host "Verification Results:" -ForegroundColor Cyan
$missingCount = 0
foreach ($name in $expectedFiles) {
    if ($foundTracker[$name]) {
        Write-Host "  OK: $name" -ForegroundColor Green
    }
    else {
        Write-Warning "  MISSING: $name"
        $missingCount++
    }
}

Write-Host ""
Write-Host "Total packed: $packedCount" -ForegroundColor Green
Write-Host "Total missing: $missingCount" -ForegroundColor Yellow
Write-Host "Done! Please upload source_code.txt here." -ForegroundColor Yellow
Write-Host ""
