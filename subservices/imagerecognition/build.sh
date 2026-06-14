#!/bin/sh
# Build the Image Recognition subservice and (optionally) deploy it into an
# ArozOS tree's ./subservice/ folder.
#
#   sh build.sh                 build builtin (pure-Go) binary for this host
#   ONNX=1 sh build.sh          build with the ONNX/YOLO backend (-tags onnx)
#   DEPLOY=/path/to/arozos/src sh build.sh   also copy into <DEPLOY>/subservice/
#
# Cross-compile by exporting GOOS / GOARCH before running.
set -e

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$DIR"

GOOS_VAL=$(go env GOOS)
GOARCH_VAL=$(go env GOARCH)
if [ "$GOOS_VAL" = "windows" ]; then
    OUT="imagerecognition.exe"
else
    OUT="imagerecognition_${GOOS_VAL}_${GOARCH_VAL}"
fi

TAGS=""
if [ "${ONNX:-0}" = "1" ]; then
    TAGS="-tags onnx"
    echo "Building with ONNX/YOLO backend (set ONNXRUNTIME_LIB when running)."
fi

echo "Building $OUT ..."
# shellcheck disable=SC2086
go build $TAGS -o "$OUT" .
echo "Built $DIR/$OUT"

if [ -n "${DEPLOY:-}" ]; then
    DEST="$DEPLOY/subservice/imagerecognition"
    mkdir -p "$DEST"
    cp "$OUT" "$DEST/"
    cp -r web "$DEST/" 2>/dev/null || true
    [ -d models ] && cp -r models "$DEST/" 2>/dev/null || true
    echo "Deployed to $DEST"
fi
