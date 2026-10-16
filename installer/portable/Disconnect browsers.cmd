@echo off
rem Undoes "Connect browsers.cmd", only where it points at this folder, so an
rem installed copy that was connected later stays connected.
setlocal
set "HERE=%~dp0"
set "HOST=com.xoguyfawksxo.internet_downloader"
call :drop "HKCU\Software\Google\Chrome\NativeMessagingHosts\%HOST%" chromium-host.json
call :drop "HKCU\Software\Microsoft\Edge\NativeMessagingHosts\%HOST%" chromium-host.json
call :drop "HKCU\Software\Mozilla\NativeMessagingHosts\%HOST%" firefox-host.json
echo The browsers no longer use this copy of Galactic Downloader.
pause
exit /b 0
:drop
for /f "tokens=2,*" %%a in ('reg query "%~1" /ve 2^>nul ^| findstr /i "REG_SZ"') do (
    if /i "%%b"=="%HERE%%~2" reg delete "%~1" /f >nul
)
exit /b 0
