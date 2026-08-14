# Independent maintenance policy

This repository is independently maintained. It owns its issue tracker, CI,
GHCR images, database releases, security process, API compatibility decisions,
and data-quality gates. It is not affiliated with or endorsed by the upstream
authors.

Independence does not erase provenance: inherited Git authorship, GPL-3.0-only
obligations, and source-data notices remain intact.

## Version policy

- Program releases and data releases are versioned explicitly.
- `vMAJOR.MINOR.0` publishes an immutable data release and its matching image;
  later patch tags in that minor line publish image-only fixes and remain bound
  to the `vMAJOR.MINOR.0` data release.
- Containers and databases must not depend on mutable `latest` artifacts.
- Every database release identifies its schema, generator, and exact source
  commit in a machine-readable manifest.
- Every considered source file and accepted/rejected record is accounted for in
  a deterministic source report whose digest and totals are bound into that
  release manifest.
- Breaking identity or API changes require a new major API/data version and a
  documented migration map where possible.

## Upstream policy

Upstream changes may be reviewed and ported deliberately. There is no automatic
sync, rebase, or release from upstream. Every imported change must retain its
authorship and pass local quality gates.

## Release gate

A release requires passing tests, race detection, static analysis, dependency
scanning, database integrity checks, provenance checks, reproducible-identity
tests, atomic-startup regression tests, container policy checks, and a manual
review of known data-quality exceptions. A successful build is not evidence
that poetry text is authoritative.
