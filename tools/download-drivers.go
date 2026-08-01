// +build ignore

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: go run tools/download-drivers.go <goos> [goarch]\n")
		fmt.Fprintf(os.Stderr, "Example: go run tools/download-drivers.go windows amd64\n")
		os.Exit(1)
	}

	goos := os.Args[1]
	goarch := "amd64"
	if len(os.Args) > 2 {
		goarch = os.Args[2]
	}

	// Playwright version to download drivers for
	playwrightVersion := "v1.48.0"

	// Map of (goos, goarch) -> driver filename and URL
	drivers := map[string]map[string]string{
		"windows": {
			"amd64": fmt.Sprintf("https://registry.npmmirror.com/-/binary/playwright-chromium/%s/chromium-win64.zip", playwrightVersion),
		},
		"linux": {
			"amd64": fmt.Sprintf("https://registry.npmmirror.com/-/binary/playwright-chromium/%s/chromium-linux.zip", playwrightVersion),
			"arm64": fmt.Sprintf("https://registry.npmmirror.com/-/binary/playwright-chromium/%s/chromium-linux-arm64.zip", playwrightVersion),
		},
		"darwin": {
			"amd64": fmt.Sprintf("https://registry.npmmirror.com/-/binary/playwright-chromium/%s/chromium-mac.zip", playwrightVersion),
			"arm64": fmt.Sprintf("https://registry.npmmirror.com/-/binary/playwright-chromium/%s/chromium-mac-arm64.zip", playwrightVersion),
		},
	}

	urls, ok := drivers[goos]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unsupported OS: %s\n", goos)
		os.Exit(1)
	}

	url, ok := urls[goarch]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unsupported architecture for %s: %s\n", goos, goarch)
		os.Exit(1)
	}

	// Create drivers directory
	driverDir := filepath.Join(".", "drivers", fmt.Sprintf("%s-%s", goos, goarch))
	if err := os.MkdirAll(driverDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create directory: %v\n", err)
		os.Exit(1)
	}

	outFile := filepath.Join(driverDir, fmt.Sprintf("chromium-%s-%s.zip", goos, goarch))

	fmt.Printf("Downloading Playwright driver for %s/%s from %s\n", goos, goarch, url)
	fmt.Printf("Saving to: %s\n", outFile)

	// Download the driver
	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to download: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Download failed with status %d\n", resp.StatusCode)
		os.Exit(1)
	}

	out, err := os.Create(outFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create output file: %v\n", err)
		os.Exit(1)
	}
	defer out.Close()

	// Calculate SHA256 while downloading
	hash := sha256.New()
	writer := io.MultiWriter(out, hash)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write file: %v\n", err)
		os.Exit(1)
	}

	checksum := hex.EncodeToString(hash.Sum(nil))
	fmt.Printf("Downloaded successfully. SHA256: %s\n", checksum)
	fmt.Printf("File size: %.2f MB\n", float64(resp.ContentLength)/1024/1024)
}
