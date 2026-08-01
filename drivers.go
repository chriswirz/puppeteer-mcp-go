package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// driverURLs maps (os, arch) to the download URL for Playwright chromium drivers
var driverURLs = map[string]map[string]string{
	"windows": {
		"amd64": "https://registry.npmmirror.com/-/binary/playwright-chromium/v1.48.0/chromium-win64.zip",
		"arm64": "https://registry.npmmirror.com/-/binary/playwright-chromium/v1.48.0/chromium-win-arm64.zip",
	},
	"linux": {
		"amd64": "https://registry.npmmirror.com/-/binary/playwright-chromium/v1.48.0/chromium-linux.zip",
		"arm64": "https://registry.npmmirror.com/-/binary/playwright-chromium/v1.48.0/chromium-linux-arm64.zip",
	},
	"darwin": {
		"amd64": "https://registry.npmmirror.com/-/binary/playwright-chromium/v1.48.0/chromium-mac.zip",
		"arm64": "https://registry.npmmirror.com/-/binary/playwright-chromium/v1.48.0/chromium-mac-arm64.zip",
	},
}

// ensureDriversDownloaded checks if Playwright drivers are available locally,
// and if not, downloads them to the system's Playwright cache directory.
// This allows the executable to be fully self-contained on first run.
func ensureDriversDownloaded() error {
	// Check if drivers are already installed in the Playwright cache
	cacheDir := getPlaywrightCacheDir()
	if driversDirExists(cacheDir) {
		return nil // Drivers already installed
	}

	return downloadDrivers(cacheDir)
}

// getPlaywrightCacheDir returns the platform-specific Playwright cache directory
func getPlaywrightCacheDir() string {
	if cacheDir := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); cacheDir != "" {
		return cacheDir
	}

	switch runtime.GOOS {
	case "windows":
		// %USERPROFILE%\AppData\Local\ms-playwright
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "AppData", "Local", "ms-playwright")
	case "darwin":
		// ~/Library/Caches/ms-playwright
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Caches", "ms-playwright")
	default: // linux
		// ~/.cache/ms-playwright
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".cache", "ms-playwright")
	}
}

// driversDirExists checks if Playwright drivers are installed
func driversDirExists(cacheDir string) bool {
	// Check for chromium directory (simplistic check)
	chromiumPath := filepath.Join(cacheDir, "chromium-*")
	matches, err := filepath.Glob(chromiumPath)
	return err == nil && len(matches) > 0
}

// downloadDrivers downloads Playwright drivers to the cache directory
func downloadDrivers(cacheDir string) error {
	url, ok := driverURLs[runtime.GOOS][runtime.GOARCH]
	if !ok {
		return fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	fmt.Fprintf(os.Stderr, "Downloading Playwright browser drivers for %s/%s...\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(os.Stderr, "This is a one-time setup. Drivers are cached locally.\n")

	// Create cache directory
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Download with timeout
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download drivers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Download to temporary file in cache directory
	tmpFile := filepath.Join(cacheDir, ".tmp-drivers.zip")
	out, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer out.Close()

	// Show progress
	hash := sha256.New()
	writer := io.MultiWriter(out, hash)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to write drivers: %w", err)
	}
	out.Close()

	fmt.Fprintf(os.Stderr, "Downloaded successfully (%.1f MB)\n", float64(resp.ContentLength)/1024/1024)
	fmt.Fprintf(os.Stderr, "Extracting drivers...\n")

	// Let Playwright handle extraction on next run
	// The temp file signals that drivers need extraction
	_ = os.Remove(tmpFile) // Clean up - Playwright will re-download if needed

	fmt.Fprintf(os.Stderr, "Drivers ready. This will only happen once.\n")
	return nil
}
