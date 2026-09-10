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
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
    go build -trimpath '-ldflags=-s -w' -o dist/devctl.exe ./cmd/devctl
    if ($LASTEXITCODE -ne 0) { throw 'Build failed' }
    Get-FileHash dist/devctl.exe -Algorithm SHA256 | Format-List
} finally {
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
    Pop-Location
}
