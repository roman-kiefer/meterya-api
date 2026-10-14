param()
$ErrorActionPreference = 'Stop'
$deployRoot = $PSScriptRoot
$envPath = Join-Path $deployRoot '.env'
$realmPath = Join-Path $deployRoot 'keycloak/generated-realm.json'
if ((Test-Path -LiteralPath $envPath) -or (Test-Path -LiteralPath $realmPath)) {
  throw 'Lokale Konfiguration existiert bereits. Bestehende Zugangsdaten werden nicht ersetzt.'
}
function New-LocalSecret {
  $bytes = [byte[]]::new(32)
  [System.Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
  return [Convert]::ToHexString($bytes).ToLowerInvariant()
}
$settings = [ordered]@{}
foreach ($key in @('POSTGRES_PASSWORD','APP_DB_PASSWORD','KC_DB_PASSWORD','KC_ADMIN_PASSWORD','OIDC_CLIENT_SECRET','KEYCLOAK_SERVICE_SECRET','DEMO_ADMIN_PASSWORD')) {
  $settings[$key] = New-LocalSecret
}
$realm = Get-Content -LiteralPath (Join-Path $deployRoot 'keycloak/realm.template.json') -Raw
foreach ($key in $settings.Keys) {
  $realm = $realm.Replace("__${key}__", $settings[$key])
}
$null = $realm | ConvertFrom-Json
[IO.File]::WriteAllText($realmPath, $realm, [Text.UTF8Encoding]::new($false))
$lines = foreach ($key in $settings.Keys) { "${key}=$($settings[$key])" }
[IO.File]::WriteAllText($envPath, ($lines -join "`n") + "`n", [Text.UTF8Encoding]::new($false))
Write-Output 'Lokale Konfiguration angelegt. Passwörter stehen in deploy/.env; nicht veröffentlichen.'
