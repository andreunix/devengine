# Releasing

1. Ensure `CHANGELOG.md` describes the release and any upgrade/rollback notes.
2. Confirm the main branch is green and create an annotated SemVer tag, for
   example `git tag -a v0.4.0 -m "v0.4.0"`.
3. Push the tag. The release workflow reruns module hygiene, vet/race tests,
   PostgreSQL 17 and 18 integration tests, vulnerability scanning, and API
   compatibility against the previous tag.
4. GitHub Release is created only after every gate succeeds.

The current hardened pre-v1 baseline is `v0.4.0`. New backward-compatible fixes
use `v0.4.x`; intentional pre-v1 API changes require the next minor version.

Tags are release artifacts and must not be moved or reused. If a release is
bad, publish a new patch version. GitHub repository administrators must keep
`main` and release tags protected, require the CI, Security, API Compatibility,
and PostgreSQL matrix checks, and keep secret scanning and push protection
enabled.

The workflow needs only the repository-provided `GITHUB_TOKEN`; its release job
has `contents: write`, while all build and verification jobs remain read-only.
