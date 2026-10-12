<#
.SYNOPSIS
Registers (or removes) the Native Messaging host for Chrome, Edge and Firefox
for the current user. No admin rights needed; everything goes under HKCU and
%LOCALAPPDATA%.

.EXAMPLE
.\register-native-host.ps1
.\register-native-host.ps1 -Browsers Chrome,Firefox
.\register-native-host.ps1 -Unregister
#>
param(
    [string]$HostPath = (Join-Path $PSScriptRoot '..\bin\nmhost.exe'),
    # Fixed by the "key" in extension/manifest.base.json; Chrome and Edge share it.
    [string]$ChromeExtensionId = 'akmjpphfkcdejdhhobbbnbddgdbajpem',
    [string]$FirefoxExtensionId = 'internet-downloader@xoguyfawksxo',
    [ValidateSet('Chrome', 'Edge', 'Firefox')]
    [string[]]$Browsers = @('Chrome', 'Edge', 'Firefox'),
    [switch]$Unregister
)

$ErrorActionPreference = 'Stop'
$HostName = 'com.xoguyfawksxo.internet_downloader'
$Description = 'Internet Downloader relay'
$ManifestDir = Join-Path $env:LOCALAPPDATA 'InternetDownloader\native-host'

$RegRoots = @{
    Chrome  = 'HKCU:\Software\Google\Chrome\NativeMessagingHosts'
    Edge    = 'HKCU:\Software\Microsoft\Edge\NativeMessagingHosts'
    Firefox = 'HKCU:\Software\Mozilla\NativeMessagingHosts'
}

function Write-JsonNoBom([string]$Path, $Object) {
    $json = $Object | ConvertTo-Json -Depth 4
    [IO.File]::WriteAllText($Path, $json, (New-Object Text.UTF8Encoding $false))
}

function Set-HostKey([string]$Browser, [string]$Manifest) {
    $key = Join-Path $RegRoots[$Browser] $HostName
    if (-not (Test-Path $key)) { New-Item -Path $key -Force | Out-Null }
    Set-Item -Path $key -Value $Manifest
    Write-Host "$Browser -> $Manifest"
}

if ($Unregister) {
    foreach ($b in $Browsers) {
        $key = Join-Path $RegRoots[$b] $HostName
        if (Test-Path $key) { Remove-Item -Path $key -Recurse; Write-Host "$b unregistered" }
        else { Write-Host "$b was not registered" }
    }
    $remaining = $RegRoots.Keys | Where-Object { Test-Path (Join-Path $RegRoots[$_] $HostName) }
    if (-not $remaining -and (Test-Path $ManifestDir)) {
        Remove-Item -Path $ManifestDir -Recurse
        $parent = Split-Path $ManifestDir
        if (-not (Get-ChildItem -Path $parent -Force)) { Remove-Item -Path $parent }
    }
    return
}

$resolved = Resolve-Path -LiteralPath $HostPath -ErrorAction SilentlyContinue
if (-not $resolved) { throw "nmhost.exe not found at '$HostPath'. Build it first: go build -o bin/ ./cmd/..." }
$exe = $resolved.Path
New-Item -ItemType Directory -Path $ManifestDir -Force | Out-Null

$chromium = @($Browsers | Where-Object { $_ -ne 'Firefox' })
if ($chromium.Count -gt 0) {
    if (-not $ChromeExtensionId) {
        Write-Warning "Skipping $($chromium -join ', '): pass -ChromeExtensionId (shown on chrome://extensions)."
    } else {
        $m = Join-Path $ManifestDir 'chromium-host.json'
        Write-JsonNoBom $m ([ordered]@{
            name            = $HostName
            description     = $Description
            path            = $exe
            type            = 'stdio'
            allowed_origins = @("chrome-extension://$ChromeExtensionId/")
        })
        foreach ($b in $chromium) { Set-HostKey $b $m }
    }
}

if ($Browsers -contains 'Firefox') {
    # Firefox uses allowed_extensions (add-on IDs) instead of allowed_origins.
    $m = Join-Path $ManifestDir 'firefox-host.json'
    Write-JsonNoBom $m ([ordered]@{
        name               = $HostName
        description        = $Description
        path               = $exe
        type               = 'stdio'
        allowed_extensions = @($FirefoxExtensionId)
    })
    Set-HostKey 'Firefox' $m
}
