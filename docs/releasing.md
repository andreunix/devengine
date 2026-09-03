# Releasing

1. Ensure `CHANGELOG.md` describes the release and any upgrade/rollback notes.
2. Confirm the main branch is green and create an annotated SemVer tag, for
   example `git tag -a v0.2.0 -m "v0.2.0"`.
3. Push the tag. The release workflow reruns module hygiene, vet/race tests,
   PostgreSQL 17 and 18 integration tests, vulnerability scanning, and API
   compatibility against the previous tag.
4. GitHub Release is created only after every gate succeeds.

Tags are release artifacts and must not be moved or reused. If a release is
bad, publish a new patch version. GitHub repository administrators must enable
a ruleset that protects `main`, prevents tag updates/deletions, and requires
the CI, Security, API Compatibility, and PostgreSQL matrix checks. Secret
scanning and push protection must also be enabled in repository settings.
Those controls are external GitHub settings and cannot truthfully be activated
by files in this repository alone.

The workflow needs only the repository-provided `GITHUB_TOKEN`; its release job
has `contents: write`, while all build and verification jobs remain read-only.
