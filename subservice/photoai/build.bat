@echo off
:: build.bat - Build the photoai subservice binary for Windows
::
:: Requirements:
::   - Go  https://go.dev/dl/
::   - A C compiler for CGo.  Install MinGW-w64 via winget:
::       winget install mingw
::     or download from https://www.mingw-w64.org/
::     After installing, ensure gcc.exe is on your PATH.
::
:: Usage (from subservice\photoai):
::   build.bat
::
setlocal EnableDelayedExpansion

:: Detect architecture from the running process
set GOARCH=amd64
if "%PROCESSOR_ARCHITECTURE%"=="ARM64" set GOARCH=arm64

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
    echo Install MinGW-w64:
    echo   winget install mingw
    echo Then re-open this terminal so PATH is updated.
    exit /b 1
)

echo Building photoai for %GOOS%/%GOARCH% -^> photoai.exe
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
