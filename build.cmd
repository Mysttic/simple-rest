@echo off
rem Cross-compiles simple-rest into dist\ as one static file per system
rem (Windows host), named <project>-<version>-<platform>[.exe], plus a sha256
rem checksums file in sha256sum format. Same binaries as build.sh; the .deb
rem packages need dpkg-deb, so only build.sh on Linux makes them.
setlocal
cd /d "%~dp0"
set PROJECT=simple-rest
set /p VERSION=<VERSION.md
set VERSION=%VERSION:v=%
if exist dist rmdir /s /q dist
mkdir dist
set CGO_ENABLED=0
echo building %PROJECT% %VERSION%:
call :build windows amd64 windows-amd64.exe
call :build linux   amd64 linux-amd64
call :build linux   arm64 linux-arm64
set GOARM=6
call :build linux   arm   linux-armhf
set GOARM=
powershell -NoProfile -Command "Get-ChildItem 'dist\%PROJECT%-*' | Where-Object { $_.Name -notlike '*-checksums.txt' } | ForEach-Object { (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower() + '  ' + $_.Name } | Set-Content -Encoding ascii 'dist\%PROJECT%-%VERSION%-checksums.txt'"
echo   checksums -^> dist\%PROJECT%-%VERSION%-checksums.txt
dir dist
exit /b 0

:build
set GOOS=%1
set GOARCH=%2
echo   %1/%2 -^> dist\%PROJECT%-%VERSION%-%3
go build -trimpath -ldflags "-s -w" -o "dist\%PROJECT%-%VERSION%-%3" .
exit /b 0
