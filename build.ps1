param([string]$Go, [switch]$Test)
$ErrorActionPreference='Stop'
$arguments=@{Test=$Test}
if($Go){$arguments.Go=$Go}
& (Join-Path $PSScriptRoot 'native_go\mygoapp\build.ps1') @arguments
