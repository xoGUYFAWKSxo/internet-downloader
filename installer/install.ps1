<#
.SYNOPSIS
Builds Galactic Downloader and installs it for the current user: the programs
go to %LOCALAPPDATA%\Programs\Galactic Downloader, with Start Menu and desktop
shortcuts, and the browser relay is registered from there. No admin rights
needed. Opening the app (or downloading from the browser) starts the engine.

.EXAMPLE
.\install.ps1
.\install.ps1 -NoBuild      # install what is already built
.\install.ps1 -Uninstall
#>
param(
    # An install from before the rename to Galactic Downloader stays where it is,
    # as the installer does.
    [string]$InstallDir = $(
        $old = Join-Path $env:LOCALAPPDATA 'Programs\Internet Downloader'
        if (Test-Path (Join-Path $old 'engine.exe')) { $old } else { Join-Path $env:LOCALAPPDATA 'Programs\Galactic Downloader' }
    ),
    [switch]$NoBuild,
    [switch]$NoDesktopShortcut,
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'
# The saved PATH, so tools installed since this shell opened (Go, FFmpeg) are found.
$env:PATH = @($env:PATH, [Environment]::GetEnvironmentVariable('Path', 'User'), [Environment]::GetEnvironmentVariable('Path', 'Machine')) -join ';'
$Root = Split-Path $PSScriptRoot
$Name = 'Galactic Downloader'
$Shortcuts = @(
    (Join-Path ([Environment]::GetFolderPath('Programs')) "$Name.lnk"),
    (Join-Path ([Environment]::GetFolderPath('Desktop')) "$Name.lnk")
)
# Shortcuts made before the rename, removed on install and uninstall.
$OldShortcuts = @(
    (Join-Path ([Environment]::GetFolderPath('Programs')) 'Internet Downloader.lnk'),
    (Join-Path ([Environment]::GetFolderPath('Desktop')) 'Internet Downloader.lnk')
)
foreach ($s in $OldShortcuts) { if (Test-Path $s) { Remove-Item $s } }

# Stops engines started from the install folder or the repo's bin, so their
# files can be replaced. Downloads in progress stop with them.
function Stop-Engine {
    $dirs = @($InstallDir, (Join-Path $Root 'bin')) | ForEach-Object { [IO.Path]::GetFullPath($_).TrimEnd('\') }
    Get-Process engine, ui -ErrorAction SilentlyContinue | Where-Object {
        $_.Path -and ($dirs -contains (Split-Path $_.Path))
    } | ForEach-Object {
        Write-Host "Stopping $($_.Name) ($($_.Path))"
        $_ | Stop-Process -Force
        $_.WaitForExit(5000) | Out-Null
    }
}

if ($Uninstall) {
    Stop-Engine
    foreach ($s in $Shortcuts) { if (Test-Path $s) { Remove-Item $s } }
    & (Join-Path $PSScriptRoot 'register-native-host.ps1') -Unregister
    if (Test-Path $InstallDir) { Remove-Item $InstallDir -Recurse }
    Write-Host "$Name removed. Your downloads are untouched."
    return
}

if (-not $NoBuild) {
    $goBin = Join-Path $env:ProgramFiles 'Go\bin'
    if (-not (Get-Command go -ErrorAction SilentlyContinue) -and (Test-Path $goBin)) { $env:PATH = "$goBin;$env:PATH" }
    Push-Location $Root
    try {
        go build -o bin/ ./cmd/...
        if ($LASTEXITCODE) { throw 'go build failed' }
        node extension/build.mjs
        if ($LASTEXITCODE) { throw 'extension build failed' }
        $wails = Join-Path $env:USERPROFILE 'go\bin\wails.exe'
        if (-not (Test-Path $wails)) { $wails = 'wails' }
        Push-Location ui
        try {
            & $wails build -clean
            if ($LASTEXITCODE) { throw 'wails build failed' }
        } finally { Pop-Location }
    } finally { Pop-Location }
}

$files = @{
    'engine.exe' = Join-Path $Root 'bin\engine.exe'
    'nmhost.exe' = Join-Path $Root 'bin\nmhost.exe'
    'ui.exe'     = Join-Path $Root 'ui\build\bin\ui.exe'
}
foreach ($f in $files.Values) { if (-not (Test-Path $f)) { throw "Missing $f. Run without -NoBuild." } }

Stop-Engine
New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
foreach ($k in $files.Keys) { Copy-Item $files[$k] (Join-Path $InstallDir $k) -Force }

# The engine looks for FFmpeg next to itself first, so a copy there works
# even when the browser that starts the engine has an older PATH.
$ffmpeg = Get-Command ffmpeg.exe -ErrorAction SilentlyContinue
if ($ffmpeg) {
    Copy-Item $ffmpeg.Source (Join-Path $InstallDir 'ffmpeg.exe') -Force
} else {
    Write-Warning 'FFmpeg not found on PATH: streams with separate sound and picture will not be joined.'
}

# A fresh icon file each time, as the installer does: Windows keeps its own
# copy of each icon by file name and misses a new picture in ui.exe.
Remove-Item (Join-Path $InstallDir 'GalacticDownloader*.ico') -ErrorAction SilentlyContinue
$icon = Join-Path $InstallDir ('GalacticDownloader-{0}.ico' -f (Get-Date -Format 'yyyyMMddHHmmss'))
Copy-Item (Join-Path $PSScriptRoot '..\ui\build\windows\icon.ico') $icon

$shell = New-Object -ComObject WScript.Shell
$targets = if ($NoDesktopShortcut) { @($Shortcuts[0]) } else { $Shortcuts }
foreach ($s in $targets) {
    $lnk = $shell.CreateShortcut($s)
    $lnk.TargetPath = Join-Path $InstallDir 'ui.exe'
    $lnk.WorkingDirectory = $InstallDir
    $lnk.Description = $Name
    $lnk.IconLocation = "$icon,0"
    $lnk.Save()
}

& (Join-Path $PSScriptRoot 'register-native-host.ps1') -HostPath (Join-Path $InstallDir 'nmhost.exe')
Write-Host "$Name installed in $InstallDir. Open it from the Start Menu or the desktop."
