package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
)

// The handoff tools exist because the interesting workflows are not "the model
// drives" or "the person drives" but the two taking turns: sign in here, then
// I will take it from there; open the page you are worried about and tell me
// when; I have set this up, look at it before I continue.
//
// Without a way to wait, the model's only options are to guess how long to
// sleep or to charge ahead through a login screen it cannot pass.

// handoffJS puts a banner on the page saying what is being waited for, with a
// button that ends the wait. It returns nothing; the flag it sets is read back
// by pollHandoffJS.
//
// The banner is deliberately loud and fixed to the top: a person who has been
// asked for something needs to see the ask on the page they are looking at,
// not in a transcript in another window.
const handoffJS = `(opts) => {
  const ID = '__pmcp_handoff';
  document.getElementById(ID)?.remove();
  window.__pmcpHandoff = null;

  const bar = document.createElement('div');
  bar.id = ID;
  bar.style.cssText = [
    'position:fixed', 'top:0', 'left:0', 'right:0', 'z-index:2147483647',
    'background:#1f2937', 'color:#f9fafb', 'padding:12px 16px',
    'font:14px/1.5 system-ui,-apple-system,Segoe UI,sans-serif',
    'display:flex', 'align-items:center', 'gap:12px',
    'box-shadow:0 2px 12px rgba(0,0,0,.35)',
  ].join(';');

  const text = document.createElement('span');
  text.style.cssText = 'flex:1';
  text.textContent = opts.message;

  const go = document.createElement('button');
  go.textContent = opts.buttonLabel;
  go.style.cssText = [
    'background:#2563eb', 'color:#fff', 'border:0', 'border-radius:6px',
    'padding:7px 16px', 'font:inherit', 'font-weight:600', 'cursor:pointer',
  ].join(';');
  go.onclick = () => { window.__pmcpHandoff = 'continue'; bar.remove(); };

  const stop = document.createElement('button');
  stop.textContent = 'Cancel';
  stop.style.cssText = [
    'background:transparent', 'color:#d1d5db', 'border:1px solid #4b5563',
    'border-radius:6px', 'padding:7px 14px', 'font:inherit', 'cursor:pointer',
  ].join(';');
  stop.onclick = () => { window.__pmcpHandoff = 'cancel'; bar.remove(); };

  bar.append(text, go, stop);
  (document.body || document.documentElement).appendChild(bar);
  return true;
}`

// pollHandoffJS reads the flag the banner sets. It is a separate evaluation so
// that a page which navigated mid-wait simply reports nothing rather than
// throwing: the navigation itself is the signal in that case.
const pollHandoffJS = `() => window.__pmcpHandoff || null`

// clearHandoffJS takes the banner down, so a screenshot taken afterwards shows
// the page rather than this server's furniture.
const clearHandoffJS = `() => { document.getElementById('__pmcp_handoff')?.remove(); window.__pmcpHandoff = null; return true; }`

// registerInteractiveTools adds the tools for taking turns with a person.
func (s *Server) registerInteractiveTools() {
	s.RegisterTool(Tool{
		Name:  "browser_wait_for_user",
		Title: "Hand the browser back and wait",
		Description: "Put a message on the page and wait for the person at the browser to act on it: to sign in, " +
			"to navigate somewhere, to look at what you have set up, or simply to say carry on.\n\n" +
			"This is the tool for anything you cannot or should not do yourself - a login, a two-factor prompt, " +
			"a human-verification check, a decision that is not yours to make. Use it instead of guessing at a " +
			"delay, and instead of driving through a screen you cannot pass.\n\n" +
			"The wait ends when they press Continue, when the page navigates (which is what happens if you asked " +
			"them to go somewhere), or on timeout. It reports which, and where the browser ended up. Take a fresh " +
			"browser_snapshot afterwards: the page is very unlikely to be the one you left.\n\n" +
			"Only meaningful with a visible browser. In headless mode there is nobody to ask, and this returns " +
			"immediately saying so rather than blocking for the full timeout.",
		InputSchema: schema([]string{"message"}, map[string]any{
			"message": prop("string", "What you need from them, in a sentence, as it will appear on the page. "+
				"\"Sign in to your account, then press Continue\" - not \"waiting\"."),
			"button_label": propDefault("string", "Label for the button that ends the wait.", "Continue"),
			"timeout_ms":   propDefault("integer", "How long to wait before giving up.", 300000),
			"until_navigate": propDefault("boolean",
				"End the wait as soon as the page navigates, without waiting for the button. Right when you have "+
					"asked them to go somewhere; wrong when you have asked them to look at this page.", true),
			"page_id": prop("string", "Page to put the banner on. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Message       string `json:"message"`
			ButtonLabel   string `json:"button_label"`
			TimeoutMs     int    `json:"timeout_ms"`
			UntilNavigate *bool  `json:"until_navigate"`
			PageID        string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if strings.TrimSpace(args.Message) == "" {
			return toolError("message is required: say what you need from the person at the browser"), nil
		}
		// Nobody is watching a headless browser, so blocking would only burn
		// the timeout and then fail. Saying so immediately is the honest answer.
		if s.cfg.Browser.Headless && s.cfg.Browser.CDPURL == "" {
			return toolError("this browser is headless, so there is nobody at it to answer. " +
				"Start the server with --interactive (or browser.headless false) for a browser a person can use"), nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}

		timeout := 300000
		if args.TimeoutMs > 0 {
			timeout = args.TimeoutMs
		}
		label := orDefault(args.ButtonLabel, "Continue")
		untilNavigate := args.UntilNavigate == nil || *args.UntilNavigate

		startURL := st.Page.URL()
		if _, err := st.Page.Evaluate(handoffJS, map[string]any{
			"message": args.Message, "buttonLabel": label,
		}); err != nil {
			return toolError("could not show the request on the page: %v", err), nil
		}

		outcome, waited := s.awaitHandoff(st, startURL, untilNavigate, time.Duration(timeout)*time.Millisecond)
		st.Page.Evaluate(clearHandoffJS)

		result := map[string]any{
			"outcome":     outcome,
			"waited_ms":   waited.Milliseconds(),
			"page_id":     st.ID,
			"url":         st.Page.URL(),
			"url_changed": st.Page.URL() != startURL,
		}
		if title, err := st.Page.Title(); err == nil {
			result["title"] = title
		}
		switch outcome {
		case "cancelled":
			return &CallToolResult{
				Content:           textContent("They pressed Cancel. Stop and ask what they want instead of continuing."),
				StructuredContent: result,
				IsError:           true,
			}, nil
		case "timeout":
			return &CallToolResult{
				Content: textContent(fmt.Sprintf(
					"Nobody answered within %ds. The browser is at %s. They may be away, or looking at a "+
						"different window - say what you are waiting for before trying again.",
					timeout/1000, st.Page.URL())),
				StructuredContent: result,
				IsError:           true,
			}, nil
		}
		return toolResultJSON(result), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_notify",
		Title: "Put a message on the page",
		Description: "Show a message on the page without waiting for anything. Use it to say what you are about " +
			"to do, or what you just found, so the person watching the browser can follow along instead of " +
			"reading a transcript in another window. It disappears on its own.",
		InputSchema: schema([]string{"message"}, map[string]any{
			"message":     prop("string", "The message to show."),
			"duration_ms": propDefault("integer", "How long it stays up.", 4000),
			"page_id":     prop("string", "Page to show it on. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Message    string `json:"message"`
			DurationMs int    `json:"duration_ms"`
			PageID     string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if strings.TrimSpace(args.Message) == "" {
			return toolError("message is required"), nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		duration := args.DurationMs
		if duration <= 0 {
			duration = 4000
		}
		if _, err := st.Page.Evaluate(notifyJS, map[string]any{
			"message": args.Message, "duration": duration,
		}); err != nil {
			return toolError("could not show the message: %v", err), nil
		}
		return toolResult("Shown on " + st.ID + "."), nil
	})
}

// awaitHandoff polls until the person answers, the page navigates, or the
// timeout expires.
//
// Polling rather than waiting on an event because both signals have to be
// watched at once, and because a navigation destroys the page context the
// button lives in - an event subscription would have to be rebuilt each time,
// where a poll simply notices the URL is different.
func (s *Server) awaitHandoff(st *PageState, startURL string, untilNavigate bool, timeout time.Duration) (string, time.Duration) {
	const interval = 300 * time.Millisecond
	began := time.Now()
	deadline := began.Add(timeout)

	for time.Now().Before(deadline) {
		time.Sleep(interval)

		if untilNavigate && st.Page.URL() != startURL {
			// The banner went with the old document; the navigation is the answer.
			return "navigated", time.Since(began)
		}
		value, err := st.Page.Evaluate(pollHandoffJS)
		if err != nil {
			continue // mid-navigation: the next tick sees the settled page
		}
		switch answer, _ := value.(string); answer {
		case "continue":
			return "continued", time.Since(began)
		case "cancel":
			return "cancelled", time.Since(began)
		}
		if st.Page.IsClosed() {
			return "page_closed", time.Since(began)
		}
	}
	return "timeout", time.Since(began)
}

// notifyJS shows a transient message and takes it away again.
const notifyJS = `(opts) => {
  const ID = '__pmcp_notify';
  document.getElementById(ID)?.remove();
  const box = document.createElement('div');
  box.id = ID;
  box.textContent = opts.message;
  box.style.cssText = [
    'position:fixed', 'bottom:20px', 'right:20px', 'z-index:2147483647',
    'max-width:420px', 'background:#1f2937', 'color:#f9fafb',
    'padding:12px 16px', 'border-radius:8px',
    'font:14px/1.5 system-ui,-apple-system,Segoe UI,sans-serif',
    'box-shadow:0 4px 16px rgba(0,0,0,.35)', 'transition:opacity .3s',
  ].join(';');
  (document.body || document.documentElement).appendChild(box);
  setTimeout(() => { box.style.opacity = '0'; setTimeout(() => box.remove(), 300); }, opts.duration);
  return true;
}`

// pageIsVisible reports whether a page is the one on screen, which is what
// browser_status uses to say which tab the person is looking at.
func pageIsVisible(p playwright.Page) bool {
	value, err := p.Evaluate(`() => document.visibilityState === 'visible' && document.hasFocus()`)
	if err != nil {
		return false
	}
	visible, _ := value.(bool)
	return visible
}
