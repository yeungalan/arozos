#!/bin/sh
#
# setup.sh — Download ONNX Runtime and model files needed by photoai.
#
# Usage:
#   cd subservice/photoai && sh setup.sh
#
# Downloads go into ./models/ (override with MODEL_DIR=<path>).
# Safe to re-run; existing files are not overwritten.
#
# Windows: use setup.ps1 instead (PowerShell ≥5.1 required):
#   powershell -ExecutionPolicy Bypass -File setup.ps1
#
set -eu

MODEL_DIR="${MODEL_DIR:-$(pwd)/models}"
mkdir -p "$MODEL_DIR"

ORT_VER="1.20.1"
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"

# ---- ONNX Runtime shared library ----------------------------------------
ort_lib_name() {
    case "${GOOS}_${GOARCH}" in
        linux_amd64)  echo "onnxruntime-linux-x64-${ORT_VER}.tgz" ;;
        linux_arm64)  echo "onnxruntime-linux-aarch64-${ORT_VER}.tgz" ;;
        linux_arm)    echo "onnxruntime-linux-arm-${ORT_VER}.tgz" ;;
        darwin_amd64) echo "onnxruntime-osx-x86_64-${ORT_VER}.tgz" ;;
        darwin_arm64) echo "onnxruntime-osx-arm64-${ORT_VER}.tgz" ;;
        windows_*) echo "WINDOWS"; return 0 ;;
        *) echo ""; return 1 ;;
    esac
}

# Only download the runtime if no library file is present yet.
if ! ls "$MODEL_DIR"/libonnxruntime* "$MODEL_DIR"/onnxruntime.dll 2>/dev/null | grep -q .; then
    ORT_PKG=$(ort_lib_name) || { echo "Unsupported platform ${GOOS}/${GOARCH}; download ONNX Runtime manually."; }
    if [ "$ORT_PKG" = "WINDOWS" ]; then
        echo "Windows detected — run setup.ps1 instead (PowerShell):"
        echo "  powershell -ExecutionPolicy Bypass -File setup.ps1"
        exit 0
    fi
    if [ -n "$ORT_PKG" ]; then
        ORT_URL="https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VER}/${ORT_PKG}"
        echo "Downloading ONNX Runtime ${ORT_VER} for ${GOOS}/${GOARCH}..."
        curl -fsSL -o /tmp/_ort.tgz "$ORT_URL"
        tar -xzf /tmp/_ort.tgz -C /tmp
        LIB=$(find /tmp/onnxruntime-* \( -name "libonnxruntime.so*" -o -name "libonnxruntime.dylib" \) | head -1)
        if [ -n "$LIB" ]; then
            cp "$LIB" "$MODEL_DIR/"
            echo "  → $MODEL_DIR/$(basename "$LIB")"
        fi
        rm -rf /tmp/_ort.tgz /tmp/onnxruntime-*
    fi
else
    echo "ONNX Runtime library already present in $MODEL_DIR"
fi

# ---- YOLOv5n (object detection → photo tags) ----------------------------
YOLO_PATH="$MODEL_DIR/yolov5n.onnx"
if [ ! -f "$YOLO_PATH" ]; then
    echo "Downloading YOLOv5n ONNX..."
    # Official ultralytics assets release (input: images [1,3,640,640], output: output0 [1,25200,85])
    curl -fsSL -o "$YOLO_PATH" \
        "https://github.com/ultralytics/assets/releases/download/v0.0.0/yolov5nu.onnx"
    echo "  → $YOLO_PATH"
else
    echo "YOLOv5n already present"
fi

# ---- ultraface-slim-320 (face detection) --------------------------------
FACE_DET_PATH="$MODEL_DIR/face_detect.onnx"
if [ ! -f "$FACE_DET_PATH" ]; then
    echo "Downloading ultraface-slim-320 ONNX..."
    # ONNX Model Zoo: input=input [1,3,240,320], outputs=scores [1,4420,2] + boxes [1,4420,4]
    curl -fsSL -o "$FACE_DET_PATH" \
        "https://github.com/onnx/models/raw/main/validated/vision/body_analysis/ultraface/models/version-slim-320.onnx"
    echo "  → $FACE_DET_PATH"
else
    echo "ultraface already present"
fi

# ---- MobileFaceNet / w600k_mbf (face embedding) -------------------------
FACE_EMB_PATH="$MODEL_DIR/face_embed.onnx"
if [ ! -f "$FACE_EMB_PATH" ]; then
    echo "Downloading MobileFaceNet ONNX (InsightFace buffalo_sc)..."
    # InsightFace buffalo_sc pack: input=input.1 [1,3,112,112], output=fc1 [1,512]
    curl -fsSL -o /tmp/_buffalo_sc.zip \
        "https://github.com/deepinsight/insightface/releases/download/v0.7/buffalo_sc.zip"
    # The zip contains w600k_mbf.onnx — extract just that file
    unzip -p /tmp/_buffalo_sc.zip "*/w600k_mbf.onnx" > "$FACE_EMB_PATH" 2>/dev/null || \
        unzip -p /tmp/_buffalo_sc.zip "w600k_mbf.onnx" > "$FACE_EMB_PATH"
    rm -f /tmp/_buffalo_sc.zip
    echo "  → $FACE_EMB_PATH"
else
    echo "MobileFaceNet already present"
fi

echo ""
echo "Setup complete. Models in $MODEL_DIR"
echo ""
echo "Build:  sh build.sh"
echo "Run:    ./photoai_<os>_<arch> -models $MODEL_DIR"
