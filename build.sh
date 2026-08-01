#!/bin/sh
# Build puppeteer-mcp locally.
#
# Released builds are versioned v0.1.<run number> by the GitHub Actions
# pipeline. A local build has no run number, so it stamps 0.1.0000-dev: the
# version string alone tells you a binary did not come from CI.
set -e
version="0.1.0000-dev"
if sha=$(git rev-parse --short HEAD 2>/dev/null); then
  version="${version}+${sha}"
fi
go build -trimpath -ldflags "-s -w -X main.version=$version" -o puppeteer-mcp .
echo "built puppeteer-mcp $version"
