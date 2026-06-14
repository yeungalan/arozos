#!/bin/sh
#
# build.sh — Build and install the photoai subservice binary for the current OS/arch.
#
# Usage (from the repository root or this directory):
#   cd subservice/photoai && sh build.sh
#
# The resulting binary is placed in this directory with the name required by
# the ArozOS subservice loader: photoai_<goos>_<goarch>  (e.g. photoai_linux_amd64).
# On Windows the binary is named photoai.exe.
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
go build -o "$OUT" .
echo "Done: $(pwd)/${OUT}"
echo ""
echo "To install, ensure this directory ($(pwd)) exists under <arozos-binary>/subservice/photoai/"
echo "then restart ArozOS. The Photo AI module will appear automatically in the module list."
