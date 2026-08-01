package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// registerNavigationTools adds the tools that move a page around and wait for
// it to settle.
func (s *Server) registerNavigationTools() {
	s.RegisterTool(Tool{
		Name:  "browser_navigate",
		Title: "Go to a URL",
		Description: "Navigate the current page to a URL and report where it ended up, what it loaded with, and " +
			"anything the page logged on the way. A redirect or a failed load is reported rather than hidden, " +
			"because a page that is not where you think it is explains most of what goes wrong afterwards.",
		InputSchema: schema([]string{"url"}, map[string]any{
			"url": prop("string", "Absolute URL to open. A chrome-extension:// URL works too, which is how you "+
				"open your extension's own options or popup page."),
			"wait_until": map[string]any{
				"type": "string",
				"enum": []string{"load", "domcontentloaded", "networkidle", "commit"},
				"description": "How much loading to wait for. domcontentloaded (the default) is right for most " +
					"pages; networkidle suits a single-page app that fetches its content after load, but hangs " +
					"on a page holding a long-lived connection.",
				"default": "domcontentloaded",
			},
			"timeout_ms": prop("integer", "Override the navigation timeout for this call."),
			"page_id":    prop("string", "Page to navigate. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			URL       string `json:"url"`
			WaitUntil string `json:"wait_until"`
			TimeoutMs int    `json:"timeout_ms"`
			PageID    string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if args.URL == "" {
			return toolError("url is required"), nil
		}
		if bad := s.checkNavigable(args.URL); bad != nil {
			return bad, nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		mark := st.Mark()

		opts := playwright.PageGotoOptions{WaitUntil: waitUntilState(args.WaitUntil)}
		if args.TimeoutMs > 0 {
			opts.Timeout = playwright.Float(float64(args.TimeoutMs))
		}
		resp, err := st.Page.Goto(args.URL, opts)
		if err != nil {
			return toolError("navigating to %s: %v\n\n%s", args.URL, err, troubleSince(st, mark)), nil
		}
		result := map[string]any{
			"page_id":   st.ID,
			"url":       st.Page.URL(),
			"requested": args.URL,
		}
		if title, err := st.Page.Title(); err == nil {
			result["title"] = title
		}
		if resp != nil {
			result["status"] = resp.Status()
			if resp.URL() != args.URL {
				result["redirected_to"] = resp.URL()
			}
			if resp.Status() >= 400 {
				result["note"] = fmt.Sprintf("the server answered %d %s", resp.Status(), resp.StatusText())
			}
		}
		if trouble := troubleSince(st, mark); trouble != "" {
			result["page_reported"] = trouble
		}
		return toolResultJSON(result), nil
	})

	s.RegisterTool(Tool{
		Name:        "browser_back",
		Title:       "Go back",
		Description: "Go back one entry in the page's history.",
		InputSchema: schema(nil, map[string]any{
			"page_id": prop("string", "Page to act on. Omitted means the current page."),
		}),
	}, s.historyTool(func(p playwright.Page) error {
		_, err := p.GoBack()
		return err
	}, "back"))

	s.RegisterTool(Tool{
		Name:        "browser_forward",
		Title:       "Go forward",
		Description: "Go forward one entry in the page's history.",
		InputSchema: schema(nil, map[string]any{
			"page_id": prop("string", "Page to act on. Omitted means the current page."),
		}),
	}, s.historyTool(func(p playwright.Page) error {
		_, err := p.GoForward()
		return err
	}, "forward"))

	s.RegisterTool(Tool{
		Name:  "browser_reload",
		Title: "Reload the page",
		Description: "Reload the current page. Use this after editing a file the page loads, or after reloading " +
			"an extension whose content script needs to be re-injected.",
		InputSchema: schema(nil, map[string]any{
			"page_id": prop("string", "Page to act on. Omitted means the current page."),
			"clear_events": propDefault("boolean",
				"Empty the console, error and network buffers first, so what you read afterwards is only about "+
					"this load.", true),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			PageID      string `json:"page_id"`
			ClearEvents *bool  `json:"clear_events"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		if args.ClearEvents == nil || *args.ClearEvents {
			st.ClearEvents()
		}
		mark := st.Mark()
		if _, err := st.Page.Reload(); err != nil {
			return toolError("reloading %s: %v", st.ID, err), nil
		}
		return toolResult(fmt.Sprintf("Reloaded %s (%s)%s", st.ID, st.Page.URL(), suffixTrouble(st, mark))), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_wait_for",
		Title: "Wait for something",
		Description: "Wait until a condition holds: an element appears or disappears, text shows up on the page, " +
			"the network goes quiet, or simply a fixed delay. Prefer waiting on a condition over a delay - a " +
			"delay is either too short and flaky or too long and slow, and it tells you nothing about why.",
		InputSchema: schema(nil, map[string]any{
			"selector": prop("string", "Wait for the element matching this selector to reach state."),
			"ref":      prop("string", "Wait on the element with this ref from browser_snapshot."),
			"state": map[string]any{
				"type":        "string",
				"enum":        []string{"visible", "hidden", "attached", "detached"},
				"description": "Which state the element must reach. Default visible.",
				"default":     "visible",
			},
			"text":       prop("string", "Wait until this text appears anywhere on the page."),
			"load_state": map[string]any{"type": "string", "enum": []string{"load", "domcontentloaded", "networkidle"}, "description": "Wait for this load state."},
			"url":        prop("string", "Wait until the page URL contains this substring."),
			"delay_ms":   prop("integer", "Wait this many milliseconds unconditionally. A last resort."),
			"timeout_ms": prop("integer", "How long to wait before giving up."),
			"page_id":    prop("string", "Page to wait on. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Selector  string `json:"selector"`
			Ref       string `json:"ref"`
			State     string `json:"state"`
			Text      string `json:"text"`
			LoadState string `json:"load_state"`
			URL       string `json:"url"`
			DelayMs   int    `json:"delay_ms"`
			TimeoutMs int    `json:"timeout_ms"`
			PageID    string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		mark := st.Mark()
		timeout := float64(s.cfg.Browser.DefaultTimeoutMs)
		if args.TimeoutMs > 0 {
			timeout = float64(args.TimeoutMs)
		}

		switch {
		case args.Selector != "" || args.Ref != "":
			loc, err := target{Ref: args.Ref, Selector: args.Selector}.resolve(st)
			if err != nil {
				// A detached-state wait is satisfied by the element being gone,
				// so a missing ref is the answer rather than a failure.
				if args.State == "detached" || args.State == "hidden" {
					return toolResult("The element is already gone."), nil
				}
				return toolError("%v", err), nil
			}
			state := playwright.WaitForSelectorStateVisible
			switch args.State {
			case "hidden":
				state = playwright.WaitForSelectorStateHidden
			case "attached":
				state = playwright.WaitForSelectorStateAttached
			case "detached":
				state = playwright.WaitForSelectorStateDetached
			}
			if err := loc.WaitFor(playwright.LocatorWaitForOptions{
				State: state, Timeout: playwright.Float(timeout),
			}); err != nil {
				return toolError("waiting for %s to be %s: %v", args.State, describeWait(args), err), nil
			}
			return toolResult(fmt.Sprintf("The element is %s.", orDefault(args.State, "visible"))), nil

		case args.Text != "":
			loc := st.Page.GetByText(args.Text).First()
			if err := loc.WaitFor(playwright.LocatorWaitForOptions{
				State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(timeout),
			}); err != nil {
				return toolError("waiting for the text %q to appear: %v\n\n%s", args.Text, err, troubleSince(st, mark)), nil
			}
			return toolResult(fmt.Sprintf("The text %q is on the page.", args.Text)), nil

		case args.LoadState != "":
			state := playwright.LoadStateLoad
			switch args.LoadState {
			case "domcontentloaded":
				state = playwright.LoadStateDomcontentloaded
			case "networkidle":
				state = playwright.LoadStateNetworkidle
			}
			if err := st.Page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
				State: state, Timeout: playwright.Float(timeout),
			}); err != nil {
				return toolError("waiting for load state %s: %v", args.LoadState, err), nil
			}
			return toolResult("The page reached load state " + args.LoadState + "."), nil

		case args.URL != "":
			if err := st.Page.WaitForURL("**"+args.URL+"**", playwright.PageWaitForURLOptions{
				Timeout: playwright.Float(timeout),
			}); err != nil {
				return toolError("waiting for the URL to contain %q (it is %s): %v", args.URL, st.Page.URL(), err), nil
			}
			return toolResult("The page is at " + st.Page.URL()), nil

		case args.DelayMs > 0:
			st.Page.WaitForTimeout(float64(args.DelayMs))
			return toolResult(fmt.Sprintf("Waited %dms.", args.DelayMs)), nil
		}
		return toolError("name something to wait for: selector, ref, text, load_state, url or delay_ms"), nil
	})
}

// historyTool builds the back/forward handlers, which differ only in the call
// they make and the word they use.
func (s *Server) historyTool(move func(playwright.Page) error, word string) ToolHandler {
	return func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			PageID string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		if err := move(st.Page); err != nil {
			return toolError("going %s: %v", word, err), nil
		}
		return toolResult(fmt.Sprintf("Went %s; %s is now at %s", word, st.ID, st.Page.URL())), nil
	}
}

func waitUntilState(name string) *playwright.WaitUntilState {
	switch name {
	case "load":
		return playwright.WaitUntilStateLoad
	case "networkidle":
		return playwright.WaitUntilStateNetworkidle
	case "commit":
		return playwright.WaitUntilStateCommit
	default:
		return playwright.WaitUntilStateDomcontentloaded
	}
}

// troubleSince is the errors and failed requests a page reported after the
// mark, rendered for a message about the action that was running at the time.
//
// It is scoped to the action deliberately. A page that logged a failure during
// load has done so once, and repeating it on the answer to every subsequent
// click would read as though each click had caused it.
func troubleSince(st *PageState, mark eventMark) string {
	errs, net := st.Since(mark)
	var lines []string
	for i, e := range errs {
		if i == 3 {
			break
		}
		lines = append(lines, "page error: "+firstLine(e.Error))
	}
	for _, n := range net {
		if len(lines) >= 6 {
			break
		}
		switch {
		case n.Failure != "":
			lines = append(lines, fmt.Sprintf("request failed: %s %s (%s)", n.Method, truncate(n.URL, 100), n.Failure))
		case n.Status >= 400:
			lines = append(lines, fmt.Sprintf("http %d: %s %s", n.Status, n.Method, truncate(n.URL, 100)))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "The page reported:\n  " + strings.Join(lines, "\n  ") +
		"\n(browser_console and browser_network have the rest)"
}

// suffixTrouble is troubleSince as a trailing paragraph, or nothing at all.
func suffixTrouble(st *PageState, mark eventMark) string {
	if trouble := troubleSince(st, mark); trouble != "" {
		return "\n\n" + trouble
	}
	return ""
}

func describeWait(args struct {
	Selector  string `json:"selector"`
	Ref       string `json:"ref"`
	State     string `json:"state"`
	Text      string `json:"text"`
	LoadState string `json:"load_state"`
	URL       string `json:"url"`
	DelayMs   int    `json:"delay_ms"`
	TimeoutMs int    `json:"timeout_ms"`
	PageID    string `json:"page_id"`
}) string {
	if args.Ref != "" {
		return "ref " + args.Ref
	}
	return "selector " + args.Selector
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
