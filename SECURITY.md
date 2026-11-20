# Security policy

## Reporting a vulnerability

Use [GitHub private vulnerability reporting](https://github.com/gabisonia/go-vectorstore/security/advisories/new).
Private reporting is enabled for this repository. Please do not disclose exploit
details, credentials or private application data in a public issue or pull request.

Include the affected library/Go/database/pgvector versions, a minimal reproducer,
impact, and any suggested mitigation. Use synthetic data. For ordinary bugs and
usage questions, use the public issue tracker. If private reporting becomes
unavailable, open an issue requesting a private contact without technical details
of the vulnerability.

## Supported versions and maintenance

Security fixes target the latest published patch of the latest published minor
release line. Older lines do not have a promised backport schedule.
`VERSION` may identify a release being prepared; consult
[GitHub releases](https://github.com/gabisonia/go-vectorstore/releases) for available
tags. Development master is not a supported release substitute.

The project does not promise a response/remediation SLA. Reports are assessed by
impact and available evidence. Publish coordinated advisories when appropriate;
do not interpret an open-source issue response as an operational support contract.

CI runs govulncheck against production and integration dependencies. The scheduled
hardening workflow repeats scanning weekly even without source changes and runs
extended fuzz campaigns. Maintainers must monitor workflow failures, triage new
advisories, and publish fixes; automation does not itself remediate dependencies.

## Application responsibilities

Use a patched Go toolchain and database/driver versions. Applications own database
credentials, TLS, authorization/tenant filtering, embedding-provider configuration,
resource budgets, backups and data retention. The library does not authenticate
end users or isolate tenants automatically. Treat retrieved content as untrusted
input when passing it to a generator or agent tool.

See [production operation](docs/production.md) and the
[compatibility/release policy](docs/releases.md).
