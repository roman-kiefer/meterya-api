# Meterya API

Die Go-API für Meterya verwaltet Workspaces, Rollen, Messpunkte, Ablesungen, Tarife, Monatsabrechnungen, Fotos, Audit-Ereignisse und Sicherungen. PostgreSQL speichert unveränderliche Ereignisse und daraus aktualisierte Projektionen. Keycloak übernimmt Anmeldung und zweite Faktoren; die Oberfläche liegt im separaten Repository `meterya-app`.

Die technische Spezifikation im Schwester-Repository `meterya-docs` beschreibt die fachlichen Regeln und API-Verträge. Die API verwendet Go 1.26, PostgreSQL 18, pgx und goose. Der Code ist nach HTTP (`internal/httpapi`), Fachlogik (`internal/domain`), Persistenz (`internal/store`), Authentifizierung (`internal/auth`) und Bildverarbeitung (`internal/media`) gegliedert.

## Lokal starten

Voraussetzungen: Docker Desktop mit Linux-Containern, Go 1.26 und für die Oberfläche Flutter. Im Schwesterordner `meterya-app` zuerst `flutter pub get` und `flutter build web` ausführen. Dann im API-Repository:

```powershell
cd deploy
./prepare-local.ps1
docker compose config --quiet
docker compose up -d --build
```

`prepare-local.ps1` erzeugt einmalig `deploy/.env` und `deploy/keycloak/generated-realm.json`. Beide Dateien enthalten lokale Zugangsdaten, werden ignoriert und dürfen nicht veröffentlicht werden. Vorhandene Dateien überschreibt das Skript nicht. Die Compose-Umgebung bindet Gateway und Test-Mailserver nur an `localhost`; API und PostgreSQL haben keinen öffentlichen Port.

- Oberfläche und API-Gateway: http://localhost:8080
- Keycloak-Betreiberkonsole: http://localhost:8080/identity/admin/
- Lokaler Test-Mailserver: http://localhost:8025

Die lokale Keycloak-Instanz verwendet `start-dev`, Demo-Benutzer und Mailpit. Sie ist **keine** Konfiguration für einen öffentlich erreichbaren Server. Dort sind HTTPS, produktive Identitäten, SMTP, getrennte Geheimnisse, Backups und ein eigenes Gateway nötig. Die benötigten Keycloak-Funktionen für Faktorwahl, Passkeys, Wiederherstellungscodes und bestätigten E-Mailwechsel stehen in der Realm-Vorlage für neue lokale Installationen.

Nach dem ersten Start den Demo-Workspace einmalig anlegen:

```powershell
docker compose exec api meterya bootstrap --name Haushalt --subject 11111111-1111-4111-8111-111111111111 --email admin@example.test
```

Das Demo-Passwort steht unter `DEMO_ADMIN_PASSWORD` in `deploy/.env`. Beim ersten Login einen zweiten Faktor einrichten. Für produktive Workspaces statt der Demo-ID die geprüfte Keycloak-Subject-ID verwenden. `OPERATOR_SUBJECT` muss ebenfalls auf einen verifizierten Betreiber-Subject zeigen; Workspace-Adminrechte allein erlauben keine systemweite Wiederherstellung.

### Datenbank-Baseline

Das erste Release enthält eine Baseline-Migration für eine **leere** Datenbank. Die API führt sie beim Start aus. Datenbanken aus der Entwicklungsphase mit den früheren Migrationen werden mit einer Fehlermeldung abgewiesen und niemals automatisch gelöscht. Die Entwicklungsdaten sind laut Projektfestlegung verwerfbar. Nur wenn die lokale Compose-Umgebung tatsächlich entbehrlich ist, kann ihr Daten-Volume nach eigenem Backup bewusst entfernt und anschließend frisch gestartet werden:

```powershell
cd deploy
docker compose down --volumes
docker compose up -d --build
```

`down --volumes` entfernt **alle Daten** der lokalen Compose-Datenbank und Keycloak-Realm. Bei anderen Installationen eine neue leere Datenbank und Realm bereitstellen. Alte Entwicklungssicherungen sind mit der Release-Version nicht kompatibel.

## Prüfen und aktualisieren

```powershell
go test ./...
go vet ./...
```

Die Persistenztests benötigen `TEST_DATABASE_URL` mit einer eigens für Tests vorgesehenen PostgreSQL-Datenbank. Ohne diese Variable werden sie übersprungen. Sie benötigen `CREATE SCHEMA`, legen je Test ein isoliertes Schema an und entfernen es danach. Alle Tests mit PostgreSQL und der OCR-Laufzeit lassen sich lokal über Compose starten:

```powershell
cd deploy
docker compose --profile test run --build --rm test
```

`tool/update-local.ps1` führt diese Prüfungen aus und baut danach die lokale API neu. Alternativ nach Quellcodeänderungen `docker compose up -d --build api` ausführen. Ein API-Start berechnet fehlende Jahresprojektionen; `docker compose exec api meterya rebuild` erneuert fachliche Projektionen transaktional aus Ereignissen. Sitzungen, OIDC-Zustände und operative Einladungszustände sind kein vollständiger Event-Rebuild.

Der allgemeine Betreiberbefehl `meterya import-monthly` importiert vorbereitete Monatsdaten aus JSON über stdin. `--admin-email` muss genau einen vorhandenen Admin identifizieren; `--existing-point` verlangt einen vorhandenen Zielzähler und `--replace-tariffs` ersetzt dessen Tarifplan ausdrücklich. Der Import löscht keine vorhandenen Ablesungen.

## Sicherungen

Die Betreiber-API erstellt ein ZIP mit fachlichen Daten und bestätigten Originalfotos. Die Vorschau validiert den Import in einem getrennten Datenbankschema; die bestätigte Wiederherstellung ersetzt die Fachdaten atomar und löscht dabei laufende Anmeldesitzungen. Sie verlangt Betreiberberechtigung, zweiten Faktor, Dateiprüfsumme und `X-Backup-Confirm: RESTORE`. Keycloak und Serverkonfiguration separat sichern.

Grenzen: 32 MiB für den strukturierten Datenteil und 50 GiB für das ZIP. Aufträge und Downloads verfallen nach 24 Stunden. Die feste Kennung `meterya-release-schema-v1` kennzeichnet Sicherungen dieser Release-Datenstruktur. Entwicklungssicherungen werden abgewiesen. Weitere Details stehen in `meterya-docs/docs/technical/api/backup.md`.

## Docker-Image und Release

`Dockerfile` baut ein minimales Laufzeit-Image mit Tesseract und ohne Root-Benutzer. Der GitHub-Workflow führt bei Pull Requests und `main` Go-Tests einschließlich PostgreSQL-Integrationstests, `go vet` und einen Build für `linux/amd64` und `linux/arm64` aus. Nur ein Versionstag wie `v1.0.0` veröffentlicht nach erfolgreichen Prüfungen `ghcr.io/<owner>/meterya-api:v1.0.0`. Das GHCR-Paket des zunächst privaten Repositories muss privat bleiben. Für den Abruf eines privaten Images ist eine passende Paket-Leseberechtigung nötig.

Das Image enthält nur die API. Es benötigt mindestens `DATABASE_URL`, `PUBLIC_ORIGIN`, `OIDC_ISSUER`, `OIDC_INTERNAL_BASE`, `OIDC_CLIENT_SECRET` und `KEYCLOAK_SERVICE_SECRET`; für Betreiber-Backups zusätzlich `OPERATOR_SUBJECT`. Zugangsdaten nicht in das Image einbauen. Der lokale Compose-Stack baut die API weiterhin selbst und ist kein produktives Deployment.

## Lizenz

Copyright © 2026 Meterya contributors. Veröffentlicht unter **AGPL-3.0-only**; der vollständige Text steht in [LICENSE](LICENSE).
