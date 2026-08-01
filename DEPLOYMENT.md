# Deployment Guide for puppeteer-mcp

## One-Time Setup on Target System

The first time you deploy puppeteer-mcp to a new system, you need to install Playwright's browser drivers:

### Option 1: Using puppeteer-mcp (Easiest)
Run the executable with the `--install-drivers` flag:
```powershell
.\puppeteer-mcp.exe --install-drivers
```

### Option 2: Setup Script (PowerShell)
Run the included setup script:
```powershell
.\setup.ps1
```

### Option 3: Manual Installation
If you prefer to install manually (requires Go):
```powershell
go run github.com/playwright-community/playwright-go/cmd/playwright@latest install --with-deps chromium
```

**Requirements:**
- For Option 1: Just the puppeteer-mcp.exe (no Go needed!)
- For Options 2 & 3: Go must be installed (https://golang.org/dl/)
- Internet connection to download drivers (~300MB)
- Run once per system (drivers are cached locally)

## Updating to the Latest Release

To automatically download and install the latest version:
```powershell
.\puppeteer-mcp.exe --update
```

This downloads the latest binary from GitHub, verifies it, and replaces your current executable.

## Running puppeteer-mcp with Chrome

### Option 1: Auto-launch browser with config (Easiest)

Create `config.json`:
```json
{
  "server": { "transport": "stdio" },
  "browser": {
    "cdp_url": "http://127.0.0.1:9222",
    "cdp_launch_cmd": "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe --remote-debugging-port=9222 --user-data-dir=D:\\puppeteer-mcp\\data"
  }
}
```

Then just run:
```powershell
.\puppeteer-mcp.exe -c config.json
```

The browser launches automatically!

### Option 2: Auto-launch with command-line flag

```powershell
.\puppeteer-mcp.exe `
  --cdp http://127.0.0.1:9222 `
  --cdp-launch-cmd "C:\Program Files\Google\Chrome\Application\chrome.exe --remote-debugging-port=9222 --user-data-dir=D:\puppeteer-mcp\data"
```

### Option 3: Manual browser launch (Traditional)

Start Chrome manually:
```powershell
"C:\Program Files\Google\Chrome\Application\chrome.exe" `
  --remote-debugging-port=9222 `
  --user-data-dir=D:\puppeteer-mcp\data
```

Then connect to it:
```powershell
.\puppeteer-mcp.exe --cdp http://127.0.0.1:9222
```

## Configuration Example

Create `config.json` for attach mode:
```json
{
  "server": {
    "name": "puppeteer-mcp",
    "transport": "stdio"
  },
  "browser": {
    "cdp_url": "http://127.0.0.1:9222",
    "cdp_launch_cmd": "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe --remote-debugging-port=9222 --user-data-dir=D:\\puppeteer-mcp\\data"
  }
}
```

**Note:** Config files are automatically normalized to UTF-8 Unix format regardless of how they were created (PowerShell, Windows editors, etc.), so you don't need to worry about file encoding or line endings.

## Troubleshooting

### "please install the driver (v1.60.0) first"
- Run: `.\puppeteer-mcp.exe --install-drivers`
- Or use setup script: `.\setup.ps1`
- Or manually: `go run github.com/playwright-community/playwright-go/cmd/playwright@latest install --with-deps chromium`

### Chrome connection refused
- Ensure Chrome is running with `--remote-debugging-port=9222`
- Check that the port number matches in both the Chrome command and `--cdp` flag
- The `--user-data-dir` flag is required (Chrome won't open the debugging port without it)

### Go not found
- Install Go from https://golang.org/dl/
- Ensure it's in your PATH (you should be able to run `go version`)
