# Meterya API

Meterya API is the Go backend for household energy data. It stores immutable events and current projections in PostgreSQL and uses Keycloak for sign-in. The Flutter web app is maintained in the separate Meterya App repository.

## Features

- Workspaces, members, roles, and metering points
- Meter readings, tariffs, monthly calculations, and reading photos
- Audit events and operator backup and restore
- Keycloak authentication with second-factor support

## Use with Docker Compose

The API and Keycloak images are publicly available from GHCR:

```text
ghcr.io/roman-kiefer/meterya-api:latest
ghcr.io/roman-kiefer/meterya-keycloak:latest
```

Run them with PostgreSQL 18 and the Meterya web app in a Compose network. An HTTPS reverse proxy routes requests to the services. If the public proxy runs on the host, bind the Compose gateway port only to loopback, for example `127.0.0.1:8080:8080`. Keep the API, Keycloak, and PostgreSQL ports inside the Compose network.

Once your deployment's Compose file and configuration are in place, start the services from that directory:

```sh
docker compose pull
docker compose up -d
docker compose ps
```

Configure the API container with:

| Variable                  | Value                                                                |
| ------------------------- | -------------------------------------------------------------------- |
| `DATABASE_URL`            | PostgreSQL connection URL for the Meterya database                   |
| `PUBLIC_ORIGIN`           | Public HTTPS origin, for example `https://meterya.example.com`       |
| `OIDC_ISSUER`             | `${PUBLIC_ORIGIN}/identity/realms/meterya`                           |
| `OIDC_INTERNAL_BASE`      | Internal Keycloak URL, for example `http://keycloak:8080/identity`   |
| `OIDC_CLIENT_SECRET`      | Secret of the `meterya-web` Keycloak client                          |
| `KEYCLOAK_SERVICE_SECRET` | Secret of the `meterya-invitations` Keycloak client                  |
| `OPERATOR_SUBJECT`        | Verified Keycloak subject authorized for operator backup and restore |

Configure the Keycloak container for PostgreSQL and start it with `start --import-realm`. Set `KC_HOSTNAME` to the public `https://.../identity` URL, `KC_HTTP_RELATIVE_PATH=/identity`, `KC_HTTP_ENABLED=true`, and `KC_PROXY_HEADERS=xforwarded` for TLS termination at the proxy. The [realm template](deploy/keycloak/realm.template.json) contains placeholders for the public origin, client secrets, and SMTP settings; render it with real values **outside the image** and mount it at `/opt/keycloak/data/import/meterya-realm.json` for the first start. It contains no demo user. Keycloak imports a realm only when it does not already exist, so later template edits do not update a running realm.

Use an empty PostgreSQL database for the first release. Older development schemas and backups are unsupported. After creating a real Keycloak user and configuring a second factor, create the first workspace with the API image:

```sh
docker compose exec api meterya bootstrap --name Household --subject '<keycloak-user-id>' --email 'admin@example.com'
```

The exact Compose services, domain, SMTP server, secrets, database volumes, and backup schedule belong to the deployment configuration. Keep secrets and the rendered realm out of Git. The API responds to `GET /health/live` and `GET /health/ready` for health checks.

## Development checks

```sh
go test ./...
go vet ./...
```

PostgreSQL integration tests require `TEST_DATABASE_URL` pointing to a dedicated test database; they are skipped when it is unset. The release workflow runs them with PostgreSQL.

## Community

See the [contribution guide](CONTRIBUTING.md), [security policy](SECURITY.md), and
[accessibility information](ACCESSIBILITY.md). Please follow the
[Code of Conduct](CODE_OF_CONDUCT.md).

Licensed under [AGPL-3.0-only](LICENSE). Copyright © 2026 Meterya contributors.
