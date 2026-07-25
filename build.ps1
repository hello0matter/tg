$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Push-Location "$root\webui"
try {
    if (-not (Test-Path "node_modules")) {
        npm install
    }
    npm run build
} finally {
    Pop-Location
}

$env:GOTOOLCHAIN = "go1.25.12"
Push-Location $root
try {
    go test ./...
    New-Item -ItemType Directory -Force "$root\dist" | Out-Null
    go build -trimpath -ldflags "-s -w" -o "$root\dist\TGWorkbench.exe" ./cmd/tgworkbench
    Write-Host "Built: $root\dist\TGWorkbench.exe"
} finally {
    Pop-Location
}
