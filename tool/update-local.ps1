$ErrorActionPreference = 'Stop'
Push-Location (Join-Path (Split-Path -Parent $PSScriptRoot) 'deploy')
try {
    $testOutput = @(docker compose --profile test run --build --rm test 2>&1)
    $testExitCode = $LASTEXITCODE
    $testOutput | ForEach-Object { Write-Host "$_" }
    if ($testExitCode -ne 0) {
        Write-Host 'Fehlgeschlagene Tests:'
        $testOutput | Select-String -Pattern '^--- FAIL:', '^FAIL\s' | ForEach-Object { Write-Host $_.Line }
        throw 'Datenbanktests fehlgeschlagen. Die laufende API wurde nicht ersetzt. Bitte die Zeilen unter Fehlgeschlagene Tests senden.'
    }
    docker compose up -d --build api
    if ($LASTEXITCODE -ne 0) { throw 'API konnte nicht aktualisiert werden.' }
    Write-Output 'API aktualisiert. Meterya im Browser neu laden.'
} finally {
    Pop-Location
}
