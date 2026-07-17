# Surveillance Subservice

A self-contained ArozOS **subservice** that provides the foundation of a
browser-based IP-camera surveillance platform. It runs as its own binary,
reverse-proxied by ArozOS under `/surveillance/`, and appears on the desktop
like any other module.

This first increment implements **camera management** — the base every later
feature (live view, recording, playback, analytics …) builds on. See
[`docs/surveillance-requirements.md`](../docs/surveillance-requirements.md) for
the full product specification and the roadmap of what is / isn't built yet.

## What it does today

- **Camera CRUD** — add, edit, remove, enable/disable cameras. Each camera
  carries name, description, RTSP URL, separate username/password, stream type,
  transport (TCP/UDP/auto), codec, manufacturer, model, resolution, FPS,
  recording settings, status and last-seen timestamp (spec §2.1).
- **Secure credential handling** — passwords are stored server-side and never
  serialised back to clients; the backend builds the final authenticated RTSP
  URL on demand (spec §2.2). A blank password on update keeps the existing one.
- **Grouping & tags** — organise cameras by building / floor / location and by
  free-form tags; deleting a group cleanly un-links its cameras.
- **Search & filter** — by name, description, manufacturer, status, group, tag
  (spec §14).
- **RTSP stream validation** — a pure-Go probe (no ffmpeg dependency) that dials
  the camera, runs `OPTIONS`/`DESCRIBE`, distinguishes *unreachable* /
  *timeout* / *authentication failed* / *invalid URL*, and parses the returned
  SDP to detect the codec (and resolution/FPS when advertised) — spec §2.2.
- **Camera health status** — a "Test" action probes a stored camera and records
  its online/offline status and last-seen time (foundation for spec §11).

## Architecture

- **Single portable binary**, no cgo and no external Go dependencies. The
  front-end (`web/`) is embedded with `go:embed`, so the binary is fully
  self-contained.
- **Persistence** is an atomically-written JSON file (`data/cameras.json`) kept
  beside the binary — durable without pulling in an SQL engine, keeping the
  cross-platform / no-system-dependency guarantee.
- **Every route** is namespaced under `/surveillance/` because ArozOS proxies
  that prefix to the binary without stripping it.

## Build & deploy

```bash
cd surveillance
make test          # run the Go test suite
make deploy        # cross-compile for the host and stage into src/subservice/surveillance/
```

`make deploy` places `surveillance_<GOOS>_<GOARCH>` into
`src/subservice/surveillance/` (which is git-ignored — binaries are never
committed). ArozOS discovers it on the next start (or via **System Settings →
Subservices → Start**), probes it with `-info`, then launches it as:

```
surveillance_<GOOS>_<GOARCH> -port :<port> -rpt http://localhost:<arozosPort>/api/ajgi/interface
```

## HTTP API (all under `/surveillance/`)

| Method & path | Purpose |
|---|---|
| `GET /api/cameras` | List cameras (passwords redacted) |
| `POST /api/cameras` | Add a camera |
| `GET /api/cameras/{id}` | Get one camera |
| `PUT /api/cameras/{id}` | Update a camera |
| `DELETE /api/cameras/{id}` | Remove a camera |
| `POST /api/cameras/{id}/enable` · `/disable` | Toggle a camera |
| `POST /api/cameras/{id}/test` | Probe a stored camera and record status |
| `GET /api/groups` · `POST /api/groups` | List / create groups |
| `PUT /api/groups/{id}` · `DELETE /api/groups/{id}` | Update / delete a group |
| `GET /api/tags` | Distinct tags in use |
| `GET /api/search?q=&status=&group=&tag=&manufacturer=` | Filtered camera search |
| `POST /api/validate` | Probe an unsaved RTSP URL |

## Not yet implemented

Live streaming/transcoding, recording, playback, motion detection, AI
analytics, notifications, ONVIF discovery, maps and multi-user roles are
specified in the requirements doc but out of scope for this camera-management
foundation. Live media will require the streaming engine (ffmpeg/WebRTC) that
this subservice is structured to grow into.
