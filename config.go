package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Config is the whole of config.json.
type Config struct {
	Server  ServerConfig  `json:"server"`
	Tunnel  TunnelConfig  `json:"tunnel"`
	Browser BrowserConfig `json:"browser"`

	// path is the file this was loaded from, so a session id the tunnel
	// server issues can be written back to it. It is not part of the file.
	path string
}

// Path returns the file the configuration was loaded from.
func (c *Config) Path() string { return c.path }

// ErrNoConfigFile reports that there is nowhere to persist a session id: the
// server is running on its defaults and flags, with no config.json to write
// back to. It is not a failure of the tunnel, only of remembering the URL.
var ErrNoConfigFile = errors.New("no config file to write to")

// configWriteMu serialises the read-modify-write of the config file. It is a
// package variable rather than a field so that Config stays copyable - the
// server keeps its own copy, and a mutex inside would make that a vet error.
var configWriteMu sync.Mutex

// SaveSessionID writes a session id the tunnel server issued back into the
// tunnel section of config.json, so restarting the server reclaims the same
// public URL instead of being handed a new one.
//
// Every other key is left exactly as it was found: the file is decoded into
// raw messages, one field is replaced, and the result is written through a
// temporary file so an interrupted write cannot truncate the configuration.
func (c *Config) SaveSessionID(id string) error {
	if id == "" || id == c.Tunnel.SessionID {
		return nil
	}
	c.Tunnel.SessionID = id
	if c.path == "" {
		return ErrNoConfigFile
	}
	if _, err := os.Stat(c.path); err != nil {
		if os.IsNotExist(err) {
			return ErrNoConfigFile
		}
		return err
	}
	return c.updateSection("tunnel", "session_id", func(section map[string]json.RawMessage) error {
		encoded, err := json.Marshal(id)
		if err != nil {
			return err
		}
		section["session_id"] = encoded
		return nil
	})
}

// updateSection rewrites one top-level section of the config file, leaving
// every other key as it was found.
func (c *Config) updateSection(name, field string, mutate func(map[string]json.RawMessage) error) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()

	raw, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}), &doc); err != nil {
		return fmt.Errorf("%s: %w", c.path, err)
	}
	section := map[string]json.RawMessage{}
	if b, ok := doc[name]; ok {
		if err := json.Unmarshal(b, &section); err != nil {
			return fmt.Errorf("%s: %s: %w", c.path, name, err)
		}
	}
	if err := mutate(section); err != nil {
		return fmt.Errorf("updating %s.%s: %w", name, field, err)
	}
	if doc[name], err = json.Marshal(section); err != nil {
		return err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	// Written to a temporary file and renamed, so a crash mid-write leaves the
	// old configuration intact rather than half a file. 0600 because the
	// config can carry an auth token and a tunnel key.
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// TunnelConfig exposes this server on a public HTTPS URL through an
// https-tunnel server, without opening a port on this machine. The MCP handler
// is served in process by the tunnel client, so it works even where binding a
// local port is awkward, and it is what makes the browser reachable from a
// hosted client that cannot see this network.
//
// Think about what that means before enabling it: the tunnel is public, and
// every tool here drives a real browser holding your real sessions. Set an
// auth token, and consider browser.allow_eval false and a host allowlist.
type TunnelConfig struct {
	Enabled bool `json:"enabled"`

	// ServerURL is the tunnel control plane, e.g. https://tunnel.example.com.
	ServerURL string `json:"server_url"`

	// APIKey authenticates against that server. Leave it empty to read
	// APIKeyEnv instead, which keeps the key out of the config file.
	APIKey    string `json:"api_key"`
	APIKeyEnv string `json:"api_key_env"`

	// Subdomain asks for a particular label. The server grants it when free
	// and issues a random one otherwise, so read the URL the client reports
	// rather than assuming this one.
	Subdomain string `json:"subdomain"`

	// SessionID resumes a previous session, keeping its public URL. It is
	// normally left empty and managed through SessionFile.
	SessionID string `json:"session_id"`

	// SessionFile persists the session id the server issues, so a restart
	// reclaims the same URL. Relative paths resolve against the working
	// directory; empty means do not persist.
	SessionFile string `json:"session_file"`

	// Only serves the tunnel alone, with no local listener at all.
	Only bool `json:"only"`

	ClientInfo string `json:"client_info"`
}

// APIKeyValue is the key to authenticate with, from the config or the
// environment variable it names.
func (t TunnelConfig) APIKeyValue() string {
	if t.APIKey != "" {
		return t.APIKey
	}
	if name := t.APIKeyEnvName(); name != "" {
		return os.Getenv(name)
	}
	return ""
}

// APIKeyEnvName is the environment variable the key is read from.
func (t TunnelConfig) APIKeyEnvName() string {
	if t.APIKeyEnv != "" {
		return t.APIKeyEnv
	}
	return "TUNNEL_API_KEY"
}

// ServerConfig covers identity and transport. It is the same shape code-mcp
// uses, so a client configured for one server needs nothing new for this one.
type ServerConfig struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Instructions string `json:"instructions"`
	Transport    string `json:"transport"` // "stdio" (default) or "http"
	URL          string `json:"url"`       // full MCP endpoint URL clients connect to

	// AdditionalURLs serves several endpoints at once, typically one http and
	// one https on different ports. When set it replaces URL entirely.
	AdditionalURLs []string `json:"urls,omitempty"`

	AllowedOrigins []string `json:"allowed_origins"`

	// AllowedHeaders is what a CORS preflight is told a browser may send.
	// ["*"] allows any header; empty echoes whatever the preflight asks for.
	AllowedHeaders []string `json:"allowed_headers"`

	// AllowPrivateNetwork answers Chrome's Private Network Access preflight,
	// which a page on a public address must pass before it may reach a server
	// on a private one such as 127.0.0.1. On by default.
	AllowPrivateNetwork bool `json:"allow_private_network"`

	AuthToken string `json:"auth_token"` // optional bearer token

	// ScreenshotPath, when set (e.g. "/screenshots/"), serves the files
	// browser_screenshot writes under browser.screenshot_dir over this
	// transport, so a client reaching the server over HTTP or the tunnel can
	// fetch a capture that was too large to come back inline. Empty - the
	// default - serves nothing, because the files are only meaningful to a
	// client on the same machine as the server.
	//
	// The same auth_token guards these files as guards the MCP endpoint.
	ScreenshotPath string `json:"screenshot_path"`

	// ScreenshotListing serves a directory index at ScreenshotPath. Off by
	// default: a caller that knows a filename can fetch it without also being
	// handed the name of every other capture the session took.
	ScreenshotListing bool `json:"screenshot_listing"`

	TLSCertFile   string `json:"tls_cert_file"`
	TLSKeyFile    string `json:"tls_key_file"`
	TLSSelfSigned bool   `json:"tls_self_signed"`

	// LegacyCompatibility serves the initialize-based revisions 2025-03-26
	// through 2025-11-25 alongside the current one. On by default.
	LegacyCompatibility bool `json:"legacy_compatibility"`

	// SessionTimeoutSeconds is how long an idle legacy session is kept.
	SessionTimeoutSeconds int `json:"session_timeout_seconds"`
}

// BrowserConfig decides which browser the tools drive and how it is reached.
//
// There are two modes and the difference matters. In launch mode this server
// starts Chrome itself against ProfileDir, which is reproducible and can run
// headless - the right mode for a test run. In attach mode it connects to a
// Chrome you started yourself with --remote-debugging-port, which is the right
// mode for debugging: the window is yours, you are already signed in, your
// extension is already loaded, and you can watch what the agent does to it.
type BrowserConfig struct {
	// CDPURL, when set (e.g. "http://127.0.0.1:9222"), attaches to an
	// already-running Chrome over the DevTools protocol instead of launching
	// one. Most of the rest of this section is ignored then: the browser is
	// already running with whatever flags you gave it.
	CDPURL string `json:"cdp_url"`

	// Interactive says a person is watching this browser and will take turns
	// with the model in it: navigating somewhere and asking about it, or
	// letting the model drive and then stepping in. It forces a window,
	// follows whichever tab that person is actually looking at, and is what
	// makes browser_wait_for_user meaningful rather than a hang.
	//
	// Without it the browser is the model's alone, which is the right shape
	// for a test run and the wrong one for debugging together.
	Interactive bool `json:"interactive"`

	// FollowActiveTab makes the tools act on the tab that has focus rather
	// than the one a previous call happened to select. A person who opens a
	// new tab and asks "what is wrong with this page" means the tab they are
	// looking at, not the one from ten minutes ago. Defaults to on in
	// interactive mode and off otherwise.
	FollowActiveTab *bool `json:"follow_active_tab"`

	// StartURL is opened when the browser starts. It is what makes an
	// interactive session begin somewhere useful - the app under test, rather
	// than a blank tab someone has to type into.
	StartURL string `json:"start_url"`

	// Headless runs the launched browser with no window. Ignored when
	// attaching, and forced off when Extensions are loaded: Chrome will not
	// load an unpacked extension in headless mode.
	Headless bool `json:"headless"`

	// Channel picks a stable Chrome install ("chrome", "chrome-beta",
	// "msedge") rather than Playwright's bundled Chromium. Real Chrome is what
	// an extension will actually run in, so it is the default; set it to
	// "chromium" to use the bundled build instead.
	Channel string `json:"channel"`

	// ExecutablePath overrides Channel with a specific binary.
	ExecutablePath string `json:"executable_path"`

	// ProfileDir is the persistent user-data directory for launch mode, so
	// logins and extension state survive a restart.
	ProfileDir string `json:"profile_dir"`

	// Extensions are unpacked extension directories to load on launch, the
	// equivalent of --load-extension. Each is the directory holding
	// manifest.json.
	Extensions []string `json:"extensions"`

	// Args are extra Chrome command-line flags for launch mode.
	Args []string `json:"args"`

	// IgnoreDefaultArgs drops flags Playwright would otherwise pass.
	// --enable-automation is the one worth dropping: it is both a fingerprint
	// and the reason Chrome shows the automation infobar.
	IgnoreDefaultArgs []string `json:"ignore_default_args"`

	// UserAgent overrides the browser's own, for launch mode.
	UserAgent string `json:"user_agent"`

	// Viewport is the window size for pages this server opens. Zero means
	// leave the browser's own size alone, which is what you want when
	// attaching to a window a person is looking at.
	ViewportWidth  int `json:"viewport_width"`
	ViewportHeight int `json:"viewport_height"`

	// DefaultTimeoutMs bounds every wait a tool performs: locator resolution,
	// an explicit wait_for. A tool call may override it.
	DefaultTimeoutMs int `json:"default_timeout_ms"`

	// NavigationTimeoutMs bounds page loads specifically, which deserve longer
	// than an element lookup.
	NavigationTimeoutMs int `json:"navigation_timeout_ms"`

	// EventBufferSize is how many console messages, page errors and network
	// records are kept per page for browser_console and browser_network. The
	// buffer is a ring: the oldest entries are dropped, never the newest.
	EventBufferSize int `json:"event_buffer_size"`

	// SlowMoMs delays each browser operation, which makes a headed run
	// followable by eye.
	SlowMoMs int `json:"slow_mo_ms"`

	// ScreenshotDir is where browser_screenshot writes every capture. The file
	// is written whatever the size; MaxScreenshotBytes decides only whether the
	// image also comes back inline.
	ScreenshotDir string `json:"screenshot_dir"`

	// MaxScreenshotBytes caps an inline (base64) screenshot. A full-page
	// capture of a long document runs to megabytes, and a megabyte of base64
	// in a tool result is worse than useless to a model - past this size the
	// tool returns the file path alone. The file itself is always written.
	MaxScreenshotBytes int `json:"max_screenshot_bytes"`

	// AllowDownloads accepts downloads a page starts, saving them under
	// DownloadDir.
	AllowDownloads bool   `json:"allow_downloads"`
	DownloadDir    string `json:"download_dir"`

	// AllowEval permits browser_evaluate and browser_cdp, which run arbitrary
	// JavaScript and raw DevTools commands in the browser. On by default -
	// they are half the point of the server - but turning them off is what
	// makes it safe to point this at a browser holding a session you care
	// about.
	AllowEval bool `json:"allow_eval"`

	// AllowNavigateHosts, when non-empty, is the allowlist of hostnames
	// browser_navigate may open. An entry matches a host and its subdomains;
	// "*" allows anything.
	AllowNavigateHosts []string `json:"allow_navigate_hosts"`
}

// DefaultConfig is the configuration before any file is read.
func DefaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Name:                appName,
			Transport:           "stdio",
			URL:                 "http://127.0.0.1:8770/mcp",
			AllowedOrigins:      []string{"http://127.0.0.1", "http://localhost"},
			AllowPrivateNetwork: true,
			LegacyCompatibility: true,
		},
		Browser: BrowserConfig{
			AllowEval: true,
		},
	}
}

// LoadConfig reads config.json. A missing file is not an error unless the path
// was given explicitly: the server is fully usable on its defaults.
func LoadConfig(path string, explicit bool) (Config, error) {
	cfg := DefaultConfig()
	cfg.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !explicit {
			return cfg, nil
		}
		return cfg, err
	}
	// PowerShell's redirection writes a UTF-8 BOM, and --example-config piped
	// to a file is the documented way to get started, so tolerate one.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Normalize fills in derived values and validates the result. dir is the
// directory the server was started in, which relative paths resolve against.
func (c *Config) Normalize(dir string) error {
	if c.Server.Name == "" {
		c.Server.Name = appName
	}
	if c.Server.Version == "" {
		c.Server.Version = version
	}
	if c.Server.Instructions == "" {
		c.Server.Instructions = DefaultInstructions
	}
	switch c.Server.Transport {
	case "":
		c.Server.Transport = "stdio"
	case "http", "stdio":
	default:
		return fmt.Errorf("server.transport: want %q or %q, got %q", "stdio", "http", c.Server.Transport)
	}
	if c.Server.URL == "" {
		c.Server.URL = "http://127.0.0.1:8770/mcp"
	}
	if c.Server.SessionTimeoutSeconds <= 0 {
		c.Server.SessionTimeoutSeconds = 7200
	}
	if c.Server.Transport == "http" {
		if _, _, err := c.Endpoint(); err != nil {
			return err
		}
	}
	if c.Server.ScreenshotPath != "" {
		if c.Server.Transport != "http" {
			return fmt.Errorf("server.screenshot_path: only the HTTP transport serves files, so it needs server.transport %q, not %q", "http", c.Server.Transport)
		}
		p := c.Server.ScreenshotPath
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		// The trailing slash is what makes this a subtree rather than one
		// exact path, both for the mux pattern and for the prefix strip.
		if !strings.HasSuffix(p, "/") {
			p += "/"
		}
		if p == "/" {
			return fmt.Errorf("server.screenshot_path: %q would take over the whole server; use something like %q", c.Server.ScreenshotPath, "/screenshots/")
		}
		c.Server.ScreenshotPath = p
	}

	b := &c.Browser
	if b.CDPURL != "" {
		u, err := url.Parse(b.CDPURL)
		if err != nil {
			return fmt.Errorf("browser.cdp_url %q: %w", b.CDPURL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss" {
			return fmt.Errorf("browser.cdp_url: want an http(s) or ws(s) URL, got %q", b.CDPURL)
		}
	}
	if b.Channel == "" {
		b.Channel = "chrome"
	}
	// A person cannot take turns with a browser they cannot see.
	if b.Interactive {
		b.Headless = false
		if b.FollowActiveTab == nil {
			b.FollowActiveTab = boolPtr(true)
		}
	}
	if b.ProfileDir == "" {
		b.ProfileDir = "profile"
	}
	b.ProfileDir = resolveDir(dir, b.ProfileDir)
	for i, ext := range b.Extensions {
		abs := resolveDir(dir, ext)
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("browser.extensions[%d]: %w", i, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("browser.extensions[%d]: %s is not a directory; "+
				"an unpacked extension is loaded by the directory holding its manifest.json", i, abs)
		}
		if _, err := os.Stat(filepath.Join(abs, "manifest.json")); err != nil {
			return fmt.Errorf("browser.extensions[%d]: no manifest.json in %s", i, abs)
		}
		b.Extensions[i] = abs
	}
	// Chrome will not load an unpacked extension in headless mode, so a config
	// asking for both is a contradiction. The extension is the reason the
	// operator named it, so it wins.
	if len(b.Extensions) > 0 {
		b.Headless = false
	}
	if b.DefaultTimeoutMs <= 0 {
		b.DefaultTimeoutMs = 15000
	}
	if b.NavigationTimeoutMs <= 0 {
		b.NavigationTimeoutMs = 45000
	}
	if b.EventBufferSize <= 0 {
		b.EventBufferSize = 500
	}
	if b.ScreenshotDir == "" {
		b.ScreenshotDir = "screenshots"
	}
	b.ScreenshotDir = resolveDir(dir, b.ScreenshotDir)
	if b.DownloadDir == "" {
		b.DownloadDir = "downloads"
	}
	b.DownloadDir = resolveDir(dir, b.DownloadDir)
	if b.MaxScreenshotBytes <= 0 {
		b.MaxScreenshotBytes = 1 << 20
	}
	if b.ViewportWidth < 0 || b.ViewportHeight < 0 {
		return fmt.Errorf("browser.viewport_width/height: must not be negative")
	}

	if c.Tunnel.Enabled {
		if c.Server.Transport != "http" {
			return fmt.Errorf("tunnel.enabled: the tunnel serves the HTTP handler, so it needs server.transport %q, not %q", "http", c.Server.Transport)
		}
		if c.Tunnel.ServerURL == "" {
			return fmt.Errorf("tunnel.server_url: required when the tunnel is enabled, e.g. https://tunnel.example.com")
		}
		u, err := url.Parse(c.Tunnel.ServerURL)
		if err != nil {
			return fmt.Errorf("tunnel.server_url %q: %w", c.Tunnel.ServerURL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("tunnel.server_url: want an http(s) URL, got %q", c.Tunnel.ServerURL)
		}
		if c.Tunnel.APIKeyValue() == "" {
			return fmt.Errorf("tunnel.api_key: required when the tunnel is enabled, or set %s in the environment", c.Tunnel.APIKeyEnvName())
		}
		c.Tunnel.SessionFile = resolveDir(dir, c.Tunnel.SessionFile)
	}
	return nil
}

// FollowsActiveTab reports whether tool calls should retarget onto the tab
// that currently has focus.
func (b BrowserConfig) FollowsActiveTab() bool {
	return b.FollowActiveTab != nil && *b.FollowActiveTab
}

func boolPtr(v bool) *bool { return &v }

// ScreenshotURL turns a written capture into the URL that serves it, or "" if
// the files are not served or the file landed outside the served directory -
// which an absolute path argument can do.
func (c *Config) ScreenshotURL(file string) string {
	if c.Server.ScreenshotPath == "" || c.Server.Transport != "http" {
		return ""
	}
	rel, err := filepath.Rel(c.Browser.ScreenshotDir, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	base := c.Server.URL
	if urls := c.Server.URLs(); len(urls) > 0 {
		base = urls[0]
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = c.Server.ScreenshotPath + filepath.ToSlash(rel)
	return u.String()
}

// resolveDir makes a path absolute against base.
func resolveDir(base, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

// Endpoint splits the configured URL into a listen address and a path.
func (c *Config) Endpoint() (addr, path string, err error) {
	endpoints, err := c.Endpoints()
	if err != nil {
		return "", "", err
	}
	return endpoints[0].Addr, endpoints[0].Path, nil
}

// Endpoint is one URL this server answers on.
type Endpoint struct {
	URL  string
	Addr string
	Path string
	TLS  bool
}

// URLs is every URL the server is configured to serve, in order.
func (s ServerConfig) URLs() []string {
	if len(s.AdditionalURLs) > 0 {
		return s.AdditionalURLs
	}
	if s.URL == "" {
		return nil
	}
	return []string{s.URL}
}

// Endpoints parses every configured URL.
func (c *Config) Endpoints() ([]Endpoint, error) {
	urls := c.Server.URLs()
	if len(urls) == 0 {
		return nil, fmt.Errorf("server.url: no endpoint URL configured")
	}
	var endpoints []Endpoint
	seen := map[string]string{}

	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("server.url %q: %w", raw, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("server.url: want an http(s) URL, got %q", raw)
		}
		host := u.Hostname()
		if host == "" {
			host = "127.0.0.1"
		}
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		if _, convErr := strconv.Atoi(port); convErr != nil {
			return nil, fmt.Errorf("server.url %q: bad port %q", raw, port)
		}
		path := u.Path
		if path == "" {
			path = "/"
		}
		addr := net.JoinHostPort(host, port)

		// One port carries one protocol. Two URLs sharing an address must
		// agree on the scheme, or neither would work.
		if previous, ok := seen[addr]; ok && previous != u.Scheme {
			return nil, fmt.Errorf("server.urls: %s is configured for both %s and %s; "+
				"give the two schemes different ports", addr, previous, u.Scheme)
		}
		seen[addr] = u.Scheme

		for _, existing := range endpoints {
			if existing.Addr == addr && existing.Path == path {
				return nil, fmt.Errorf("server.urls: %q is listed twice", raw)
			}
		}
		endpoints = append(endpoints, Endpoint{URL: raw, Addr: addr, Path: path, TLS: u.Scheme == "https"})
	}
	return endpoints, nil
}

// Listeners groups the endpoints by listen address, since one address is one
// socket however many paths it serves.
func (c *Config) Listeners() ([]Listener, error) {
	endpoints, err := c.Endpoints()
	if err != nil {
		return nil, err
	}
	var listeners []Listener
	index := map[string]int{}
	for _, e := range endpoints {
		if at, ok := index[e.Addr]; ok {
			listeners[at].Paths = append(listeners[at].Paths, e.Path)
			listeners[at].URLs = append(listeners[at].URLs, e.URL)
			continue
		}
		index[e.Addr] = len(listeners)
		listeners = append(listeners, Listener{
			Addr:  e.Addr,
			Paths: []string{e.Path},
			URLs:  []string{e.URL},
			TLS:   e.TLS,
		})
	}
	return listeners, nil
}

// Listener is one socket and everything it serves.
type Listener struct {
	Addr  string
	Paths []string
	URLs  []string
	TLS   bool
}

// urlHost returns the hostname of a URL, or "" if it cannot be parsed.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
