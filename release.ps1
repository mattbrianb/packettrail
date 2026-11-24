# Packages a release into .\dist\release: the Windows zip and the Linux
# tarballs (each with the executable, README, licence and third-party notices)
# plus SHA256SUMS.txt. The macOS PacketTrail.app zips are built and signed on a
# Mac by GitHub Actions (.github/workflows/macos-app.yml) when the release is
# published, and added to the release and its SHA256SUMS.txt there.
#   .\release.ps1              # test, build and package
#   .\release.ps1 -SkipBuild   # package what is already in .\dist
param([string]$Version = "1.5.0", [switch]$SkipBuild)
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
Push-Location $root
try {
    if (-not $SkipBuild) {
        & .\build.ps1 -Version $Version
        if ($LASTEXITCODE) { throw 'build failed' }
    }
    $tc = Join-Path $root '.toolchain'
    $go = Join-Path $tc 'go\bin\go.exe'
    if (Test-Path $go) {
        $env:GOROOT = Join-Path $tc 'go'; $env:GOPATH = Join-Path $tc 'gopath'
        $env:GOMODCACHE = Join-Path $tc 'gopath\pkg\mod'; $env:GOCACHE = Join-Path $tc 'gocache'
    } else { $go = 'go' }
    $env:GOTOOLCHAIN = 'local'; $env:GOTELEMETRY = 'off'; $env:CGO_ENABLED = '0'

    $out = Join-Path $root 'dist\release'
    if (Test-Path $out) { Remove-Item $out -Recurse -Force }
    New-Item -ItemType Directory -Force $out | Out-Null

    # Notices shipped inside every archive: the repo's list plus the Twemoji licence text.
    $notices = Join-Path $out 'THIRD-PARTY-NOTICES.md'
    ((Get-Content THIRD-PARTY-NOTICES.md -Raw) + "`n---`n`n" + (Get-Content third_party\TwemojiCountryFlags-LICENSE.md -Raw)) |
        Set-Content $notices -Encoding utf8

    # Asset names carry no version, so ".../releases/latest/download/<name>"
    # links on the website always fetch the newest release.
    $stage = Join-Path $out 'packettrail-windows-x64'
    New-Item -ItemType Directory -Force $stage | Out-Null
    Copy-Item dist\packettrail.exe, README.md, LICENSE, $notices $stage
    Compress-Archive -Path (Join-Path $stage '*') -DestinationPath (Join-Path $out 'packettrail-windows-x64.zip') -CompressionLevel Optimal
    # The installer carries GeoIP seed files into stable per-user storage.
    # Existing databases are preserved by packaging/packettrail.iss.
    $geoDir = Join-Path $root 'dist\data'
    & $go run ./tools/geoipseed -dir $geoDir
    if ($LASTEXITCODE) { throw 'GeoIP installer seed preparation failed' }
    foreach ($file in @('dbip-city-lite.mmdb', 'dbip-city-lite.version', 'dbip-asn-lite.mmdb', 'dbip-asn-lite.version')) {
        if (-not (Test-Path (Join-Path $geoDir $file))) { throw "missing GeoIP installer seed: $file" }
    }
    $iscc = Join-Path $env:LOCALAPPDATA 'Programs\Inno Setup 7\ISCC.exe'
    if (-not (Test-Path $iscc)) { $iscc = (Get-Command ISCC.exe -ErrorAction SilentlyContinue).Source }
    if (-not $iscc -or -not (Test-Path $iscc)) { throw 'Inno Setup 7 compiler (ISCC.exe) is required for the Windows installer' }
    & $iscc "/DAppVersion=$Version" "/DSourceDir=$stage" "/DGeoDir=$geoDir" "/DOutputDir=$out" (Join-Path $root 'packaging\packettrail.iss')
    if ($LASTEXITCODE) { throw 'Windows installer build failed' }
    Remove-Item $stage -Recurse -Force
    Write-Host 'packaged packettrail-windows-x64.zip and packettrail-windows-x64-setup.exe'

    # Linux: tarballs with real Unix permissions (the binary is executable).
    foreach ($t in @(@{ name = 'packettrail-linux-x64'; bin = 'dist\linux-amd64\packettrail' },
                     @{ name = 'packettrail-linux-arm64'; bin = 'dist\linux-arm64\packettrail' })) {
        & $go run ./tools/pack -o (Join-Path $out "$($t.name).tar.gz") -prefix $t.name -exec $t.bin README.md LICENSE $notices
        if ($LASTEXITCODE) { throw "pack failed for $($t.name)" }
        Write-Host "packaged $($t.name).tar.gz"
    }
    Remove-Item $notices

    $sums = Get-ChildItem $out -File | Sort-Object Name | ForEach-Object {
        '{0}  {1}' -f (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower(), $_.Name
    }
    # Unix line endings: with CRLF, "sha256sum -c" on Linux and macOS looks
    # for file names that end in a carriage return and finds none.
    [IO.File]::WriteAllText((Join-Path $out 'SHA256SUMS.txt'), (($sums -join "`n") + "`n"), [Text.Encoding]::ASCII)
} finally {
    Pop-Location
}
Get-ChildItem (Join-Path $root 'dist\release') -File | Select-Object Name, @{ n = 'MB'; e = { [math]::Round($_.Length / 1MB, 1) } }
