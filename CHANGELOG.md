# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v7.2.158-arsydoni4326-alt]

### Added

- **Codex model-level cooling**: New configuration option `codex.model-level-cooling` that enables per-model quota cooldown instead of credential-level cooldown. When enabled, hitting a usage limit on one Codex model only cools down that specific model, allowing sibling models to continue working.
- **RapidProxy sponsor**: Added RapidProxy as a new sponsor in README files (English, Chinese, Japanese).
- **New test coverage**: Comprehensive tests for Codex quota failover behavior, Claude executor cloaking, auth conductor cooldown/selection logic, and model registry safety.

### Changed

- **Sponsor list**: Removed RunAPI from README sponsor tables in all language versions.
- **Config defaults**: Updated `config.example.yaml` with refined retry and cooldown settings including `request-retry`, `max-retry-credentials`, `max-retry-interval`, and quota backoff configuration.
- **Claude executor cloaking**: Improved cloaking logic for Claude Code requests including better fingerprint computation and billing header generation.
- **Codex executor**: Added model-level cooling support and improved terminal error handling for quota-related failures.
- **SDK auth conductor**: Enhanced auth lifecycle management, cooldown state persistence, session affinity lookups, and result policy application.
- **Model registry**: Improved model registration, capability tracking, and availability caching.
- **Client error handling**: Added better classification for upstream request faults including `IsItemNotPersisted` detection for responses with `store: false`.

### Fixed

- **Codex quota behavior**: Terminal quota errors now properly cool down the affected account and fail over to alternative credentials. Model-level cooling ensures sibling models are not affected by quota limits on a single model.
- **Auth refresh and persistence**: Improved handling of auth state updates, metadata merging, and refresh scheduling.

---

## [Previous Versions]

> Older changelog entries would appear here once established.
