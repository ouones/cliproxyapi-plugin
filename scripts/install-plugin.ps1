[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ArtifactPath,

    [Parameter(Mandatory = $true)]
    [string]$PluginDir
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$artifactFullPath = (Resolve-Path -LiteralPath $ArtifactPath -ErrorAction Stop).Path
$extension = [IO.Path]::GetExtension($artifactFullPath).ToLowerInvariant()
if ($extension -notin @('.so', '.dll', '.dylib')) {
    throw "unsupported plugin artifact extension: $extension"
}

if (Test-Path -LiteralPath $PluginDir) {
    $pluginDirectory = (Resolve-Path -LiteralPath $PluginDir -ErrorAction Stop).Path
} else {
    $pluginDirectory = [IO.Path]::GetFullPath($PluginDir)
}

New-Item -ItemType Directory -Force -Path $pluginDirectory | Out-Null
$destinationPath = Join-Path $pluginDirectory ("command-code" + $extension)
Copy-Item -LiteralPath $artifactFullPath -Destination $destinationPath -Force
Write-Output $destinationPath
