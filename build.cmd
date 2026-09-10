@echo off
rem Cross-compiles simple-rest into dist\ as one static file per platform (Windows host).
setlocal
cd /d "%~dp0"
if not exist dist mkdir dist
set CGO_ENABLED=0
call :build windows amd64 simple-rest-windows-amd64.exe
call :build windows arm64 simple-rest-windows-arm64.exe
call :build linux   amd64 simple-rest-linux-amd64
call :build linux   arm64 simple-rest-linux-arm64
set GOARM=6
call :build linux   arm   simple-rest-linux-armv6
set GOARM=
call :build darwin  arm64 simple-rest-macos-arm64
call :build darwin  amd64 simple-rest-macos-amd64
call :build android arm64 simple-rest-android-arm64
echo done
exit /b 0

:build
set GOOS=%1
set GOARCH=%2
echo   %1/%2 -^> dist\%3
go build -trimpath -ldflags "-s -w" -o "dist\%3" .
exit /b 0
