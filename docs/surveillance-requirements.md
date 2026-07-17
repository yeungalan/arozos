# Surveillance System — Requirements & Roadmap

This document records the full product specification for the ArozOS
Surveillance system and tracks which parts are implemented. The implementation
lives in the [`surveillance/`](../surveillance/) subservice (a self-contained,
reverse-proxied binary) — see its [README](../surveillance/README.md).

## Status legend

- **[done]** — implemented in the current subservice
- **[partial]** — foundation in place, more work needed
- **[planned]** — specified, not yet built

## 1. Overview

A browser-based surveillance management platform to connect, monitor, record and
manage IP cameras from a central web interface, for both local networks and
remote deployments, with an emphasis on low latency, reliability, scalability and
ease of use.

## 2. Camera management & RTSP

- **[done]** Add / edit / remove / enable / disable cameras.
- **[done]** Group cameras by location / floor / building; tag cameras.
- **[done]** Per-camera fields: name, description, RTSP URL, stream type,
  manufacturer, model, resolution, FPS, recording settings, status, last-seen.
- **[done]** RTSP without auth, RTSP basic auth (embedded or separate
  username/password fields); backend constructs the final connection securely.
- **[done]** Transport selection: TCP / UDP / auto.
- **[done]** Codec support (validated set): H.264, H.265, MJPEG, plus AV1 and
  MPEG4 accepted for forward-compatibility.
- **[partial]** Stream validation: connectivity, credential check, codec
  detection and (when advertised) resolution/FPS are probed over RTSP with a
  pure-Go handshake. Bitrate detection and Digest-auth verification require the
  streaming engine and are not yet done.

## 3. Live view — **[planned]**

Single/multi-camera views, fullscreen, PiP, low-latency playback, mute, digital
zoom, snapshot; 1/2/4/6/9/16/25/custom layouts with drag-and-drop and saved
layouts. Requires the streaming engine (ffmpeg/WebRTC).

## 4. Recording — **[partial]**

Recording *intent* (mode: continuous/motion/scheduled/manual, format: MP4/MKV,
retention days, max storage) is stored per camera. Actual capture, storage
back-ends (local/NAS/network share/S3) and retention enforcement are
**[planned]**.

## 5. Playback — **[planned]**

Timeline + calendar playback, fast-forward, slow motion, frame stepping, clip
download/export, snapshot; search by time/camera/motion event.

## 6. Motion detection — **[planned]**

Server-side or camera built-in; sensitivity, zones, schedule, ignore regions;
motion-detected / motion-ended events.

## 7. AI analytics — **[planned]**

Person/vehicle/animal/face/plate detection, object counting, intrusion, line
crossing, loitering.

## 8. Event management — **[planned]**

Event types (motion, camera on/offline, recording failure, storage full, AI
detection, login attempt, system error) with timestamp, camera, severity,
description and snapshot.

## 9. Notifications — **[planned]**

Email, Discord, Slack, Telegram (SMS future); rules for motion, camera offline,
storage full, AI events.

## 10. User management — **[planned]**

Roles (Administrator / Operator / Viewer). ArozOS already provides
username/password auth and per-module permissions the subservice inherits via
the reverse proxy; surveillance-specific roles are future work.

## 11. Camera health monitoring — **[partial]**

Online/offline status and last-seen are tracked and updatable via the per-camera
"Test" probe. Stream FPS, bitrate, packet loss, latency, CPU/memory and the
historical-uptime dashboard are **[planned]** (need the streaming engine).

## 12. Camera discovery — **[planned]**

ONVIF discovery, IP/network scan. Manual RTSP entry is **[done]**.

## 13. Dashboard — **[partial]**

Header shows online / offline / total / recording counts. The full widget
dashboard (storage, CPU, memory, bandwidth, motion-today) is **[planned]**.

## 14. Search — **[done]**

Search cameras by name, location (group), tags, status and manufacturer.

## 15. Maps — **[planned]**

Floor plans, building maps, camera placement/direction, click-to-open live
stream.

## Roadmap summary

1. **Camera management foundation** — *this increment* (done).
2. **Streaming engine** — ffmpeg/WebRTC subservice for live view + health
   metrics.
3. **Recording & playback** — capture pipeline, storage back-ends, timeline.
4. **Events & notifications** — event bus + delivery channels.
5. **Motion & AI analytics** — server-side detection, optional models.
6. **Discovery & maps** — ONVIF/IP scan, floor-plan placement.
