#!/bin/sh
# Fetch the ONNX Runtime shared library and a YOLO object-detection model so the
# subservice can be built/run with real object detection (-tags onnx).
#
# These artefacts are intentionally NOT committed to the repository (the model
# is ~63 MB and the runtime is platform specific). Run this script once on the
# host that will run the subservice.
#
# Usage:  sh models/setup.sh [ort_version]
#
# After running, build with:
#   ONNXRUNTIME_LIB=$(pwd)/models/onnxruntime-linux-x64-<ver>/lib/libonnxruntime.so \
#   go build -tags onnx -o imagerecognition_linux_amd64 .
set -e

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ORT_VERSION="${1:-1.26.0}"      # Must match the onnxruntime_go C API (see go.mod)
OS="linux-x64"

echo "==> ONNX Runtime $ORT_VERSION ($OS)"
curl -fsSL -o "$DIR/ort.tgz" \
    "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/onnxruntime-${OS}-${ORT_VERSION}.tgz"
tar -xzf "$DIR/ort.tgz" -C "$DIR"
rm -f "$DIR/ort.tgz"
echo "    lib: $DIR/onnxruntime-${OS}-${ORT_VERSION}/lib/libonnxruntime.so"

echo "==> tiny-yolov3 object model (COCO-80, ONNX model zoo, public domain)"
curl -fsSL -o "$DIR/yolov3tiny.onnx" \
    "https://github.com/onnx/models/raw/main/validated/vision/object_detection_segmentation/tiny-yolov3/model/tiny-yolov3-11.onnx"
echo "    object: $DIR/yolov3tiny.onnx ($(wc -c < "$DIR/yolov3tiny.onnx") bytes)"

echo "==> YuNet face detector (OpenCV Zoo, MIT)"
curl -fsSL -o "$DIR/yunet.onnx" \
    "https://media.githubusercontent.com/media/opencv/opencv_zoo/main/models/face_detection_yunet/face_detection_yunet_2023mar.onnx"
echo "    faces:  $DIR/yunet.onnx ($(wc -c < "$DIR/yunet.onnx") bytes)"

echo "==> SFace face recognizer (OpenCV Zoo, Apache-2.0)"
curl -fsSL -o "$DIR/sface.onnx" \
    "https://media.githubusercontent.com/media/opencv/opencv_zoo/main/models/face_recognition_sface/face_recognition_sface_2021dec.onnx"
echo "    embed:  $DIR/sface.onnx ($(wc -c < "$DIR/sface.onnx") bytes)"

echo "Done. model.json already references all three models."
echo "Set ONNXRUNTIME_LIB to the libonnxruntime.so path above when running."
