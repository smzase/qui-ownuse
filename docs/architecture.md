# Architecture Notes

Internal reference for agents and maintainers. Read this before changing cross-module data flow, service boundaries, API routing, or long-lived architecture. For user-facing docs, use `documentation/docs/`.

## Module Map

- `cmd/qui/main.go`: CLI entrypoint for serve, config generation, user creation, and other commands.
- `internal/api/`: HTTP handlers, middleware, and routing.
- `internal/qbittorrent/`: qBittorrent client pool and sync manager.
- `internal/transmission/`: Transmission RPC client plus a protocol bridge (`http.RoundTripper`) that serves the qBittorrent WebUI API v2 surface from RPC translations, so the whole qBittorrent pipeline (client pool, sync manager, SSE, handlers) works unchanged against a Transmission daemon. See [Transmission support](#transmission-instances).
- `internal/services/`: domain services such as cross-seed, Jackett/Torznab, reannounce, and tracker rules.
- `internal/fsops/`: filesystem backend abstraction. Service callsites are migrating from direct `os.*` calls to an `fsops.Backend` (migration in #1915) resolved per instance via `fsops.Pool`. The pool returns the local backend for instances with local filesystem access, returns the SFTP-backed remote backend (`internal/fsops/remote`; implements writes over sftp but advertises only Read until #2942, reflinks unsupported) for an instance with SSH credentials and a confirmed host-key pin, and returns a noop backend otherwise (every filesystem op fails with `ErrNoFilesystemAccess`, or with the context error). The remote backend slots in at the pool (design: `docs/remote-backend-design.md`).
- `internal/sshpool/`: SSH connections for remote instances. One-shot dials with the stored key, host-key pinning against the confirmed pin and the read-only capability probe serve `ssh-test`; `Pool` keeps one persistent SSH+SFTP connection per instance for the remote backend, with keepalive, reconnect backoff, and an in-memory refusal memo for a mismatched or unreadable pin that a changed pin or a restart clears; after a restart, the next dial refuses an unchanged wrong key again.
- `internal/proxy/`: reverse proxy support for external apps.
- `internal/backups/`: scheduled snapshots.
- `internal/database/`: SQLite/Postgres migrations and database setup.
- `internal/models/`: data models and store interfaces.
- `pkg/`: shared utilities.
- `web/src/`: frontend. Its stack and rules are in `web/AGENTS.md`.

## Core Data Flow

1. `SyncManager` polls qBittorrent instances through `ClientPool`.
2. Torrent state is cached in memory with delta updates.
3. Frontend reads state through REST APIs and receives live updates through SSE.
4. Cross-seed services react to torrent completion and search/match events.

## Transmission Instances

Instances carry a `client_type` (`qbittorrent` default, `transmission` opt-in). The client pool routes Transmission instances through `internal/transmission`: the pooled go-qbittorrent client uses the bridge as its HTTP transport, allowing the shared sync, API, and SSE pipeline to remain client-agnostic. Feature-sensitive UI and services still inspect the normalized client type so they do not offer qBittorrent-only operations. The bridge reports a synthetic Web API version (2.7.0), while explicit Transmission capabilities enable tracker editing, file priority, renames, ratio and idle-seeding limits, and tags. Comments, torrent creation/export, categories, subcategories, and qBittorrent-only share-limit actions remain unsupported. Transmission labels are exposed as tags, including per-torrent tag edits; categories are intentionally empty. The maindata sync is served as rid-based deltas computed from `torrent-get` polls.

## Frontend Live State Note

`web/src/components/torrents/TorrentDetailsPanel.tsx` live row state such as speed, progress, ratio, and state is stream-backed via `useSyncStream`. Polling only runs as a fallback while the stream is unavailable. Content/files and Peers tabs still poll on an interval, but polling is tab-scoped and visibility-gated, so streaming them is optional future work rather than a pending migration.
