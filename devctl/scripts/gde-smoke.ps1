param([Parameter(Mandatory=$true)][string]$Config)
$ErrorActionPreference = 'Stop'
$c = Get-Content -Raw -LiteralPath $Config | ConvertFrom-Json
$base = [Uri]$c.baseURL
if ($base.Scheme -ne 'http' -or $base.Host -notin @('127.0.0.1','localhost','::1')) { throw 'Smoke target must be the local JVM' }
$results = @()
foreach ($path in (@($c.healthPath) + @($c.readOnlyPaths))) {
    if (-not $path.StartsWith('/') -or $path.StartsWith('//')) { throw 'Only relative API paths are accepted' }
    $response = Invoke-WebRequest -UseBasicParsing -Uri ($base.AbsoluteUri.TrimEnd('/') + $path) -Method Get -TimeoutSec $c.timeoutSeconds -MaximumRedirection 0
    if ($response.StatusCode -ne 200) { throw 'Read-only smoke endpoint failed' }
    $results += @{ path=$path; status=200 }
}
@{ok=$true; checks=$results; businessReadsConfigured=(@($c.readOnlyPaths).Count -gt 0)} | ConvertTo-Json -Depth 4
