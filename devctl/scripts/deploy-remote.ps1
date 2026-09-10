param([Parameter(Mandatory=$true)][string]$Config,
 [ValidateSet('discover','plan','apply','verify','export')][string]$Operation = 'apply',
 [string]$Devctl = 'devctl.exe')
$ErrorActionPreference = 'Stop'
& $Devctl admin remote --config $Config --operation $Operation --format json
exit $LASTEXITCODE
