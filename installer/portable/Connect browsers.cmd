@echo off
rem Tells Chrome, Edge and Firefox where this copy's browser relay is, for
rem this Windows user only. Run it again after moving the folder.
setlocal
set "HERE=%~dp0"
set "HOST=com.xoguyfawksxo.internet_downloader"
if not exist "%HERE%nmhost.exe" (
    echo nmhost.exe isn't next to this file, so the browsers can't be connected.
    pause
    exit /b 1
)
reg add "HKCU\Software\Google\Chrome\NativeMessagingHosts\%HOST%" /ve /t REG_SZ /d "%HERE%chromium-host.json" /f >nul || goto failed
reg add "HKCU\Software\Microsoft\Edge\NativeMessagingHosts\%HOST%" /ve /t REG_SZ /d "%HERE%chromium-host.json" /f >nul || goto failed
reg add "HKCU\Software\Mozilla\NativeMessagingHosts\%HOST%" /ve /t REG_SZ /d "%HERE%firefox-host.json" /f >nul || goto failed
echo Chrome, Edge and Firefox now use Galactic Downloader from:
echo %HERE%
echo Restart the browser if the extension still can't reach the app.
pause
exit /b 0
:failed
echo Couldn't connect the browsers.
pause
exit /b 1
