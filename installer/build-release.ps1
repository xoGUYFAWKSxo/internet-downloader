<#
.SYNOPSIS
Builds a release into dist\release: InternetDownloader-<version>-setup.exe
(Inno Setup 6), the extension zipped for each browser store, and their
SHA-256 sums. The programs carry the version; FFmpeg is a pinned build,
checked against its SHA-256 before it goes in.

.EXAMPLE
.\build-release.ps1 -Version 0.1.0
#>
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^\d+\.\d+\.\d+$')]
    [string]$Version,
    [string]$Iscc
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest is very slow with it
$Root = Split-Path $PSScriptRoot
$Out = Join-Path $Root 'dist\release'
$Stage = Join-Path $Out 'stage'
$Cache = Join-Path $Root 'dist\cache'

# Gyan Doshi's "essentials" build (GPLv3, like this app). The engine only
# runs it to copy tracks into one file, never to re-encode.
$FFmpeg = @{
    Version = '9.0.2'
    Url     = 'https://github.com/GyanD/codexffmpeg/releases/download/9.0.2/ffmpeg-9.0.2-essentials_build.zip'
    Sha256  = '60F467265B1E312373DBCD92200C2618A74850F98D3D078E94296BB3FA2047BA'
}

function Invoke-Native([string]$What, [scriptblock]$Run) {
    & $Run
    if ($LASTEXITCODE) { throw "$What failed (exit code $LASTEXITCODE)" }
}

function Write-Text([string]$Path, [string]$Text) {
    [IO.File]::WriteAllText($Path, $Text, (New-Object Text.UTF8Encoding $false))
}

$goBin = Join-Path $env:ProgramFiles 'Go\bin'
if (-not (Get-Command go -ErrorAction SilentlyContinue) -and (Test-Path $goBin)) { $env:PATH = "$goBin;$env:PATH" }
$wails = Join-Path $env:USERPROFILE 'go\bin\wails.exe'
if (-not (Test-Path $wails)) { $wails = 'wails' }
if (-not $Iscc) {
    $Iscc = (Get-Command iscc.exe -ErrorAction SilentlyContinue).Source
    if (-not $Iscc) { $Iscc = Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6\ISCC.exe' }
}
if (-not (Test-Path $Iscc)) { throw "Inno Setup 6 not found ($Iscc). Install it, or pass -Iscc." }

if (Test-Path $Out) { Remove-Item $Out -Recurse -Force }
New-Item -ItemType Directory -Path $Stage, $Cache -Force | Out-Null

Push-Location $Root
try {
    # The engine and the relay.
    $ld = "-s -w -X github.com/xoGUYFAWKSxo/internet-downloader/internal/version.Version=$Version"
    Invoke-Native 'go build' { go build -trimpath -ldflags $ld -o "$Stage\" ./cmd/engine ./cmd/nmhost }

    # The window, with the version in its file properties.
    $wj = Join-Path $Root 'ui\wails.json'
    $orig = [IO.File]::ReadAllText($wj)
    try {
        Write-Text $wj ($orig -replace '"productVersion": "[^"]*"', "`"productVersion`": `"$Version`"")
        Push-Location ui
        try { Invoke-Native 'wails build' { & $wails build -clean -trimpath } } finally { Pop-Location }
    } finally { Write-Text $wj $orig }
    Copy-Item 'ui\build\bin\ui.exe' $Stage

    # The extension, zipped for the stores. tar writes the forward slashes
    # Firefox's store wants, which Compress-Archive doesn't.
    Invoke-Native 'extension build' { node extension/build.mjs "--version=$Version" }
    # Windows' own tar, not Git's, which reads C: as a remote host.
    $tar = Join-Path $env:SystemRoot 'System32\tar.exe'
    foreach ($t in 'chrome', 'firefox') {
        $zip = Join-Path $Out "InternetDownloader-$Version-$t-extension.zip"
        Invoke-Native "zipping the $t extension" { & $tar -a -c -f $zip -C "extension\dist\$t" * }
    }

    # FFmpeg, from the cache when it is already there and intact.
    $ffzip = Join-Path $Cache "ffmpeg-$($FFmpeg.Version)-essentials_build.zip"
    if (-not (Test-Path $ffzip) -or (Get-FileHash $ffzip -Algorithm SHA256).Hash -ne $FFmpeg.Sha256) {
        Write-Host "Downloading FFmpeg $($FFmpeg.Version)"
        Invoke-WebRequest -Uri $FFmpeg.Url -OutFile $ffzip -UseBasicParsing
        if ((Get-FileHash $ffzip -Algorithm SHA256).Hash -ne $FFmpeg.Sha256) {
            Remove-Item $ffzip
            throw "The FFmpeg download doesn't match its SHA-256; not using it."
        }
    }
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $z = [IO.Compression.ZipFile]::OpenRead($ffzip)
    try {
        $top = "ffmpeg-$($FFmpeg.Version)-essentials_build/"
        foreach ($f in @(@('bin/ffmpeg.exe', 'ffmpeg.exe'), @('LICENSE', 'FFmpeg-LICENSE.txt'), @('README.txt', 'FFmpeg-README.txt'))) {
            $e = $z.Entries | Where-Object { $_.FullName -eq $top + $f[0] }
            if (-not $e) { throw "$($f[0]) is missing from the FFmpeg zip" }
            [IO.Compression.ZipFileExtensions]::ExtractToFile($e, (Join-Path $Stage $f[1]), $true)
        }
    } finally { $z.Dispose() }
    Write-Text (Join-Path $Stage 'FFmpeg-SOURCE.txt') @"
ffmpeg.exe is FFmpeg $($FFmpeg.Version), the "essentials" build by Gyan Doshi:
$($FFmpeg.Url)

It is free software under the GNU GPL version 3 (FFmpeg-LICENSE.txt). Its
source code is at https://ffmpeg.org/releases/ffmpeg-$($FFmpeg.Version).tar.xz, and the
libraries built into it, with links to their source, are listed in
FFmpeg-README.txt.
"@

    # Licences: this app's, and those of the Go modules built into it.
    Copy-Item 'LICENSE' (Join-Path $Stage 'LICENSE.txt')
    $mods = [ordered]@{}
    foreach ($m in @(@('.', './cmd/engine', './cmd/nmhost'), @('ui', '.'))) {
        Push-Location $m[0]
        try {
            $lines = go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{with .Replace}}{{.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}' @($m[1..($m.Count - 1)])
            if ($LASTEXITCODE) { throw 'go list failed' }
        } finally { Pop-Location }
        foreach ($l in $lines | Where-Object { $_ } | Sort-Object -Unique) {
            $p, $v, $d = $l -split '\|', 3
            if ($p -like 'github.com/xoGUYFAWKSxo/internet-downloader*' -or $mods.Contains($p)) { continue }
            $mods[$p] = @($v, $d)
        }
    }
    $notice = New-Object Text.StringBuilder
    [void]$notice.AppendLine('Internet Downloader includes these Go modules, under their own licences.')
    foreach ($p in $mods.Keys) {
        $v, $d = $mods[$p]
        $lic = Get-ChildItem -LiteralPath $d -File | Where-Object { $_.Name -match '^(LICEN[CS]E|COPYING)' } | Sort-Object Name
        if (-not $lic) { throw "No licence file found for $p in $d" }
        [void]$notice.AppendLine().AppendLine(('=' * 78)).AppendLine("$p $v").AppendLine(('=' * 78))
        foreach ($f in $lic) { [void]$notice.AppendLine((Get-Content -LiteralPath $f.FullName -Raw).TrimEnd()) }
    }
    Write-Text (Join-Path $Stage 'THIRD-PARTY-NOTICES.txt') $notice.ToString()

    # The browser relay's manifests; setup.iss registers them.
    & (Join-Path $PSScriptRoot 'register-native-host.ps1') -WriteManifestsTo $Stage

    Invoke-Native 'Inno Setup' { & $Iscc /Q "/DAppVersion=$Version" "/DStage=$Stage" "/DOutDir=$Out" (Join-Path $PSScriptRoot 'setup.iss') }

    $sums = Get-ChildItem $Out -File | Sort-Object Name | ForEach-Object {
        "$((Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower())  $($_.Name)"
    }
    Write-Text (Join-Path $Out 'SHA256SUMS.txt') (($sums -join "`n") + "`n")
    Write-Host "Release $Version built in $Out"
    Get-ChildItem $Out -File | ForEach-Object { '{0,12:N0}  {1}' -f $_.Length, $_.Name }
} finally { Pop-Location }
