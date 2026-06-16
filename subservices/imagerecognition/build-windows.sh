#!/bin/sh
# Cross-compile the Windows (amd64) build of the Image Recognition subservice
# from Linux/macOS. The ONNX/YOLO build uses CGO, so it needs the mingw-w64
# cross C compiler:
#
#   Debian/Ubuntu:  sudo apt-get install gcc-mingw-w64-x86-64
#   macOS (brew):   brew install mingw-w64
#
# Usage:
#   sh build-windows.sh                 # ONNX build  -> imagerecognition.exe
#   PUREGO=1 sh build-windows.sh        # pure-Go build (no models, weaker faces)
#
# The ONNX Runtime DLL for Windows is bundled under
# models/onnxruntime-win-x64-*/lib/onnxruntime.dll and is located automatically
# at runtime, so no environment variable is needed on the target machine.
set -e

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$DIR"

CC=${CC:-x86_64-w64-mingw32-gcc}
OUT="imagerecognition.exe"

if [ "${PUREGO:-0}" = "1" ]; then
    echo "Building pure-Go $OUT (builtin face path, no ONNX models needed)..."
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$OUT" .
else
    if ! command -v "$CC" >/dev/null 2>&1; then
        echo "error: $CC not found. Install mingw-w64 (see header), or use PUREGO=1." >&2
        exit 1
    fi
    echo "Building ONNX/YOLO $OUT with $CC ..."
    CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="$CC" go build -tags onnx -o "$OUT" .
fi
echo "Built $DIR/$OUT"
echo "Deploy: copy $OUT plus the models/ and web/ folders into"
echo "        <arozos>/src/subservice/imagerecognition/ and (re)start ArozOS."
