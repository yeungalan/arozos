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

1. Fetch the runtime + model (not committed — large + platform specific):

   ```sh
   sh models/setup.sh
   ```

   This downloads ONNX Runtime and `tinyyolov2-8.onnx` (tiny-yolov2, VOC,
   public-domain) into this folder.

2. Build and run with the `onnx` tag, pointing at the runtime library:

   ```sh
   ONNXRUNTIME_LIB=$PWD/models/onnxruntime-linux-x64-1.26.0/lib/libonnxruntime.so \
     go build -tags onnx -o imagerecognition_linux_amd64 .
   ```

   At runtime the service auto-loads `model.json` from this folder; if the model
   or runtime is missing it logs a notice and falls back to the builtin backend,
   so an `-tags onnx` build still runs without the model present.

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
