param(
    [switch]$Dev,
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$WailsArguments
)

$ErrorActionPreference = 'Stop'
Push-Location -LiteralPath $PSScriptRoot
try {
    & node scripts/sync-version.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Version synchronization failed' }
    $taskWailsCommand = if ($Dev) { 'dev' } else { 'build' }
    & wails $taskWailsCommand @WailsArguments
    if ($LASTEXITCODE -ne 0) { throw 'Wails command failed' }
} finally {
    Pop-Location
}
