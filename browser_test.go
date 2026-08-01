package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The end-to-end tests drive a real headless browser. They are skipped under
// -short and when PUPPETEER_MCP_E2E is not set, because they need a Chrome or
// a Playwright-installed Chromium on the machine, which not every checkout has.
//
// Run them with:
//
//	PUPPETEER_MCP_E2E=1 go test -run E2E -v
const e2ePage = `<!doctype html>
<title>Fixture</title>
<h1>Fixture page</h1>
<button id="go">Click me</button>
<input id="name" placeholder="Your name">
<p id="out">nothing yet</p>
<script>
  console.log('page loaded');
  document.getElementById('go').onclick = () => {
    document.getElementById('out').textContent = 'clicked: ' + document.getElementById('name').value;
    console.warn('button was clicked');
  };
</script>`

func TestE2EBrowserFlow(t *testing.T) {
	srv, page := startE2E(t)
	ctx := context.Background()

	// The snapshot is the tool the whole design leans on, so the test walks the
	// way a model is meant to: snapshot, act on the refs it hands back, read
	// what the page reported.
	snapshot := callTool(t, srv, ctx, "browser_navigate", map[string]any{"url": page})
	if !strings.Contains(snapshot, "Fixture") {
		t.Fatalf("navigate did not reach the fixture:\n%s", snapshot)
	}

	outline := callTool(t, srv, ctx, "browser_snapshot", nil)
	for _, want := range []string{`heading 1 "Fixture page"`, `button "Click me"`, `textbox "Your name"`} {
		if !strings.Contains(outline, want) {
			t.Errorf("snapshot is missing %q:\n%s", want, outline)
		}
	}
	buttonRef := refFor(t, outline, `button "Click me"`)
	inputRef := refFor(t, outline, `textbox "Your name"`)

	callTool(t, srv, ctx, "browser_type", map[string]any{"ref": inputRef, "text": "Chris"})
	callTool(t, srv, ctx, "browser_click", map[string]any{"ref": buttonRef})

	if text := callTool(t, srv, ctx, "browser_get_text", map[string]any{"selector": "#out"}); text != "clicked: Chris" {
		t.Errorf("page text = %q, want \"clicked: Chris\"", text)
	}

	console := callTool(t, srv, ctx, "browser_console", nil)
	for _, want := range []string{"page loaded", "button was clicked"} {
		if !strings.Contains(console, want) {
			t.Errorf("console is missing %q:\n%s", want, console)
		}
	}

	value := callTool(t, srv, ctx, "browser_evaluate", map[string]any{
		"expression": "() => document.title",
	})
	if !strings.Contains(value, "Fixture") {
		t.Errorf("evaluate returned %q", value)
	}
}

func TestE2EStaleRefIsExplained(t *testing.T) {
	srv, page := startE2E(t)
	ctx := context.Background()
	callTool(t, srv, ctx, "browser_navigate", map[string]any{"url": page})
	outline := callTool(t, srv, ctx, "browser_snapshot", nil)
	ref := refFor(t, outline, `button "Click me"`)

	// Replacing the body drops every stamped ref. The model has to be told
	// that plainly, and told what to do about it, or it retries the same ref.
	callTool(t, srv, ctx, "browser_evaluate", map[string]any{
		"expression": "() => { document.body.innerHTML = '<p>gone</p>'; return true; }",
	})
	msg := callTool(t, srv, ctx, "browser_click", map[string]any{"ref": ref})
	if !strings.Contains(msg, "not on the page any more") || !strings.Contains(msg, "browser_snapshot") {
		t.Errorf("a stale ref was not explained usefully:\n%s", msg)
	}
}

func TestE2ENetworkFailureIsRecorded(t *testing.T) {
	srv, page := startE2E(t)
	ctx := context.Background()
	callTool(t, srv, ctx, "browser_navigate", map[string]any{"url": page})
	callTool(t, srv, ctx, "browser_evaluate", map[string]any{
		"expression": "() => { fetch('http://127.0.0.1:9/nothing').catch(() => {}); return true; }",
	})
	callTool(t, srv, ctx, "browser_wait_for", map[string]any{"delay_ms": 500})

	network := callTool(t, srv, ctx, "browser_network", map[string]any{"failures_only": true})
	if !strings.Contains(network, "127.0.0.1:9") {
		t.Errorf("the failed request was not recorded:\n%s", network)
	}
}

// startE2E writes the fixture page and returns a server with a headless
// browser ready to drive it.
func startE2E(t *testing.T) (*Server, string) {
	t.Helper()
	if testing.Short() || os.Getenv("PUPPETEER_MCP_E2E") == "" {
		t.Skip("set PUPPETEER_MCP_E2E=1 to run the tests that drive a real browser")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.html")
	if err := os.WriteFile(path, []byte(e2ePage), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.Browser.Headless = true
	cfg.Browser.ProfileDir = filepath.Join(dir, "profile")
	srv := newTestServer(t, cfg)
	t.Cleanup(srv.browser.Close)

	return srv, "file:///" + filepath.ToSlash(path)
}

// callTool invokes a tool the way a client would and returns its text content,
// failing the test on a protocol error.
func callTool(t *testing.T, srv *Server, ctx context.Context, name string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := srv.tools[name]
	if !ok {
		t.Fatalf("no tool named %q", name)
	}
	result, rpcErr := tool.handler(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("%s: %v", name, rpcErr)
	}
	if len(result.Content) == 0 {
		return ""
	}
	return result.Content[0].Text
}

// refFor pulls the ref out of the snapshot line describing an element, so the
// tests act on what the page actually handed back rather than a guess.
func refFor(t *testing.T, snapshot, contains string) string {
	t.Helper()
	for _, line := range strings.Split(snapshot, "\n") {
		if !strings.Contains(line, contains) {
			continue
		}
		if _, after, found := strings.Cut(line, "ref="); found {
			return strings.Fields(after)[0]
		}
	}
	t.Fatalf("no ref for %q in:\n%s", contains, snapshot)
	return ""
}

func TestE2EHandoffBannerRoundTrip(t *testing.T) {
	srv, page := startE2E(t)
	ctx := context.Background()
	callTool(t, srv, ctx, "browser_navigate", map[string]any{"url": page})

	st, err := srv.browser.CurrentPage()
	if err != nil {
		t.Fatal(err)
	}

	// The banner is what a person actually sees and clicks, so the test drives
	// it the way they would: put it up, find the button by its label, click it,
	// and check the flag the wait loop reads.
	if _, err := st.Page.Evaluate(handoffJS, map[string]any{
		"message": "Sign in, then press Continue", "buttonLabel": "Continue",
	}); err != nil {
		t.Fatalf("showing the banner: %v", err)
	}
	outline := callTool(t, srv, ctx, "browser_snapshot", nil)
	if !strings.Contains(outline, "Sign in, then press Continue") {
		t.Errorf("the request is not visible on the page:\n%s", outline)
	}

	if v, _ := st.Page.Evaluate(pollHandoffJS); v != nil {
		t.Errorf("the wait ended before anyone answered: %v", v)
	}
	callTool(t, srv, ctx, "browser_click", map[string]any{"selector": "#__pmcp_handoff button >> nth=0"})

	value, err := st.Page.Evaluate(pollHandoffJS)
	if err != nil {
		t.Fatal(err)
	}
	if answer, _ := value.(string); answer != "continue" {
		t.Errorf("after the click the flag is %q, want \"continue\"", answer)
	}

	// And it takes itself down, so a screenshot afterwards shows the page.
	if _, err := st.Page.Evaluate(clearHandoffJS); err != nil {
		t.Fatal(err)
	}
	after := callTool(t, srv, ctx, "browser_snapshot", nil)
	if strings.Contains(after, "Sign in, then press Continue") {
		t.Error("the banner outlived the wait")
	}
}

func TestE2ECancelIsReportedAsAnError(t *testing.T) {
	srv, page := startE2E(t)
	ctx := context.Background()
	callTool(t, srv, ctx, "browser_navigate", map[string]any{"url": page})
	st, _ := srv.browser.CurrentPage()

	if _, err := st.Page.Evaluate(handoffJS, map[string]any{
		"message": "Look at this", "buttonLabel": "Continue",
	}); err != nil {
		t.Fatal(err)
	}
	callTool(t, srv, ctx, "browser_click", map[string]any{"selector": "#__pmcp_handoff button >> nth=1"})

	// Cancel has to reach the model as a failure. A person who cancels is
	// saying stop, and a success result would have it carry on regardless.
	outcome, _ := srv.awaitHandoff(st, st.Page.URL(), false, 3*time.Second)
	if outcome != "cancelled" {
		t.Errorf("outcome = %q, want cancelled", outcome)
	}
}

func TestE2EStartURLOpensTheBrowserSomewhereUseful(t *testing.T) {
	srv, page := startE2E(t)

	// Interactive mode brings the browser up at startup rather than on the
	// first tool call, and start_url is what stops that being a blank tab.
	if srv.browser.Running() {
		t.Fatal("the fixture started a browser before the test asked for one")
	}
	if err := srv.browser.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	openStartURL(srv, page, log.New(io.Discard, "", 0))

	st, err := srv.browser.CurrentPage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Page.URL(), "fixture.html") {
		t.Errorf("browser opened at %q, want the start URL", st.Page.URL())
	}
	title, _ := st.Page.Title()
	if title != "Fixture" {
		t.Errorf("title = %q, want the start page to have loaded", title)
	}
}
