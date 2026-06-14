@echo off
:: build.bat - Build the photoai subservice binary for Windows
::
:: Requirements:
::   - Go  https://go.dev/dl/
::   - A C compiler for CGo. Choose one of:
::       Scoop (no admin):  irm get.scoop.sh | iex  &&  scoop install gcc
::       TDM-GCC installer: https://jmeubank.github.io/tdm-gcc/
::       w64devkit (zip):   https://github.com/skeeto/w64devkit/releases
::     After installing, open a NEW terminal so gcc.exe is on your PATH.
::
::   On ARM64 Windows: install the x64 edition of Go (not ARM64 Go) so it
::   matches the x64 TDM-GCC / Scoop GCC toolchain. Windows ARM64 can run
::   x64 binaries transparently via built-in emulation.
::
:: Usage (from subservice\photoai):
::   build.bat
::
setlocal EnableDelayedExpansion

set GOOS=windows
set CGO_ENABLED=1

where go >nul 2>&1
if %errorlevel% neq 0 (
    echo ERROR: go not found on PATH.
    echo Install Go from https://go.dev/dl/ and re-open this terminal.
    exit /b 1
)

where gcc >nul 2>&1
if %errorlevel% neq 0 (
    echo ERROR: gcc not found on PATH ^(required for CGo^).
    echo Install a C compiler, then re-open this terminal:
    echo   Scoop ^(no admin^):  irm get.scoop.sh ^| iex  ^&^&  scoop install gcc
    echo   TDM-GCC installer:  https://jmeubank.github.io/tdm-gcc/
    echo   w64devkit ^(zip^):    https://github.com/skeeto/w64devkit/releases
    exit /b 1
)

:: Derive GOARCH from the GCC target triple so the Go target always matches the
:: C toolchain.  This avoids a broken binary when gcc is x64 but GOARCH would
:: otherwise be set to arm64 (e.g. on ARM64 Windows with x64 TDM-GCC).
set GOARCH=
for /f "delims=" %%a in ('gcc -dumpmachine 2^>nul') do set GCC_MACHINE=%%a
if "!GCC_MACHINE:x86_64=!" neq "!GCC_MACHINE!" set GOARCH=amd64
if "!GCC_MACHINE:aarch64=!" neq "!GCC_MACHINE!" set GOARCH=arm64
if "!GCC_MACHINE:i686=!"   neq "!GCC_MACHINE!" set GOARCH=386
if "%GOARCH%"=="" (
    :: Fallback: use whatever Go's native GOARCH is
    for /f "delims=" %%a in ('go env GOARCH') do set GOARCH=%%a
    echo WARN: could not detect GCC machine type; using go env GOARCH=%GOARCH%
)

echo Building photoai for %GOOS%/%GOARCH% ^(gcc: %GCC_MACHINE%^) -^> photoai.exe
go build -o photoai.exe .
if %errorlevel% neq 0 (
    echo.
    echo Build failed. See errors above.
    exit /b 1
)

echo Done: %~dp0photoai.exe
echo.
echo Next step - download models if you have not already:
echo   setup.bat
echo.
echo Then run:
echo   photoai.exe -models "%~dp0models"
