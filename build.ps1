# Build puppeteer-mcp.exe locally.
#
# Released builds are versioned v0.1.<run number> by the GitHub Actions
# pipeline. A local build has no run number, so it stamps 0.1.0000-dev: the
# version string alone tells you a binary did not come from CI.
$ErrorActionPreference = "Stop"
$version = "0.1.0000-dev"
$sha = (git rev-parse --short HEAD 2>$null)
if ($sha) { $version = "$version+$sha" }
go build -trimpath -ldflags "-s -w -X main.version=$version" -o puppeteer-mcp.exe .
Write-Host "built puppeteer-mcp.exe $version"
