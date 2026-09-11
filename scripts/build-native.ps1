param([switch]$SkipUI)
$ErrorActionPreference = 'Stop'
$repoPath = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$releaseVersion = 'v' + (Get-Content -LiteralPath (Join-Path $repoPath 'VERSION') -Raw).Trim()
if (-not $SkipUI) {
    Push-Location (Join-Path $repoPath 'management-ui')
    try {
        & npm exec --yes --package=bun@1.3.14 -- bun install --frozen-lockfile
        if ($LASTEXITCODE -ne 0) { throw 'Frontend dependency installation failed.' }
        $previousVersion = $env:VERSION
        try {
            $env:VERSION = $releaseVersion
            & npm exec --yes --package=bun@1.3.14 -- bun run build
            if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed.' }
        } finally { $env:VERSION = $previousVersion }
        Copy-Item -LiteralPath 'dist/index.html' -Destination (Join-Path $repoPath 'internal/native/web/management.html')
    } finally { Pop-Location }
}
Push-Location $repoPath
try {
    New-Item -ItemType Directory -Force 'bin' | Out-Null
    $releaseCommit = (& git rev-parse --short HEAD).Trim()
    $releaseDate = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
    & go build -trimpath -ldflags "-s -w -X main.Version=$releaseVersion -X main.Commit=$releaseCommit -X main.BuildDate=$releaseDate" -o bin/cli-proxy-api-native.exe ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw 'CPA build failed.' }
} finally { Pop-Location }
