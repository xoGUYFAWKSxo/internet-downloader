; The release installer, built by Inno Setup 6. Run build-release.ps1, which
; fills the stage folder and passes the version and paths.
;
; It installs for the current user only, with no admin rights, into the same
; folder as install.ps1, so it upgrades a development install in place.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef Stage
  #define Stage "..\dist\release\stage"
#endif
#ifndef OutDir
  #define OutDir "..\dist\release"
#endif
#define AppName "Internet Downloader"
#define HostName "com.xoguyfawksxo.internet_downloader"
#define Repo "https://github.com/xoGUYFAWKSxo/internet-downloader"

[Setup]
; Never change AppId: it is how Windows tells an upgrade from a second copy.
AppId={{0ED760B9-9B76-4BE3-80F4-179EC2F98659}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher=xoGUYFAWKSxo
AppPublisherURL={#Repo}
AppSupportURL={#Repo}/issues
AppUpdatesURL={#Repo}/releases
VersionInfoVersion={#AppVersion}
DefaultDirName={localappdata}\Programs\{#AppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
LicenseFile={#Stage}\LICENSE.txt
OutputDir={#OutDir}
OutputBaseFilename=InternetDownloader-{#AppVersion}-setup
SetupIconFile=..\ui\build\windows\icon.ico
UninstallDisplayIcon={app}\ui.exe
UninstallDisplayName={#AppName}
WizardStyle=modern
Compression=lzma2/max
SolidCompression=yes
; The engine and window hold their files open; an upgrade closes them.
; Downloads in progress are left paused, to resume afterwards.
CloseApplications=force
RestartApplications=no

[Tasks]
Name: desktopicon; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"

[Files]
Source: "{#Stage}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs

[Icons]
Name: "{autoprograms}\{#AppName}"; Filename: "{app}\ui.exe"; WorkingDir: "{app}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\ui.exe"; WorkingDir: "{app}"; Tasks: desktopicon

[Registry]
; The browser relay, for Chrome, Edge and Firefox. The manifests name
; nmhost.exe beside them.
Root: HKCU; Subkey: "Software\Google\Chrome\NativeMessagingHosts\{#HostName}"; ValueType: string; ValueName: ""; ValueData: "{app}\chromium-host.json"; Flags: uninsdeletekey
Root: HKCU; Subkey: "Software\Microsoft\Edge\NativeMessagingHosts\{#HostName}"; ValueType: string; ValueName: ""; ValueData: "{app}\chromium-host.json"; Flags: uninsdeletekey
Root: HKCU; Subkey: "Software\Mozilla\NativeMessagingHosts\{#HostName}"; ValueType: string; ValueName: ""; ValueData: "{app}\firefox-host.json"; Flags: uninsdeletekey

[Run]
Filename: "{app}\ui.exe"; Description: "{cm:LaunchProgram,{#AppName}}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; Stops the engine and windows started from this folder, and nothing else.
Filename: "powershell.exe"; Parameters: "-NoProfile -NonInteractive -Command ""Get-Process engine, ui -ErrorAction SilentlyContinue | Where-Object {{ $_.Path -and (Split-Path $_.Path) -eq '{app}' } | Stop-Process -Force; Start-Sleep -Milliseconds 500"""; Flags: runhidden; RunOnceId: "StopEngine"

[UninstallDelete]
; Also when install.ps1 made the folder, so Inno Setup didn't.
Type: dirifempty; Name: "{app}"

; The download list and settings in %LOCALAPPDATA%\InternetDownloader, and
; the downloads themselves, are kept.
