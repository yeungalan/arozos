#!/bin/sh
#
# build.sh — Build the photoai subservice binary.
#
# Usage:
#   cd subservice/photoai && sh build.sh
#
# CGo is required (ONNX Runtime bindings). The runtime shared library is
# loaded at runtime via SetSharedLibraryPath; only CGo itself is needed at
# build time — the ONNX header files are bundled inside onnxruntime_go.
#
# If the ONNX Runtime library is installed system-wide (e.g. from your distro
# package manager), no extra flags are needed. If you placed the library in
# ./models/, set CGO_LDFLAGS before calling this script:
#
#   export CGO_LDFLAGS="-L$(pwd)/models -Wl,-rpath,$(pwd)/models"
#   sh build.sh
#
set -eu

GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"

if [ "$GOOS" = "windows" ]; then
    OUT="photoai.exe"
else
    OUT="photoai_${GOOS}_${GOARCH}"
fi

echo "Building photoai for ${GOOS}/${GOARCH} → ${OUT}"
CGO_ENABLED=1 go build -o "$OUT" .
echo "Done: $(pwd)/${OUT}"
echo ""
echo "Run setup.sh first to download ONNX Runtime and model files into ./models/"
echo "Then start with: ./${OUT} -models $(pwd)/models"
