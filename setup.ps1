# One-time setup script for puppeteer-mcp deployment
# Run this once on the target system before using puppeteer-mcp

Write-Host "Setting up puppeteer-mcp..." -ForegroundColor Green

# Check if Go is installed
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "Error: Go is not installed or not in PATH" -ForegroundColor Red
    Write-Host "Please install Go from https://golang.org/dl/" -ForegroundColor Yellow
    exit 1
}

Write-Host "Installing Playwright browser drivers..." -ForegroundColor Cyan
go run github.com/playwright-community/playwright-go/cmd/playwright@latest install --with-deps chromium

if ($LASTEXITCODE -eq 0) {
    Write-Host "Setup complete! You can now run puppeteer-mcp" -ForegroundColor Green
} else {
    Write-Host "Setup failed. Check the errors above." -ForegroundColor Red
    exit 1
}
