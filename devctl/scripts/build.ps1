$ErrorActionPreference = 'Stop'
Push-Location (Join-Path $PSScriptRoot '..')
$names = @('GOTOOLCHAIN','GOPROXY','GOSUMDB','CGO_ENABLED','GOOS','GOARCH')
$saved = @{}
foreach ($name in $names) { $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
try {
    $env:GOTOOLCHAIN = 'local'; $env:GOPROXY = 'off'; $env:GOSUMDB = 'off'; $env:CGO_ENABLED = '0'
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed' }
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Vet failed' }
    New-Item -ItemType Directory -Force dist | Out-Null
    foreach ($target in @('windows/amd64','windows/arm64','linux/amd64','linux/arm64')) {
        $parts = $target.Split('/'); $env:GOOS = $parts[0]; $env:GOARCH = $parts[1]
        $suffix = if ($env:GOOS -eq 'windows') { '.exe' } else { '' }
        $output = "dist/devctl-$($env:GOOS)-$($env:GOARCH)$suffix"
        go build -trimpath '-ldflags=-s -w' -o $output ./cmd/devctl
        if ($LASTEXITCODE -ne 0) { throw "Build failed: $target" }
    }
    Copy-Item -Force dist/devctl-windows-amd64.exe dist/devctl.exe
    $sums = Get-ChildItem dist/devctl-* -File | Sort-Object Name | ForEach-Object {
        ((Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() + '  ' + $_.Name)
    }
    [IO.File]::WriteAllLines((Join-Path (Get-Location) 'dist/SHA256SUMS'), $sums, [Text.UTF8Encoding]::new($false))
} finally {
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
    Pop-Location
}
