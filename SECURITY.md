# Security policy

## Supported versions

Only the latest stable release of this repository receives security fixes. Older releases and unreleased development commits are not supported release lines. Updates to bundled components may require updating their images as well.

The `latest` image tag is mutable. Include the exact release tag, source commit, or image digest when reporting an issue. For the installation repository, also include the affected API, Keycloak, and app versions.

## Reporting a vulnerability

Do not disclose suspected vulnerabilities in public issues, discussions, pull requests, or their attachments.

GitHub Private Vulnerability Reporting is enabled for this repository.

Use [Report a vulnerability](https://github.com/roman-kiefer/meterya-api/security/advisories/new) under this repository's **Security → Advisories** tab. If the form is unavailable, do not post vulnerability details publicly or use the public bug form. You may open an issue saying only that a confidential reporting channel is unavailable, without exploit details or sensitive information.

Reports about installation and cross-component issues belong in Meterya; backend and bundled Keycloak issues in Meterya API; web interface issues in Meterya App. If the affected component is unclear, use the private reporting channel in [Meterya](https://github.com/roman-kiefer/meterya/security/advisories/new), once available. One report with links to affected components is sufficient.

Include:

- Affected component, release or image digest, and relevant deployment details.
- A description of the issue, potential impact, and required permissions or access.
- Minimal reproduction steps or a proof of concept using synthetic data.
- Any suggested fix or mitigation, if known.

Do not include production credentials, active tokens, private household data, or personal photos. Test only systems you own or have permission to assess, without disrupting other users.

Maintainers will assess reports, discuss reproduction and mitigation privately, and coordinate a fix and disclosure with the reporter. No fixed response or resolution time is promised.
