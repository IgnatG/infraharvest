# Security policy

## Supported versions

Only the latest release gets security fixes: a fix comes in a new release, not as a patch to an older one. Prereleases and builds from `main` aren't supported. Upgrade to the latest release before reporting, if you can.

## Reporting a vulnerability

Please report vulnerabilities privately, and don't open a public issue. Use GitHub's private vulnerability reporting: [report a vulnerability](https://github.com/IgnatG/infraharvest/security/advisories/new) (the repository's **Security → Report a vulnerability**).

Include the affected version or commit, how to reproduce the issue, and its impact.

## What happens next

- **Acknowledgement** within 5 working days.
- **Assessment** within 10 working days of that: whether it is a vulnerability, how severe, and what a fix involves. You hear the outcome either way.
- **A fix** in a new release, aimed at within 90 days of the report, sooner for a severe one. You're kept up to date until it ships.
- **Disclosure**: a GitHub security advisory, with a CVE where one applies, once the fix is released. You can agree a different date with us. Reporters are credited in the advisory unless they ask not to be.

## Scope

In scope, for example:

- A secret reaching the generated configuration, the report or the logs (see [Output](docs/output.md)).
- A permission template under `permissions/` that grants more than read access, or reads data its documentation says it denies.
- infraharvest writing, or making Terraform apply, a change to a cloud during an import, verification or drift run.
- The release artifacts, container image, install script or GitHub Action: their signatures, checksums or provenance.

Not in scope: vulnerabilities in Terraform, OpenTofu, the providers or the clouds themselves (report those to their maintainers), and what a configuration does once you apply it.
