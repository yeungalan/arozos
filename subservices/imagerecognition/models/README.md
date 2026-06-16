# Models — DNN backends (ONNX)

Building with `-tags onnx` enables three ONNX models (all bundled here for
linux/amd64, and all permissively licensed — see `NOTICE.md`):

| Capability | Model | Builtin fallback (default build) |
|------------|-------|----------------------------------|
| Object tags | `yolov3tiny.onnx` (tiny-yolov3, COCO-80) | scene/colour + composition tags + a person tag |
| **Face detection** | `yunet.onnx` (YuNet, + 5 landmarks) | pigo cascade |
| **Face recognition** | `sface.onnx` (SFace, 128-d embedding) | appearance/HOG descriptor |

The DNN face pair is the important upgrade: **YuNet** detects faces far more
reliably than the builtin cascade (no false positives on reflections/foliage,
catches non-frontal/low-light faces) and provides landmarks, which are used to
**align** each face before **SFace** produces an embedding. Cosine similarity on
those embeddings is what reliably groups the *same person across different
photos* under one UUID. Face detection + recognition still work in the default
(pure-Go) build via the fallbacks, just less accurately.

## Enabling on linux/amd64 (turnkey)

The models **and** the ONNX Runtime library are committed, so just build:

```sh
go build -tags onnx -o imagerecognition_linux_amd64 .
```

`model.json` points `sharedLibrary` at the bundled
`onnxruntime-linux-x64-1.26.0/lib/libonnxruntime.so.1.26.0`; no env var needed.
If any model/runtime is missing the service logs a notice and uses the builtin
fallback for that capability, so the build always runs.

## Other platforms / refreshing

```sh
sh models/setup.sh          # fetches runtime + all three models
ONNXRUNTIME_LIB=/path/to/libonnxruntime.so go build -tags onnx -o imagerecognition_<os>_<arch> .
```

`ONNXRUNTIME_LIB` overrides the `sharedLibrary` path in `model.json`.

## `model.json`

Three optional sections — `object`, `faceDetection`, `faceRecognition` — each
describing the model file, tensor names and thresholds. Drop a section to
disable that model (falling back to the builtin path). Key face knobs:

| Field | Meaning | Default |
|-------|---------|---------|
| `faceDetection.scoreThreshold` | `sqrt(cls*obj)` acceptance for a face | 0.7 |
| `faceDetection.nmsThreshold` | IoU for de-duplicating boxes | 0.3 |
| `faceRecognition.matchThreshold` | cosine similarity for "same person" | 0.363 |

The `matchThreshold` (0.363) is OpenCV's recommended SFace cut-off: same person
across pose/lighting scores above it, different people below.
