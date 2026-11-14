# Builds packettrail for Windows, Linux and macOS into .\dist using the
# portable Go toolchain in .\.toolchain (falls back to a "go" on PATH).
#   .\build.ps1              # test + build all targets
#   .\build.ps1 -SkipTests   # build only
param([string]$Version = "1.2.0", [switch]$SkipTests)
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$tc = Join-Path $root '.toolchain'
$go = Join-Path $tc 'go\bin\go.exe'
if (Test-Path $go) {
    $env:GOROOT = Join-Path $tc 'go'
    $env:GOPATH = Join-Path $tc 'gopath'
    $env:GOMODCACHE = Join-Path $tc 'gopath\pkg\mod'
    $env:GOCACHE = Join-Path $tc 'gocache'
} else {
    $go = 'go'
}
$env:GOTELEMETRY = 'off'
$env:GOTOOLCHAIN = 'local'
$env:CGO_ENABLED = '0'

$targets = @(
    @{ os = 'windows'; arch = 'amd64'; out = 'dist\packettrail.exe' },
    @{ os = 'linux';   arch = 'amd64'; out = 'dist\linux-amd64\packettrail' },
    @{ os = 'linux';   arch = 'arm64'; out = 'dist\linux-arm64\packettrail' },
    @{ os = 'darwin';  arch = 'arm64'; out = 'dist\macos-arm64\packettrail' },
    @{ os = 'darwin';  arch = 'amd64'; out = 'dist\macos-amd64\packettrail' }
)

Push-Location $root
try {
    if (-not $SkipTests) {
        & $go vet ./...
        if ($LASTEXITCODE) { throw 'go vet failed' }
        & $go test ./...
        if ($LASTEXITCODE) { throw 'tests failed' }
    }
    foreach ($t in $targets) {
        $env:GOOS = $t.os
        $env:GOARCH = $t.arch
        Write-Host "building $($t.os)/$($t.arch) -> $($t.out)"
        & $go build -trimpath -ldflags "-s -w -X main.version=$Version" -o $t.out ./cmd/packettrail
        if ($LASTEXITCODE) { throw "build failed for $($t.os)/$($t.arch)" }
    }
    Copy-Item README.md dist\README.md -Force
    # SHA-256 of every executable, so a copy on another PC can be checked
    # against this build (Get-FileHash .\packettrail.exe).
    $distRoot = (Resolve-Path dist).Path
    $sums = Get-ChildItem dist -Recurse -File -Include packettrail, packettrail.exe | Where-Object FullName -NotMatch '\\screenshot-demo\\' | Sort-Object FullName | ForEach-Object {
        $rel = $_.FullName.Substring($distRoot.Length + 1).Replace('\', '/')
        '{0}  {1}' -f (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower(), $rel
    }
    Set-Content dist\SHA256SUMS.txt $sums -Encoding ascii
} finally {
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    Pop-Location
}
Get-ChildItem dist -Recurse -File -Include packettrail, packettrail.exe | Select-Object FullName, @{ n = 'MB'; e = { [math]::Round($_.Length / 1MB, 1) } }
