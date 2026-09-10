param([string]$Destination = 'dist/devctl-admin-kit.zip')
$ErrorActionPreference = 'Stop'
Push-Location (Join-Path $PSScriptRoot '..')
$temp = Join-Path ([IO.Path]::GetTempPath()) ('devctl-package-' + [Guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory $temp | Out-Null
    foreach ($folder in @('deploy','docs','examples','scripts')) {
        Copy-Item -Recurse -LiteralPath $folder -Destination $temp
    }
    # Exclude local site configuration and credentials from public packages.
    Get-ChildItem -LiteralPath $temp -Recurse -File | Where-Object {
        $_.Name -like '*.local.*' -or $_.Name -like '*.credentials.json' -or $_.Name -like '*.private.json' -or $_.Name -like '*.log'
    } | Remove-Item
    Copy-Item README.md $temp
    New-Item -ItemType Directory (Join-Path $temp 'dist') | Out-Null
    foreach ($file in @('devctl-windows-amd64.exe','devctl-windows-arm64.exe','devctl-linux-amd64','devctl-linux-arm64')) {
        Copy-Item -LiteralPath (Join-Path dist $file) -Destination (Join-Path $temp 'dist')
    }
    Copy-Item dist/devctl-windows-amd64.exe (Join-Path $temp 'devctl.exe')
    $sums = Get-ChildItem -LiteralPath $temp -Recurse -File | Sort-Object FullName | ForEach-Object {
        $relative = $_.FullName.Substring($temp.Length + 1).Replace('\','/')
        ((Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() + '  ' + $relative)
    }
    [IO.File]::WriteAllLines((Join-Path $temp 'SHA256SUMS'), $sums, [Text.UTF8Encoding]::new($false))
    Compress-Archive -Path (Join-Path $temp '*') -DestinationPath $Destination -Force
    $distSums = Get-ChildItem dist/devctl-* -File | Sort-Object Name | ForEach-Object {
        ((Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() + '  ' + $_.Name)
    }
    [IO.File]::WriteAllLines((Join-Path (Get-Location) 'dist/SHA256SUMS'), $distSums, [Text.UTF8Encoding]::new($false))
    Get-FileHash -LiteralPath $Destination -Algorithm SHA256
} finally {
    Remove-Item -Recurse -Force -LiteralPath $temp -ErrorAction SilentlyContinue
    Pop-Location
}
