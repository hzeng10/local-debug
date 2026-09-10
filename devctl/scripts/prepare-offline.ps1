param(
    [Parameter(Mandatory=$true)][string]$Config,
    [string]$Devctl,
    [string]$Proxy,
    [AllowEmptyString()][string]$NoProxy
)
$ErrorActionPreference = 'Stop'
# Handle native exit codes ourselves, including PowerShell 7 callers that enable this preference.
$PSNativeCommandUseErrorActionPreference = $false

# Prefer this kit over an unrelated/older devctl.exe on PATH. Explicit -Devctl wins.
if (-not $Devctl) {
    $kit = Split-Path -Parent $PSScriptRoot
    $packaged = Join-Path $kit 'devctl.exe'
    $arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
    $built = Join-Path $kit "dist/devctl-windows-$arch.exe"
    if (Test-Path -LiteralPath $packaged -PathType Leaf) { $Devctl = $packaged }
    elseif (Test-Path -LiteralPath $built -PathType Leaf) { $Devctl = $built }
    else { $Devctl = (Get-Command devctl.exe -CommandType Application -ErrorAction Stop).Source }
}
$resolved = (Get-Command $Devctl -ErrorAction Stop).Source
[Console]::Error.WriteLine("prepare-offline: devctl=$resolved")

function Read-ProxyEnv([string]$Name) {
    $value = [Environment]::GetEnvironmentVariable($Name, 'Process')
    if ([string]::IsNullOrWhiteSpace($value)) {
        $value = [Environment]::GetEnvironmentVariable($Name.ToLowerInvariant(), 'Process')
    }
    if ($null -eq $value) { return '' }
    return $value.Trim()
}
function Normalize-Proxy([string]$Value, [string]$Name) {
    if ([string]::IsNullOrWhiteSpace($Value)) { return '' }
    $value = $Value.Trim()
    if (-not $value.Contains('://')) { $value = 'http://' + $value }
    $uri = $null
    if (-not [Uri]::TryCreate($value, [UriKind]::Absolute, [ref]$uri) -or
        $uri.Scheme -notin @('http','https','socks5','socks5h') -or
        -not $uri.Host -or $uri.Query -or $uri.Fragment -or
        ($uri.AbsolutePath -and $uri.AbsolutePath -ne '/')) {
        # Never include the supplied value: it may contain credentials.
        throw "$Name is invalid; use http://host:port, https://host:port or socks5://host:port. HTTPS_PROXY usually points to an http:// CONNECT proxy."
    }
    return $value
}

$names = @('HTTP_PROXY','HTTPS_PROXY','NO_PROXY','http_proxy','https_proxy','no_proxy')
# Ordinal keys preserve differently cased variables on non-Windows test hosts.
$saved = [Collections.Generic.Dictionary[string,string]]::new([StringComparer]::Ordinal)
foreach ($name in $names) { $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$code = 1
try {
    $http = Read-ProxyEnv 'HTTP_PROXY'
    $https = Read-ProxyEnv 'HTTPS_PROXY'
    $bypass = Read-ProxyEnv 'NO_PROXY'
    if ($PSBoundParameters.ContainsKey('Proxy')) {
        if ([string]::IsNullOrWhiteSpace($Proxy)) { throw '-Proxy must not be empty.' }
        $http = $Proxy
        $https = $Proxy
    }
    $http = Normalize-Proxy $http 'HTTP_PROXY'
    $https = Normalize-Proxy $https 'HTTPS_PROXY'
    if (-not $https -and $http) {
        $https = $http
        [Console]::Error.WriteLine('prepare-offline: HTTPS_PROXY is unset; using HTTP_PROXY for HTTPS downloads.')
    }
    if ($PSBoundParameters.ContainsKey('NoProxy')) { $bypass = $NoProxy }
    foreach ($pair in @(@('HTTP_PROXY',$http), @('HTTPS_PROXY',$https), @('NO_PROXY',$bypass))) {
        [Environment]::SetEnvironmentVariable($pair[0], $pair[1], 'Process')
        [Environment]::SetEnvironmentVariable($pair[0].ToLowerInvariant(), $pair[1], 'Process')
    }
    if ($https) {
        $uri = [Uri]$https
        [Console]::Error.WriteLine("prepare-offline: HTTPS proxy=$($uri.Scheme)://$($uri.Authority) (credentials hidden); inherited by devctl and crane.")
    } else {
        [Console]::Error.WriteLine('prepare-offline: no HTTPS proxy; downloads will connect directly. Set $env:HTTPS_PROXY or pass -Proxy. Shell variables such as $HTTPS_PROXY are not environment variables.')
    }
    if ($bypass) {
        [Console]::Error.WriteLine('prepare-offline: NO_PROXY is set; matching hosts bypass the proxy. Check for * or GitHub/registry domains if direct connections fail.')
    }
    & $resolved bundle prepare --config $Config --format json
    $code = $LASTEXITCODE
} finally {
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
}
exit $code
