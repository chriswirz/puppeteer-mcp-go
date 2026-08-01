package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigNormalizeAppliesDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Normalize(t.TempDir()); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Server.Transport != "stdio" {
		t.Errorf("transport = %q, want stdio", cfg.Server.Transport)
	}
	if cfg.Browser.Channel != "chrome" {
		t.Errorf("channel = %q, want chrome: real Chrome is what an extension will run in", cfg.Browser.Channel)
	}
	if cfg.Browser.DefaultTimeoutMs != 15000 || cfg.Browser.NavigationTimeoutMs != 45000 {
		t.Errorf("timeouts = %d/%d, want 15000/45000", cfg.Browser.DefaultTimeoutMs, cfg.Browser.NavigationTimeoutMs)
	}
	if !filepath.IsAbs(cfg.Browser.ProfileDir) {
		t.Errorf("profile dir %q was not made absolute", cfg.Browser.ProfileDir)
	}
	if cfg.Server.Instructions == "" {
		t.Error("instructions are empty; a client would be told nothing about how to use this server")
	}
}

func TestConfigExtensionsForceHeaded(t *testing.T) {
	dir := t.TempDir()
	ext := filepath.Join(dir, "ext")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "manifest.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Browser.Headless = true
	cfg.Browser.Extensions = []string{ext}
	if err := cfg.Normalize(dir); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	// Chrome will not load an unpacked extension with no window, so the two
	// settings cannot both be honoured and the extension has to win.
	if cfg.Browser.Headless {
		t.Error("headless stayed on with an extension loaded; Chrome would silently not load it")
	}
}

func TestConfigRejectsExtensionWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Browser.Extensions = []string{dir}
	err := cfg.Normalize(dir)
	if err == nil || !strings.Contains(err.Error(), "manifest.json") {
		t.Fatalf("Normalize error = %v, want one naming manifest.json", err)
	}
}

func TestConfigRejectsTunnelOnStdio(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tunnel.Enabled = true
	cfg.Tunnel.ServerURL = "https://tunnel.example.com"
	cfg.Tunnel.APIKey = "k"
	err := cfg.Normalize(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "transport") {
		t.Fatalf("Normalize error = %v, want one explaining the tunnel needs the http transport", err)
	}
}

func TestCheckNavigable(t *testing.T) {
	srv := &Server{}
	srv.cfg.Browser.AllowNavigateHosts = []string{"example.com"}

	for _, allowed := range []string{
		"https://example.com/path",
		"https://app.example.com/",
		"about:blank",
	} {
		if bad := srv.checkNavigable(allowed); bad != nil {
			t.Errorf("checkNavigable(%q) refused: %s", allowed, bad.Content[0].Text)
		}
	}
	if bad := srv.checkNavigable("https://evil.test/"); bad == nil {
		t.Error("checkNavigable allowed a host outside the allowlist")
	}
	// A host that merely ends with the allowed name must not pass: matching
	// "notexample.com" against "example.com" is the classic suffix bug.
	if bad := srv.checkNavigable("https://notexample.com/"); bad == nil {
		t.Error("checkNavigable allowed notexample.com against an example.com allowlist")
	}

	empty := &Server{}
	if bad := empty.checkNavigable("https://anywhere.test/"); bad != nil {
		t.Error("an empty allowlist should allow everything")
	}
}

func TestRenderSnapshot(t *testing.T) {
	out := renderSnapshot([]frameSnapshot{{
		Frame: "main",
		URL:   "https://example.test/",
		Title: "Demo",
		Nodes: []snapshotNode{
			{Ref: "s1e1", Depth: 0, Tag: "h1", Role: "heading", Name: "Hello", Level: 1},
			{Ref: "s1e2", Depth: 1, Tag: "input", Role: "textbox", Name: "Email", Value: "a@b.c", State: []string{"required"}},
		},
	}})
	for _, want := range []string{"# Demo - https://example.test/", `heading 1 "Hello"`, "ref=s1e1", `value="a@b.c"`, "[required]"} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot output is missing %q:\n%s", want, out)
		}
	}
}

func TestTargetRequiresExactlyOneWayToName(t *testing.T) {
	if _, err := (target{}).resolve(&PageState{}); err == nil {
		t.Error("a target naming nothing was accepted")
	}
	if _, err := (target{Ref: "e1", Selector: "#a"}).resolve(&PageState{}); err == nil {
		t.Error("a target naming both a ref and a selector was accepted")
	}
}

func TestExtensionIDFromURL(t *testing.T) {
	cases := map[string]string{
		"chrome-extension://abcdefghijklmnop/background.js": "abcdefghijklmnop",
		"chrome-extension://abcdefghijklmnop":               "abcdefghijklmnop",
		"https://example.com/":                              "",
		"":                                                  "",
	}
	for input, want := range cases {
		if got := extensionIDFromURL(input); got != want {
			t.Errorf("extensionIDFromURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAppendRingKeepsTheNewest(t *testing.T) {
	var buf []int
	for i := range 10 {
		buf = appendRing(buf, i, 3)
	}
	if len(buf) != 3 || buf[0] != 7 || buf[2] != 9 {
		t.Errorf("ring = %v, want the last three entries [7 8 9]", buf)
	}
}

func TestPageStateSinceIsScopedToTheMark(t *testing.T) {
	st := &PageState{capacity: 10}
	st.errors = append(st.errors, ErrorEntry{Error: "before"})
	mark := st.Mark()
	st.errors = append(st.errors, ErrorEntry{Error: "after"})

	errs, _ := st.Since(mark)
	if len(errs) != 1 || errs[0].Error != "after" {
		t.Fatalf("Since returned %v, want only the entry added after the mark", errs)
	}
	// This is the point of the mark: a failure from page load must not be
	// re-reported as though the click that followed had caused it.
	if trouble := troubleSince(st, mark); !strings.Contains(trouble, "after") || strings.Contains(trouble, "before") {
		t.Errorf("troubleSince = %q, want only what happened after the mark", trouble)
	}
}

func TestLevelMatches(t *testing.T) {
	if !levelMatches("error", "error") || !levelMatches("error", "assert") {
		t.Error("an assert should count as an error")
	}
	if levelMatches("error", "log") {
		t.Error("a log message counted as an error")
	}
	if !levelMatches("warning", "warn") {
		t.Error("warn and warning should be the same filter")
	}
}

func TestToolsAreRegisteredAndDescribed(t *testing.T) {
	srv := newTestServer(t, DefaultConfig())
	names := srv.ToolNames()
	if len(names) < 30 {
		t.Fatalf("only %d tools registered", len(names))
	}
	for _, name := range names {
		tool := srv.tools[name]
		if !strings.HasPrefix(name, "browser_") {
			t.Errorf("tool %q does not carry the browser_ prefix", name)
		}
		// A tool a model cannot tell apart from its neighbours is a tool it
		// will call at random, so the description is not optional here.
		if len(tool.def.Description) < 40 {
			t.Errorf("tool %q has a thin description: %q", name, tool.def.Description)
		}
		if tool.def.InputSchema == nil {
			t.Errorf("tool %q has no input schema", name)
		}
	}
}

func TestEvalToolsAreWithheldWhenDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Browser.AllowEval = false
	srv := newTestServer(t, cfg)
	for _, name := range []string{"browser_evaluate", "browser_cdp", "browser_route", "browser_add_init_script"} {
		if _, ok := srv.tools[name]; ok {
			t.Errorf("%s was registered even though browser.allow_eval is false", name)
		}
	}
	if _, ok := srv.tools["browser_click"]; !ok {
		t.Error("disabling eval should not withdraw the ordinary tools")
	}
}

func TestPromptsRender(t *testing.T) {
	srv := newTestServer(t, DefaultConfig())
	ctx := withVersion(context.Background(), ProtocolVersion)

	listed, ok := srv.listPrompts(ctx).(*ListPromptsResult)
	if !ok || len(listed.Prompts) == 0 {
		t.Fatal("no prompts listed")
	}
	req := &Request{Method: "prompts/get", Params: json.RawMessage(
		`{"name":"debug_page","arguments":{"url":"https://example.test/","symptom":"blank page"}}`)}
	result, rpcErr := srv.getPrompt(ctx, req)
	if rpcErr != nil {
		t.Fatalf("getPrompt: %v", rpcErr)
	}
	text := result.(*GetPromptResult).Messages[0].Content.Text
	if !strings.Contains(text, "https://example.test/") || !strings.Contains(text, "blank page") {
		t.Errorf("the prompt did not carry its arguments through:\n%s", text)
	}

	// A required argument that is missing has to be refused, or the model gets
	// a prompt with a hole in it and improvises.
	bare := &Request{Method: "prompts/get", Params: json.RawMessage(`{"name":"debug_page"}`)}
	if _, rpcErr := srv.getPrompt(ctx, bare); rpcErr == nil {
		t.Error("a prompt missing its required argument was accepted")
	}
}

// newTestServer builds a server with the tools registered but no browser
// started - which is the normal state until a tool needs one.
func newTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	if err := cfg.Normalize(t.TempDir()); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	srv := NewServer(cfg.Server.Name, version, cfg.Server.Instructions, true)
	srv.cfg = cfg
	srv.browser = NewBrowser(cfg.Browser, log.New(os.Stderr, "test: ", 0))
	srv.registerPageTools()
	srv.registerNavigationTools()
	srv.registerInteractionTools()
	srv.registerInspectionTools()
	srv.registerDebugTools()
	srv.registerEvalTools()
	srv.registerExtensionTools()
	srv.registerInteractiveTools()
	return srv
}

func TestSaveSessionIDPersistsIntoConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(ExampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// Prove the rest of the file survives the write, not just that the id lands.
	cfg.Browser.Channel = ""
	if err := cfg.SaveSessionID("sess-abc123"); err != nil {
		t.Fatalf("SaveSessionID: %v", err)
	}
	if cfg.Tunnel.SessionID != "sess-abc123" {
		t.Errorf("in-memory session id = %q", cfg.Tunnel.SessionID)
	}

	reloaded, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Tunnel.SessionID != "sess-abc123" {
		t.Errorf("persisted session id = %q, want sess-abc123", reloaded.Tunnel.SessionID)
	}
	// Untouched keys must come back exactly as they were, in every section.
	if reloaded.Browser.Channel != "chrome" || reloaded.Server.URL != "http://127.0.0.1:8770/mcp" {
		t.Errorf("the rewrite disturbed other settings: channel=%q url=%q",
			reloaded.Browser.Channel, reloaded.Server.URL)
	}
	if reloaded.Tunnel.APIKeyEnv != "TUNNEL_API_KEY" {
		t.Errorf("the rewrite disturbed a sibling key in the same section: %q", reloaded.Tunnel.APIKeyEnv)
	}

	// Writing the same id again is a no-op, so a reconnect does not churn the file.
	before, _ := os.ReadFile(path)
	if err := cfg.SaveSessionID("sess-abc123"); err != nil {
		t.Fatalf("second SaveSessionID: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("re-saving the same session id rewrote the file")
	}
}

func TestSaveSessionIDWithoutConfigFile(t *testing.T) {
	// The common shape: started from flags alone, with no config.json. There is
	// nowhere to keep the id, and that has to be reported rather than swallowed
	// or treated as a tunnel failure.
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "config.json"), false)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.SaveSessionID("sess-xyz"); !errors.Is(err, ErrNoConfigFile) {
		t.Fatalf("SaveSessionID error = %v, want ErrNoConfigFile", err)
	}
	if cfg.Tunnel.SessionID != "sess-xyz" {
		t.Error("the id should still be held in memory for the life of the process")
	}
}

func TestSessionFileTakesOverFromTheConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(ExampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.Tunnel.SessionFile = filepath.Join(dir, "session")
	quiet := log.New(io.Discard, "", 0)

	before, _ := os.ReadFile(path)
	if err := sessionSaver(&cfg, quiet)("sess-from-file"); err != nil {
		t.Fatalf("sessionSaver: %v", err)
	}

	// The file is the store, so the config must come back byte for byte.
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("config.json was rewritten even though a session file was named")
	}
	saved, err := os.ReadFile(cfg.Tunnel.SessionFile)
	if err != nil {
		t.Fatalf("session file: %v", err)
	}
	if strings.TrimSpace(string(saved)) != "sess-from-file" {
		t.Errorf("session file holds %q", strings.TrimSpace(string(saved)))
	}

	// On restart the file is read, since nothing was written to the config.
	if got := tunnelSession(cfg.Tunnel); got != "sess-from-file" {
		t.Errorf("tunnelSession = %q, want the id from the file", got)
	}
	// A session id written into the config by hand still overrides it: that is
	// an operator naming the session to resume, not an automatic store.
	cfg.Tunnel.SessionID = "explicitly-configured"
	if got := tunnelSession(cfg.Tunnel); got != "explicitly-configured" {
		t.Errorf("tunnelSession = %q, want the explicitly configured id", got)
	}
}

func TestConfigIsUsedWhenNoSessionFileIsNamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(ExampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.Tunnel.SessionFile = ""
	if err := sessionSaver(&cfg, log.New(io.Discard, "", 0))("sess-in-config"); err != nil {
		t.Fatalf("sessionSaver: %v", err)
	}
	reloaded, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Tunnel.SessionID != "sess-in-config" {
		t.Errorf("persisted session id = %q, want sess-in-config", reloaded.Tunnel.SessionID)
	}
}

func TestInteractiveForcesAVisibleBrowserThatFollowsFocus(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Browser.Interactive = true
	cfg.Browser.Headless = true // a contradiction the config has to resolve
	if err := cfg.Normalize(t.TempDir()); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Browser.Headless {
		t.Error("interactive mode left the browser headless; nobody can take turns with a window they cannot see")
	}
	if !cfg.Browser.FollowsActiveTab() {
		t.Error("interactive mode should follow the tab that has focus")
	}

	// Outside interactive mode nothing changes: the browser is the model's own,
	// and chasing focus would be a round trip per tab for no reason.
	plain := DefaultConfig()
	if err := plain.Normalize(t.TempDir()); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if plain.Browser.FollowsActiveTab() {
		t.Error("focus following should be off by default")
	}

	// An explicit false is honoured even in interactive mode.
	off := DefaultConfig()
	off.Browser.Interactive = true
	off.Browser.FollowActiveTab = boolPtr(false)
	if err := off.Normalize(t.TempDir()); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if off.Browser.FollowsActiveTab() {
		t.Error("an explicit follow_active_tab false was overridden")
	}
}

func TestWaitForUserRefusesAHeadlessBrowser(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Browser.Headless = true
	srv := newTestServer(t, cfg)

	// Must not start a browser or block: there is nobody to answer, and
	// burning the full timeout before failing would be the worst outcome.
	done := make(chan *CallToolResult, 1)
	go func() {
		res, _ := srv.tools["browser_wait_for_user"].handler(context.Background(),
			json.RawMessage(`{"message":"sign in","timeout_ms":600000}`))
		done <- res
	}()
	select {
	case res := <-done:
		if !res.IsError || !strings.Contains(res.Content[0].Text, "headless") {
			t.Errorf("unexpected result: %+v", res.Content[0].Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("browser_wait_for_user blocked on a headless browser instead of saying nobody is there")
	}
	if srv.browser.Running() {
		t.Error("it started a browser just to refuse")
	}
}

func TestInteractiveIsSettableFromConfigAndFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(
		`{"server":{"transport":"stdio"},"browser":{"interactive":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// From the file alone.
	cfg, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.Normalize(dir); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !cfg.Browser.Interactive || !cfg.Browser.FollowsActiveTab() {
		t.Error("browser.interactive in config.json did not take effect")
	}

	// From flags alone, with no config file at all.
	opts, err := parseFlags([]string{"--interactive"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	bare := DefaultConfig()
	applyOptions(&bare, opts)
	if err := bare.Normalize(dir); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !bare.Browser.Interactive || !bare.Browser.FollowsActiveTab() {
		t.Error("--interactive did not take effect")
	}

	// And a flag overrides the file, in both directions.
	off, err := parseFlags([]string{"--no-follow-active-tab"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	fromFile, _ := LoadConfig(path, true)
	applyOptions(&fromFile, off)
	if err := fromFile.Normalize(dir); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if fromFile.Browser.FollowsActiveTab() {
		t.Error("--no-follow-active-tab did not override the config")
	}

	if _, err := parseFlags([]string{"--follow-active-tab", "--no-follow-active-tab"}); err == nil {
		t.Error("contradictory follow flags were accepted")
	}
	if _, err := parseFlags([]string{"--interactive", "--headless"}); err == nil {
		t.Error("--interactive with --headless was accepted")
	}
}

// TestScreenshotServing covers the optional file subtree: it is off unless a
// path is configured, it is open to any origin, it honours the bearer token,
// and it lists the directory only when told to.
func TestScreenshotServing(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Server.Transport = "http"
	cfg.Server.AuthToken = "secret"
	cfg.Server.ScreenshotPath = "shots" // normalized to /shots/
	if err := cfg.Normalize(dir); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Server.ScreenshotPath != "/shots/" {
		t.Fatalf("screenshot_path = %q, want /shots/", cfg.Server.ScreenshotPath)
	}
	if err := os.MkdirAll(cfg.Browser.ScreenshotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Browser.ScreenshotDir, "a.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(cfg.Server.Name, version, cfg.Server.Instructions, true)
	srv.cfg = cfg
	get := func(h http.Handler, method, target, auth, origin string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	h := NewHTTPTransport(srv, cfg.Server, "/mcp", log.New(io.Discard, "", 0)).HandlerFor("/mcp")

	// An origin the MCP endpoint would refuse still gets the image, with the
	// wildcard that lets a page actually read it.
	rec := get(h, "GET", "/shots/a.png", "Bearer secret", "https://evil.example")
	if rec.Code != http.StatusOK || rec.Body.String() != "png" {
		t.Fatalf("GET /shots/a.png = %d %q, want 200 png", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	// A preflight carries no credentials, so it must pass the token check.
	if rec := get(h, "OPTIONS", "/shots/a.png", "", "https://evil.example"); rec.Code != http.StatusNoContent {
		t.Errorf("preflight = %d, want 204", rec.Code)
	}
	if rec := get(h, "GET", "/shots/a.png", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET = %d, want 401", rec.Code)
	}
	if rec := get(h, "GET", "/shots/a.png?token=secret", "", ""); rec.Code != http.StatusOK {
		t.Errorf("token in the query = %d, want 200: an <img> cannot set a header", rec.Code)
	}
	if rec := get(h, "POST", "/shots/a.png", "Bearer secret", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
	// Listing is off by default, so the directory itself is not browsable.
	if rec := get(h, "GET", "/shots/", "Bearer secret", ""); rec.Code != http.StatusNotFound {
		t.Errorf("directory index with listing off = %d, want 404", rec.Code)
	}

	listing := cfg.Server
	listing.ScreenshotListing = true
	hl := NewHTTPTransport(srv, listing, "/mcp", log.New(io.Discard, "", 0)).HandlerFor("/mcp")
	rec = get(hl, "GET", "/shots/", "Bearer secret", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "a.png") {
		t.Errorf("directory index with listing on = %d %q, want 200 naming a.png", rec.Code, rec.Body.String())
	}

	// Unconfigured, nothing is served and the MCP 404 answers instead.
	off := cfg.Server
	off.ScreenshotPath = ""
	ho := NewHTTPTransport(srv, off, "/mcp", log.New(io.Discard, "", 0)).HandlerFor("/mcp")
	if rec := get(ho, "GET", "/shots/a.png", "Bearer secret", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET with serving off = %d, want 404", rec.Code)
	}

	if got := cfg.ScreenshotURL(filepath.Join(cfg.Browser.ScreenshotDir, "a.png")); got != "http://127.0.0.1:8770/shots/a.png" {
		t.Errorf("ScreenshotURL = %q, want http://127.0.0.1:8770/shots/a.png", got)
	}
	if got := cfg.ScreenshotURL(filepath.Join(dir, "elsewhere", "a.png")); got != "" {
		t.Errorf("ScreenshotURL outside the directory = %q, want empty", got)
	}
}

func TestScreenshotServingRejectsStdioAndRoot(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Server.ScreenshotPath = "/shots/"
	if err := cfg.Normalize(t.TempDir()); err == nil || !strings.Contains(err.Error(), "transport") {
		t.Fatalf("Normalize on stdio = %v, want an error naming the transport", err)
	}
	cfg = DefaultConfig()
	cfg.Server.Transport = "http"
	cfg.Server.ScreenshotPath = "/"
	if err := cfg.Normalize(t.TempDir()); err == nil {
		t.Fatal("screenshot_path \"/\" was accepted; it would swallow the whole server")
	}
}
