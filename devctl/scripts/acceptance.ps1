param(
    [Parameter(Mandatory=$true)][string]$Profile,
    [ValidateSet('kubectl','ssh')][string]$Transport = 'kubectl',
    [string]$SshHost,
    [string]$Devctl = 'devctl.exe',
    [string]$ReportDir = (Join-Path (Get-Location) ('devctl-report-' + (Get-Date -Format yyyyMMdd-HHmmss)))
)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force $ReportDir | Out-Null
$common = @('--profile', $Profile, '--transport', $Transport, '--format', 'json')
if ($SshHost) { $common += @('--ssh-host', $SshHost) }
function Invoke-Step([string]$Name, [string[]]$Extra = @()) {
    $output = & $Devctl $Name @common @Extra 2>&1
    $code = $LASTEXITCODE
    $output | Set-Content -Encoding UTF8 (Join-Path $ReportDir ($Name + '.json'))
    if ($code -ne 0) { throw "$Name failed; see report (exit $code)" }
}
& $Devctl status --format json 1>$null 2>$null
if ($LASTEXITCODE -eq 0) { throw 'Acceptance requires no existing devctl session; finish existing work and disconnect first.' }
$owned = $false
try {
    Invoke-Step 'validate'
    Invoke-Step 'doctor'
    Invoke-Step 'connect'
    $owned = $true
    Invoke-Step 'run'
    Invoke-Step 'probe'
    Invoke-Step 'test' @('--suite', 'smoke')
    Invoke-Step 'status'
} finally {
    if ($owned) {
        & $Devctl disconnect --format json | Set-Content -Encoding UTF8 (Join-Path $ReportDir 'disconnect.json')
        if ($LASTEXITCODE -ne 0) { Write-Warning 'Disconnect did not complete; inspect devctl state.' }
    }
}
Write-Output "Acceptance normal-path report: $ReportDir"
