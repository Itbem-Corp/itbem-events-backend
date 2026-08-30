# Security policy

## Reporting a vulnerability

Do not open a public issue. Use the repository's GitHub **Security** tab to submit a private vulnerability report. Include the affected component, reproduction steps, impact, and any known mitigations. Do not include production credentials or personal data.

Maintainers should acknowledge a report within two business days, establish severity and containment within five business days, and coordinate disclosure only after a fix or compensating control is available.

## Supported versions

Only the current production release and the active staging release receive security fixes. Dependencies with known high or critical exploitable vulnerabilities block release.

## Handling requirements

- Never commit credentials, private keys, database exports, access tokens, or customer data.
- Rotate any credential suspected of exposure; deleting it from Git history is not sufficient.
- Use short-lived OIDC credentials for automation and SSM Session Manager for host access.
- Keep security logs free of bearer tokens, query credentials, cookies, and personal data.
- Production security changes require peer review, a tested rollback, and evidence recorded with the release.

