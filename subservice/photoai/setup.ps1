# setup.ps1 — Download ONNX Runtime and model files needed by photoai (Windows)
#
# Usage (from subservice/photoai):
#   powershell -ExecutionPolicy Bypass -File setup.ps1
#
# Downloads go into .\models\ by default; override with -ModelDir <path>.
#
param(
    [string]$ModelDir = "$(Get-Location)\models"
)

$OrtVer = "1.17.3"

# Detect architecture
$Arch = "x64"
try {
    $procArch = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
    if ($procArch -eq [System.Runtime.InteropServices.Architecture]::Arm64) {
        $Arch = "arm64"
    }
} catch {}

New-Item -ItemType Directory -Force -Path $ModelDir | Out-Null
Write-Host "Models directory: $ModelDir"

# ---- ONNX Runtime DLL ---------------------------------------------------
$OrtDll = Join-Path $ModelDir "onnxruntime.dll"
if (Test-Path $OrtDll) {
    Write-Host "ONNX Runtime DLL already present"
} else {
    $OrtPkg = "onnxruntime-win-$Arch-$OrtVer.zip"
    $OrtUrl  = "https://github.com/microsoft/onnxruntime/releases/download/v$OrtVer/$OrtPkg"
    $TmpZip  = Join-Path $env:TEMP "ort.zip"
    $TmpDir  = Join-Path $env:TEMP "ort_extract"
    Write-Host "Downloading ONNX Runtime $OrtVer for Windows/$Arch..."
    Invoke-WebRequest -Uri $OrtUrl -OutFile $TmpZip -UseBasicParsing
    Expand-Archive -Path $TmpZip -DestinationPath $TmpDir -Force
    $dll = Get-ChildItem -Path $TmpDir -Filter "onnxruntime.dll" -Recurse | Select-Object -First 1
    if (-not $dll) { Write-Error "onnxruntime.dll not found in archive"; exit 1 }
    Copy-Item $dll.FullName $OrtDll
    Remove-Item $TmpZip, $TmpDir -Recurse -Force
    Write-Host "  -> $OrtDll"
}

# ---- YOLOv5n (object detection -> photo tags) ---------------------------
$YoloPath = Join-Path $ModelDir "yolov5n.onnx"
if (Test-Path $YoloPath) {
    Write-Host "YOLOv5n already present"
} else {
    Write-Host "Downloading YOLOv5n ONNX..."
    Invoke-WebRequest `
        -Uri "https://github.com/ultralytics/assets/releases/download/v0.0.0/yolov5nu.onnx" `
        -OutFile $YoloPath -UseBasicParsing
    Write-Host "  -> $YoloPath"
}

# ---- ultraface-slim-320 (face detection) --------------------------------
$FaceDetPath = Join-Path $ModelDir "face_detect.onnx"
if (Test-Path $FaceDetPath) {
    Write-Host "ultraface already present"
} else {
    Write-Host "Downloading ultraface-slim-320 ONNX..."
    Invoke-WebRequest `
        -Uri "https://github.com/onnx/models/raw/main/validated/vision/body_analysis/ultraface/models/version-slim-320.onnx" `
        -OutFile $FaceDetPath -UseBasicParsing
    Write-Host "  -> $FaceDetPath"
}

# ---- MobileFaceNet / w600k_mbf (face embedding) -------------------------
$FaceEmbPath = Join-Path $ModelDir "face_embed.onnx"
if (Test-Path $FaceEmbPath) {
    Write-Host "MobileFaceNet already present"
} else {
    Write-Host "Downloading MobileFaceNet ONNX (InsightFace buffalo_sc)..."
    $TmpZip = Join-Path $env:TEMP "buffalo_sc.zip"
    $TmpDir = Join-Path $env:TEMP "buffalo_extract"
    Invoke-WebRequest `
        -Uri "https://github.com/deepinsight/insightface/releases/download/v0.7/buffalo_sc.zip" `
        -OutFile $TmpZip -UseBasicParsing
    Expand-Archive -Path $TmpZip -DestinationPath $TmpDir -Force
    $mfn = Get-ChildItem -Path $TmpDir -Filter "w600k_mbf.onnx" -Recurse | Select-Object -First 1
    if (-not $mfn) { Write-Error "w600k_mbf.onnx not found in buffalo_sc.zip"; exit 1 }
    Copy-Item $mfn.FullName $FaceEmbPath
    Remove-Item $TmpZip, $TmpDir -Recurse -Force
    Write-Host "  -> $FaceEmbPath"
}

Write-Host ""
Write-Host "Setup complete. Models in $ModelDir"
Write-Host ""
Write-Host "Build (requires MinGW-w64 for CGo):"
Write-Host "  set CGO_ENABLED=1"
Write-Host "  go build -o photoai.exe ."
Write-Host ""
Write-Host "Run:"
Write-Host "  .\photoai.exe -models `"$ModelDir`""
