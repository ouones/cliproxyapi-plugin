[CmdletBinding()]
param(
    [ValidateSet('windows', 'linux', 'darwin')]
    [string]$GOOS = 'windows',

    [ValidateSet('amd64', 'arm64')]
    [string]$GOARCH = 'amd64',

    [string]$OutputRoot = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    $OutputRoot = Join-Path $repoRoot 'dist'
} else {
    $OutputRoot = [IO.Path]::GetFullPath($OutputRoot)
}

$extension = switch ($GOOS) {
    'windows' { '.dll' }
    'linux' { '.so' }
    'darwin' { '.dylib' }
}

$outputDirectory = Join-Path (Join-Path $OutputRoot $GOOS) $GOARCH
$outputPath = Join-Path $outputDirectory ("command-code" + $extension)
$headerPath = Join-Path $outputDirectory 'command-code.h'

New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null

$env:CGO_ENABLED = '1'
$env:GOOS = $GOOS
$env:GOARCH = $GOARCH

Push-Location $repoRoot
try {
    & go build -trimpath -buildmode=c-shared -o $outputPath .
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    Pop-Location
}

if (Test-Path -LiteralPath $headerPath) {
    Remove-Item -LiteralPath $headerPath -Force
}

Write-Output $outputPath
