# Contributing to Meterya API

Contributions are welcome. This repository covers the Go backend, PostgreSQL persistence, authentication integration, and the bundled Keycloak provider and theme. For substantial changes, open a feature request before starting implementation so the approach can be discussed.

## Where to report issues

- [Meterya](https://github.com/roman-kiefer/meterya/issues/new/choose): installation, Compose, and cross-component issues.
- [Meterya API](https://github.com/roman-kiefer/meterya-api/issues/new/choose): backend, persistence, and the bundled Keycloak provider and theme.
- [Meterya App](https://github.com/roman-kiefer/meterya-app/issues/new/choose): web interface and browser behavior.

Search existing issues before opening a new one. If the responsible component is unclear, start in Meterya and link related component issues instead of copying the same report into several repositories.

Use the bug report or feature request form. Include the API and Keycloak image tags and, where possible, digests, or the source commit and Go version. The latest tag changes over time. Include the PostgreSQL version, deployment method, and relevant proxy and Keycloak configuration, with all secrets removed.

Suspected vulnerabilities belong in the confidential process described in [SECURITY.md](SECURITY.md), not in public issues or pull requests.

## Protect private data

Use synthetic examples. Remove passwords, tokens, cookies, authorization headers, SMTP credentials, personal email addresses, household data, and identifying details from logs, screenshots, photos, and reproduction files. Do not attach configured `.env` files, rendered realm files, database dumps, or backups. Do not paste expanded `docker compose config` output.

## Development and validation

Fork the repository, create a focused branch from `main`, and read the README and any applicable `AGENTS.md` instructions. Keep unrelated formatting or refactoring out of the change.

Use the Go version declared in `go.mod`. Keep HTTP handling, application logic, domain logic, and persistence separate. Store events and their projections atomically. Use exact decimal quantities and integer cents for money.

Run:

```sh
go test ./...
go vet ./...
```

PostgreSQL integration tests are skipped without `TEST_DATABASE_URL`. Set it to a dedicated, disposable PostgreSQL test database and rerun `go test -count=1 ./...`; never point tests at a production or personal database. Read the relevant tests before running them.

For authentication, Keycloak provider, or theme changes, also build the affected Docker image and verify the changed flow in a disposable deployment. Do not send real invitation emails during tests. Keep permission checks on the server and preserve the separate authorization requirements for operator restore.

## Pull requests

Write new repository text, issue descriptions, commit messages, and pull requests in English. Existing German and English product translations and website documentation remain maintained in both languages.

Explain what changes and why, link relevant issues, and list the checks performed and their results. State explicitly when checks were skipped. Update documentation when behavior or configuration changes. Flag compatibility changes, database effects, and operator actions needed for deployment.

Maintainers manage release tags and image publication. A contribution does not need to publish an image or create a release.

## Community and licensing

Follow the [Code of Conduct](CODE_OF_CONDUCT.md). Report conduct concerns confidentially to [conduct@meterya.com](mailto:conduct@meterya.com). Communicate respectfully and keep discussion focused on the work.

By contributing, you agree that your contribution may be distributed under the repository's [license](LICENSE). Preserve existing third-party license notices.
