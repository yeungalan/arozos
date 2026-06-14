@echo off
:: setup.bat - Download ONNX Runtime and model files for photoai (Windows)
::
:: Just double-click this file, or run from a Command Prompt:
::   setup.bat
::
:: This launches setup.ps1 with the execution-policy bypass so you do not
:: have to change any PowerShell system settings.
powershell -ExecutionPolicy Bypass -File "%~dp0setup.ps1" %*
