# go2rtc-mcp

A small, read-only [MCP](https://modelcontextprotocol.io) server that lets an AI assistant take
live snapshots from cameras managed by [go2rtc](https://github.com/AlexxIT/go2rtc) (standalone or
the one built into Frigate).

```
MCP client (e.g. signal-ollama) ⇄ go2rtc-mcp ⇄ go2rtc
        POST /mcp (Streamable HTTP)      /api/streams, /api/frame.jpeg
```

## Tools

| Tool | Input | Returns |
|---|---|---|
| `list_cameras` | none | Camera names, e.g. `{"cameras": ["driveway", "front_door"]}` |
| `get_snapshot` | `camera`, optional `width` | A live JPEG as MCP image content, plus a timestamp line |

Both tools are marked read-only. There is no way to add, change or delete streams.

## Safety

- **Names only.** Stream sources in go2rtc usually contain camera usernames, passwords and IPs.
  Only stream names are read from `/api/streams`; sources never leave the server.
- **Known cameras only.** `get_snapshot` only accepts a name returned by `list_cameras`, so a client
  can't make go2rtc open an arbitrary URL.
- **Optional camera allowlist** (`CAMERAS`) to expose only some cameras.
- **Optional bearer token** (`AUTH_TOKEN`) for MCP clients.
- Runs as a non-root user in a minimal image with no shell.

## Configuration

| Variable | Default | |
|---|---|---|
| `GO2RTC_URL` | required | go2rtc API, usually `http://<host>:1984` |
| `GO2RTC_USERNAME` / `GO2RTC_PASSWORD` | none | If go2rtc's API has basic auth |
| `CAMERAS` | all | Comma-separated allowlist (case-insensitive) |
| `SNAPSHOT_WIDTH` | `1280` | Default width; `0` = native resolution |
| `MAX_WIDTH` | `1920` | Cap on the width a client may request; `0` = no cap |
| `AUTH_TOKEN` | none | Require `Authorization: Bearer <token>` on `/mcp` |
| `LISTEN_ADDR` | `:8090` | |

## Run

```yaml
services:
  go2rtc-mcp:
    image: ghcr.io/tfielding/go2rtc-mcp:latest
    restart: always
    environment:
      - GO2RTC_URL=http://<go2rtc-host>:1984
```

MCP clients on the same Docker network connect to `http://go2rtc-mcp:8090/mcp`.
`GET /healthz` returns 200 when go2rtc is reachable.

With Frigate, go2rtc's API is on port 1984 of the Frigate container. Either put go2rtc-mcp on the
same Docker network as Frigate, or publish port 1984 from Frigate.

Quick check that snapshots work, before involving MCP at all:

```bash
curl -o test.jpg "http://<go2rtc-host>:1984/api/frame.jpeg?src=<camera>"
```

Locally:

```bash
GO2RTC_URL=http://<go2rtc-host>:1984 go run .
```

## Develop

```bash
go test -race ./...
```

Tests run the real MCP client from the official Go SDK against the server and a fake go2rtc.

## License

MIT. Third-party licenses are in `THIRD_PARTY_LICENSES` (also copied into the image at `/licenses`).
