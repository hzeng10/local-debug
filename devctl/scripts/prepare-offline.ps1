param([Parameter(Mandatory=$true)][string]$Config, [string]$Devctl = 'devctl.exe')
$ErrorActionPreference = 'Stop'
& $Devctl bundle prepare --config $Config --format json
exit $LASTEXITCODE
