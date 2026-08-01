# Deployment Guide for puppeteer-mcp

## Automatic Driver Installation (No Setup Required!)

**Browser drivers are downloaded automatically on first run.** When you first start puppeteer-mcp:
1. It checks if drivers are cached locally
2. If not, downloads them (~50-150MB depending on platform)
3. Caches them for future use

Just run it and it works!

```powershell
.\puppeteer-mcp.exe -c config.json
```

### Optional: Pre-download Drivers for Offline Use

For air-gapped systems or to pre-download drivers:

**Using puppeteer-mcp:**
```powershell
.\puppeteer-mcp.exe --install-drivers
```

**Using setup script:**
```powershell
.\setup.ps1
```

**Manual installation (requires Go):**
```powershell
go run github.com/playwright-community/playwright-go/cmd/playwright@latest install --with-deps chromium
```

**Requirements:**
- Internet connection on first run (auto-downloads if needed)
- No Go installation needed for automatic download!
- Drivers cached locally after first download

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

### "please install the driver" error

**Good news:** Drivers are downloaded automatically on first run!

If auto-download fails (no internet, proxy issues):
- Try running again with internet access
- Or manually pre-download: `.\puppeteer-mcp.exe --install-drivers`
- Or use setup script: `.\setup.ps1`

### Driver download hangs or fails

If the download is slow or times out:
1. Check your internet connection
2. Try again - downloads are resumable
3. Or manually run: `.\puppeteer-mcp.exe --install-drivers`

The drivers are only downloaded once and cached locally.

### Chrome connection refused
- Ensure Chrome is running with `--remote-debugging-port=9222`
- Check that the port number matches in both the Chrome command and `--cdp` flag
- The `--user-data-dir` flag is required (Chrome won't open the debugging port without it)
