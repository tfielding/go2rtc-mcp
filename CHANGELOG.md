# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- `get_snapshots` tool: snapshots from several cameras (or all) in one call, fetched in parallel.
  A camera that fails is reported without failing the others.
- `camera_status` tool: each camera's state (`streaming`, `idle`, or with `check` `online` /
  `offline`), its video and audio codecs and viewer count.
- `EXCLUDE_CAMERAS`: hide cameras by name or pattern, e.g. `*_sub` to drop substreams.
- `SNAPSHOT_CACHE` (default `2s`): a camera's frame is reused for this long, and simultaneous
  requests share one fetch, so a looping model can't hammer cameras.
- `GO2RTC_TIMEOUT` (default `20s`, previously fixed): limit for each go2rtc request. Snapshots that
  time out now say so instead of a generic error.

### Security
- Snapshot errors no longer pass go2rtc's error text to the client, since it can contain camera
  addresses or source URLs. Clients now see "camera X is offline or unreachable"; the details are
  logged by go2rtc-mcp.

### Changed
- Docker `:latest` now points to the newest release. Builds from `main` are published as `:edge`,
  and releases also get a `major.minor` tag (e.g. `:0.1`).

## [0.1.0] - 2026-10-29

### Added
- Read-only MCP server (Streamable HTTP) for go2rtc with `list_cameras` and `get_snapshot`.
- Stream sources (which contain camera credentials) are never exposed; only known camera names
  are passed to go2rtc.
- Optional camera allowlist, bearer token, snapshot width defaults and cap, `/healthz`.
- `-version` flag.
