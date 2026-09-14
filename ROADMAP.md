# Roadmap

This roadmap tracks the project direction at a high level. Completed work is recorded in `CHANGELOG.md`.

## Current Focus

- Upstream synchronization: regularly merge `upstream/main` while preserving fork-specific fixes (see `docs/MERGE-PRESERVATION-fork-fixes.md`).
- Devin provider support: OAuth login flow, quota fetching, model registry, and executor hardening.

## Near Term

- Keep the management UI (`frontend` submodule) and the served `static/management.html` bundle in sync with fork features (e.g. "Do Not Use Proxy" toggle).
- Maintain storage backend parity (file, Postgres, git, object store).

## Deferred / Intentionally Not Planned

- No speculative features beyond what upstream or active fork fixes require.
