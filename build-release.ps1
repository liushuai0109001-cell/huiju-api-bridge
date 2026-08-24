param(
    [string]$Version = "1.0.0"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$publicKeyPath = Get-ChildItem -LiteralPath $root -Recurse -File -Filter "license_public.key" | Select-Object -First 1 -ExpandProperty FullName
if (-not $publicKeyPath) {
    throw "License public key was not found."
}
$publicKey = (Get-Content -Raw -LiteralPath $publicKeyPath).Trim()
$clientName = Get-ChildItem -LiteralPath $root -File -Filter "*.exe" | Where-Object { $_.Name -notlike "*.new.exe" } | Sort-Object Length -Descending | Select-Object -First 1 -ExpandProperty Name
if (-not $clientName) {
    throw "Existing client executable was not found."
}

Push-Location $root
try {
    go test ./...
    go vet ./...

    $releaseRoot = Join-Path $root "release"
    $packageName = "huiju-api-bridge-v$Version-windows-amd64"
    $packageDir = Join-Path $releaseRoot $packageName
    if (Test-Path -LiteralPath $packageDir) {
        throw "Release directory already exists: $packageDir"
    }
    New-Item -ItemType Directory -Path $packageDir -Force | Out-Null

    $tempExe = Join-Path $packageDir "client.tmp.exe"
    $ldflags = "-H windowsgui -s -w -X main.licensePublicKey=$publicKey -X main.appVersion=v$Version"
    go build -trimpath -ldflags $ldflags -o $tempExe .
    Move-Item -LiteralPath $tempExe -Destination (Join-Path $packageDir $clientName)

    Get-ChildItem -LiteralPath $root -File -Filter "*.bat" | Select-Object -First 1 | Copy-Item -Destination $packageDir
    Copy-Item -LiteralPath (Join-Path $root "config.example.json") -Destination $packageDir
    Copy-Item -LiteralPath (Join-Path $root "README.md") -Destination $packageDir
    Get-ChildItem -LiteralPath $root -File -Filter "*.txt" | Copy-Item -Destination $packageDir
    Get-ChildItem -LiteralPath $root -File -Filter "*.md" | Where-Object Name -ne "README.md" | Sort-Object Length -Descending | Select-Object -First 1 | Copy-Item -Destination $packageDir

    $zipPath = Join-Path $releaseRoot "$packageName.zip"
    Compress-Archive -LiteralPath $packageDir -DestinationPath $zipPath -CompressionLevel Optimal
    Write-Host "Release package: $zipPath"
} finally {
    Pop-Location
}
