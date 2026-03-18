# Changelog

All notable changes to axme-cli are documented in this file.

This project follows [Semantic Versioning](https://semver.org/). During alpha (`0.x.y`), breaking changes may occur in minor version bumps.

---

## [v0.2.3] — 2026-03-10

### Fixed
- Command aliases for common subcommands
- Double-refresh bug when token was close to expiry
- Improved update hint messaging

### Changed
- Diagrams now use raw GitHub URLs from axme-docs (no proxy)
- README updated for Track H (passwordless login, workspace members, update command)

## [v0.2.2] — 2026-03-09

### Added
- Proactive pre-expiry token refresh — JWT is refreshed automatically before it expires, eliminating mid-command 401 errors
- `jwtSecondsUntilExpiry` utility for token lifetime inspection
- `axme workspace members` namespace — list, include, and exclude members at workspace level
- Post-login workspace prompt — CLI prompts for workspace selection after successful login

## [v0.2.1] — 2026-03-09

### Fixed
- Replaced misleading keyring warning with accurate server-side error message when credential storage falls back to file

## [v0.2.0] — 2026-03-09

### Added
- Email-first OTP login as the default `axme login` flow (no browser, no API key copy)
- Account-session login, workspace commands, and secure secret storage (Track H Slice 1)
- Background update check + `axme update` command
- Auto-refresh JWT on 401 `invalid_actor_token` with persisted rotated tokens

### Fixed
- Release installer compatibility with GitHub release assets
- `axme quota show` reads `body.overview.quota_policy` correctly; removed stale email hint
- Session revoke now produces human-readable output
- `axme workspace use` human-readable output, logout cleanup, JWT auto-refresh
- Doctor output cleaned up; removed server detail leakage
- CLI UX audit — readable output, keyring auto-fallback, silent error handling

## [v0.1.0] — 2026-03-08

### Added
- Initial public alpha release
- Go-first CLI surface: `axme intents`, `axme context`, `axme status`, `axme doctor`, `axme version`
- `axme login` as the alpha onboarding entry point (email OTP + browser device flow)
- Service-account lifecycle commands (`axme service-accounts create/list/keys`)
- Actor-token auth flow (`--actor-token` flag)
- Admin command group for platform operators
- Release installer (`install.sh`) for Linux and macOS
- README with diagrams, command reference, and quick-start guide

[v0.2.3]: https://github.com/AxmeAI/axme-cli/releases/tag/v0.2.3
[v0.2.2]: https://github.com/AxmeAI/axme-cli/releases/tag/v0.2.2
[v0.2.1]: https://github.com/AxmeAI/axme-cli/releases/tag/v0.2.1
[v0.2.0]: https://github.com/AxmeAI/axme-cli/releases/tag/v0.2.0
[v0.1.0]: https://github.com/AxmeAI/axme-cli/releases/tag/v0.1.0
