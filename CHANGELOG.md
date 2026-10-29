# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Read-only MCP server (Streamable HTTP) for go2rtc with `list_cameras` and `get_snapshot`.
- Stream sources (which contain camera credentials) are never exposed; only known camera names
  are passed to go2rtc.
- Optional camera allowlist, bearer token, snapshot width defaults and cap, `/healthz`.
- `-version` flag.
