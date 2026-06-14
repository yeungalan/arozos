# ArozOS Image Recognition Subservice

A self-contained [ArozOS subservice](https://github.com/aroz-online/ArozOS-Subservice-Example)
that adds AI **photo recognition** to ArozOS:

- **Image tagging** — descriptive scene/colour tags out of the box, plus real
  YOLO object classes (`person`, `car`, `dog`, …) when the optional ONNX
  backend is enabled.
- **Face detection** — locate faces in a photo (pure-Go, via
  [pigo](https://github.com/esimov/pigo)).
- **Face recognition** — group **the same person across photos** under a stable
  **UUID** and return it to the caller. The gallery persists to disk, so a
  person keeps their UUID across restarts.

It is consumed by ArozOS through the **`imagerecognition`** AGI library and is
configured in **System Settings → AI Integration → Photo Recognition**, which
detects the subservice automatically once installed.

## How it works

```
AGI script ──requirelib("imagerecognition")──► ArozOS ──HTTP──► this subservice
                                                                  │
                                  ┌───────────────────────────────┤
                                  ▼                               ▼
                          image tagging                   face detection (pigo)
                       (scene + YOLO/ONNX)                        │
                                                          identity descriptor
                                                                  │
                                                     online clustering → person UUID
                                                          (persisted gallery)
```

A fresh VM-free HTTP server speaks the standard subservice protocol: it answers
`-info` with its module metadata and `-port`/`-rpt` to start serving.

## HTTP API

All endpoints accept an image as a multipart `image` file, an `image_b64`
form field, or a raw image body. Responses are JSON.

| Method & path | Purpose |
|---------------|---------|
| `GET  /api/info` | Backend, capabilities, known-people count |
| `POST /api/tag` | `{ tags: [{label, confidence, source}] }` |
| `POST /api/face/detect` | `{ faces: [{box, confidence}] }` (no grouping) |
| `POST /api/face/recognize` | `{ faces: [{box, personUUID, newPerson, matchScore}] }` |
| `POST /api/analyze` | `{ width, height, tags, faces, backend }` |
| `GET  /api/face/people` | Known person gallery (UUIDs + sample counts) |
| `POST /api/face/reset` | Clear the people gallery |

## Build & install

```sh
# Pure-Go build for this host (object tags from the builtin scene tagger):
sh build.sh

# …or with real YOLO object detection. On linux/amd64 the model + ONNX Runtime
# are bundled in models/, so this is turnkey; other platforms run models/setup.sh
# first (see models/README.md):
ONNX=1 sh build.sh
```

Place the resulting `imagerecognition_<os>_<arch>` binary (plus the `web/` and,
for ONNX, `models/` folders) in your ArozOS install under
`./subservice/imagerecognition/` and restart ArozOS. `build.sh DEPLOY=...`
does this for you.

## Configuration (environment)

| Variable | Default | Meaning |
|----------|---------|---------|
| `IMGRECOG_DATA` | `<exe dir>/data` | Where the people gallery is stored |
| `IMGRECOG_FACE_THRESHOLD` | `0.80` | Cosine similarity to treat two faces as the same person |
| `IMGRECOG_MODELS` | `<exe dir>/models` | Models directory (ONNX build) |
| `ONNXRUNTIME_LIB` | — | Path to `libonnxruntime.so` (ONNX build) |

## Recognition accuracy

The builtin face embedding is a deterministic appearance + gradient descriptor.
It reliably groups the same face across re-encodes, crops, brightness changes
and detector jitter (validated in `recognizer_test.go`), and separates clearly
different people. For maximum cross-pose/lighting accuracy, plug a learned face
embedding model into the `FaceEmbedder` interface (see `engine.go`).

## Tests

```sh
go test ./...                 # builtin path (no model needed)
ONNX=1 ... go test -tags onnx ./...   # also compiles the ONNX backend
```

Tests run against real photos in `testdata/` (see `testdata/NOTICE.md` for
provenance/licensing).

## Licensing

Subservice code: GPLv3 (same as ArozOS). Dependencies are permissive:
pigo (MIT), onnxruntime_go (MIT), golang.org/x/image (BSD-3).
