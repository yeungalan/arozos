# Models — optional YOLO object detection (ONNX)

The subservice has two object-tagging backends:

| Backend | Build | Object tags |
|---------|-------|-------------|
| **builtin** (default) | `go build` | Scene/colour tags + a `person`/`people` tag derived from face detection. Pure Go, runs everywhere. |
| **onnx-yolo** | `go build -tags onnx` | Real object classes (`person`, `car`, `dog`, …) from a YOLO model run through ONNX Runtime. |

Face detection and face **recognition** (grouping the same person under a UUID)
work in *both* builds — they use the pure-Go pigo detector and the builtin
identity descriptor and need no model.

## Enabling the ONNX/YOLO backend

The model (`tinyyolov2-8.onnx`, tiny-yolov2 VOC, public-domain) **and** the
ONNX Runtime shared library for **linux/amd64** are committed in this folder, so
on linux/amd64 the ONNX backend is turnkey — just build with the tag:

```sh
go build -tags onnx -o imagerecognition_linux_amd64 .
```

`model.json` already points `sharedLibrary` at the bundled
`onnxruntime-linux-x64-1.26.0/lib/libonnxruntime.so.1.26.0`, so no environment
variable is needed. If the model or runtime is missing (e.g. on another
platform) the service logs a notice and falls back to the builtin backend, so an
`-tags onnx` build still runs everywhere.

### Other platforms / refreshing the artefacts

For non-linux/amd64 hosts, fetch the matching runtime (and re-fetch the model):

```sh
sh models/setup.sh
ONNXRUNTIME_LIB=/path/to/your/libonnxruntime.so \
  go build -tags onnx -o imagerecognition_<os>_<arch> .
```

`ONNXRUNTIME_LIB` (when set) overrides the `sharedLibrary` path in `model.json`.

## `model.json`

Describes the detector so other YOLO-family models can be swapped in without
code changes:

| Field | Meaning |
|-------|---------|
| `objectModel` | ONNX file name in this folder |
| `inputName` / `outputName` | Model tensor names |
| `inputSize` / `gridSize` / `numClasses` | Geometry (tiny-yolov2: 416 / 13 / 20) |
| `anchors` | Flat anchor pairs in grid units |
| `classes` | Class label names |
| `confThreshold` / `iouThreshold` | Detection + NMS thresholds |
| `sharedLibrary` | Optional path to `libonnxruntime`; `ONNXRUNTIME_LIB` overrides it |

The bundled `model.json` is configured for ONNX model zoo **tiny-yolov2**
(input `image` `[1,3,416,416]`, output `grid` `[1,125,13,13]`), which
`onnx_backend.go` decodes.
