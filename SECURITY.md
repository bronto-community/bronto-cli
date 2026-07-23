# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities **privately** via GitHub's security
advisory flow: [Report a vulnerability](https://github.com/bronto-community/bronto-cli/security/advisories/new).
Do not open a public issue for security reports.

You can expect an initial response within a few business days. Please include
a description, reproduction steps, and the affected version (`bronto --version`).

## Supported versions

Only the latest release receives security fixes. Update with your package
manager or from the [releases page](https://github.com/bronto-community/bronto-cli/releases).

| Version | Supported |
| --- | --- |
| latest release | yes |
| older releases | no |

## Verifying releases

Release checksums are signed with [cosign](https://docs.sigstore.dev/) (keyless):

```sh
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'github.com/bronto-community/bronto-cli' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --check --ignore-missing checksums.txt
```

Release archives and packages also carry signed [SLSA build provenance](https://slsa.dev/).
Verify that an artifact was built by this repo's release workflow with:

```sh
gh attestation verify bronto_<version>_<os>_<arch>.tar.gz --repo bronto-community/bronto-cli
```

SPDX SBOMs for every archive are attached to each release; container images
on ghcr.io carry provenance and SBOM attestations.

## Scope notes

- `bronto` stores API keys in the OS keychain where available (file fallback
  with restrictive permissions otherwise); it never transmits keys anywhere
  except the configured Bronto API host.
- The plugin mechanism (`bronto-<name>` on PATH) executes local binaries by
  design; treat plugin installation with the same care as installing any
  executable.
