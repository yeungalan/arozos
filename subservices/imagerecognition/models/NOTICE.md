# Bundled models & runtime — provenance & licensing

All artefacts here are permissively licensed and may be redistributed
commercially (consistent with the repository's dependency policy).

| File | Purpose | Source | License |
|------|---------|--------|---------|
| `tinyyolov2-8.onnx` | Object detection (tags) | [ONNX Model Zoo – tiny-yolov2](https://github.com/onnx/models) | Public domain (PJReddie YOLO) |
| `yunet.onnx` | Face detection + landmarks | [OpenCV Zoo – face_detection_yunet](https://github.com/opencv/opencv_zoo) (`face_detection_yunet_2023mar.onnx`) | MIT © 2020 Shiqi Yu |
| `sface.onnx` | Face recognition embedding | [OpenCV Zoo – face_recognition_sface](https://github.com/opencv/opencv_zoo) (`face_recognition_sface_2021dec.onnx`) | Apache-2.0 |
| `onnxruntime-linux-x64-1.26.0/` | ONNX Runtime (linux/amd64) | [microsoft/onnxruntime](https://github.com/microsoft/onnxruntime) | MIT (see its `LICENSE`) |

Full upstream license texts: `LICENSE-yunet` (MIT) and `LICENSE-sface`
(Apache-2.0) in this directory, and `onnxruntime-linux-x64-1.26.0/LICENSE`.
