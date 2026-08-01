package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// registerPageTools adds the tools that report on the browser and choose which
// page the other tools act on.
func (s *Server) registerPageTools() {
	s.RegisterTool(Tool{
		Name:  "browser_status",
		Title: "Browser status",
		Description: "Report whether the browser is running, whether it was launched by this server or attached to " +
			"one you started, whether it is headless, which extensions are loaded, and which pages are open. " +
			"Call this first: it is the cheapest way to find out what you are driving, and it does not start " +
			"the browser if it is not already up.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema(nil, nil),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		return toolResultJSON(s.browserStatus()), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_start",
		Title: "Start or restart the browser",
		Description: "Bring the browser up without doing anything in it, or restart it. The browser starts on its " +
			"own when a tool needs it, so this is only worth calling to restart after a crash, or to get a clean " +
			"profile before a test run. In attach mode a restart reconnects to the same Chrome; it does not " +
			"close your window.",
		InputSchema: schema(nil, map[string]any{
			"restart": propDefault("boolean",
				"Close the current browser first. In attach mode this drops the connection and reconnects.", false),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Restart bool `json:"restart"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		var err error
		if args.Restart {
			err = s.browser.Restart()
		} else {
			err = s.browser.Start()
		}
		if err != nil {
			return toolError("%v", err), nil
		}
		return toolResultJSON(s.browserStatus()), nil
	})

	s.RegisterTool(Tool{
		Name:        "browser_pages",
		Title:       "List open pages",
		Description: "List every open tab with its id, title and URL, and say which one is current.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema(nil, nil),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		if _, err := s.browser.Context(); err != nil {
			return toolError("%v", err), nil
		}
		return toolResultJSON(map[string]any{
			"current": s.browser.CurrentID(),
			"pages":   s.pageSummaries(),
		}), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_select_page",
		Title: "Choose the current page",
		Description: "Make one tab the current page, so the other tools act on it without being passed a page_id " +
			"every time.",
		InputSchema: schema([]string{"page_id"}, map[string]any{
			"page_id": prop("string", "Page id from browser_pages."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			PageID string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if err := s.browser.Select(args.PageID); err != nil {
			return toolError("%v", err), nil
		}
		st, err := s.browser.CurrentPage()
		if err != nil {
			return toolError("%v", err), nil
		}
		return toolResult(fmt.Sprintf("Current page is now %s (%s)", st.ID, st.Page.URL())), nil
	})

	s.RegisterTool(Tool{
		Name:        "browser_new_page",
		Title:       "Open a tab",
		Description: "Open a new tab, optionally at a URL, and make it the current page.",
		InputSchema: schema(nil, map[string]any{
			"url": prop("string", "URL to open in the new tab. Omitted means about:blank."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			URL string `json:"url"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, err := s.browser.NewPage()
		if err != nil {
			return toolError("%v", err), nil
		}
		if args.URL != "" {
			if bad := s.checkNavigable(args.URL); bad != nil {
				return bad, nil
			}
			if _, err := st.Page.Goto(args.URL, playwright.PageGotoOptions{
				WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			}); err != nil {
				return toolError("opened %s but could not navigate to %s: %v", st.ID, args.URL, err), nil
			}
		}
		return toolResult(fmt.Sprintf("Opened %s at %s; it is now the current page", st.ID, st.Page.URL())), nil
	})

	s.RegisterTool(Tool{
		Name:        "browser_close_page",
		Title:       "Close a tab",
		Description: "Close a tab. The current page moves to the most recently seen remaining tab.",
		Annotations: &ToolAnnotations{DestructiveHint: true},
		InputSchema: schema(nil, map[string]any{
			"page_id": prop("string", "Page to close. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
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
		id := st.ID
		if err := st.Page.Close(); err != nil {
			return toolError("closing %s: %v", id, err), nil
		}
		return toolResult(fmt.Sprintf("Closed %s. Current page is now %s", id, s.browser.CurrentID())), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_frames",
		Title: "List frames",
		Description: "List the frames of the current page with their URLs and names. Worth a call when a snapshot " +
			"or a click cannot find an element that is plainly on the screen: it is usually inside an iframe.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema(nil, map[string]any{
			"page_id": prop("string", "Page to inspect. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
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
		type frameInfo struct {
			Index int    `json:"index"`
			Name  string `json:"name,omitempty"`
			URL   string `json:"url"`
			Main  bool   `json:"main,omitempty"`
		}
		var frames []frameInfo
		main := st.Page.MainFrame()
		for i, fr := range st.Page.Frames() {
			frames = append(frames, frameInfo{Index: i, Name: fr.Name(), URL: fr.URL(), Main: fr == main})
		}
		return toolResultJSON(map[string]any{"page_id": st.ID, "frames": frames}), nil
	})
}

// browserStatus is what browser_status reports.
type browserStatus struct {
	Running  bool   `json:"running"`
	Mode     string `json:"mode,omitempty"`
	Headless bool   `json:"headless"`
	// Interactive says a person is at this browser and may navigate it between
	// your calls. FollowActiveTab says tool calls retarget onto whatever tab
	// they are looking at.
	Interactive     bool          `json:"interactive"`
	FollowActiveTab bool          `json:"follow_active_tab,omitempty"`
	Channel         string        `json:"channel,omitempty"`
	CDPURL          string        `json:"cdp_url,omitempty"`
	ProfileDir      string        `json:"profile_dir,omitempty"`
	Extensions      []string      `json:"extensions,omitempty"`
	EvalAllowed     bool          `json:"eval_allowed"`
	NavHosts        []string      `json:"navigate_allowed_hosts,omitempty"`
	CurrentPage     string        `json:"current_page,omitempty"`
	Pages           []pageSummary `json:"pages,omitempty"`
}

// pageSummary is one row of browser_pages.
type pageSummary struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	URL     string `json:"url"`
	Current bool   `json:"current,omitempty"`
	// Focused is the tab the person is actually looking at. Reported only when
	// the browser follows focus, since finding out costs a round trip per tab.
	Focused bool `json:"focused,omitempty"`
	Console int  `json:"console_messages"`
	Errors  int  `json:"page_errors"`
	Network int  `json:"network_records"`
}

func (s *Server) browserStatus() browserStatus {
	cfg := s.cfg.Browser
	status := browserStatus{
		Running:         s.browser.Running(),
		Mode:            s.browser.Mode(),
		Headless:        cfg.Headless,
		Channel:         cfg.Channel,
		CDPURL:          cfg.CDPURL,
		ProfileDir:      cfg.ProfileDir,
		Extensions:      cfg.Extensions,
		Interactive:     cfg.Interactive,
		FollowActiveTab: cfg.FollowsActiveTab(),
		EvalAllowed:     cfg.AllowEval,
		NavHosts:        cfg.AllowNavigateHosts,
	}
	if cfg.CDPURL != "" {
		// In attach mode the profile is the browser's own, whatever it was
		// started with, so naming ours would be a lie.
		status.ProfileDir = ""
		status.Headless = false
	}
	if status.Running {
		status.CurrentPage = s.browser.CurrentID()
		status.Pages = s.pageSummaries()
	}
	return status
}

func (s *Server) pageSummaries() []pageSummary {
	current := s.browser.CurrentID()
	var out []pageSummary
	for _, st := range s.browser.Pages() {
		row := pageSummary{
			ID:      st.ID,
			URL:     st.Page.URL(),
			Current: st.ID == current,
			Console: len(st.Console()),
			Errors:  len(st.Errors()),
			Network: len(st.Network()),
		}
		if title, err := st.Page.Title(); err == nil {
			row.Title = title
		}
		if s.cfg.Browser.FollowsActiveTab() {
			row.Focused = pageIsVisible(st.Page)
		}
		out = append(out, row)
	}
	return out
}

// checkNavigable enforces browser.allow_navigate_hosts. It is the one place
// this server refuses to go somewhere, and it exists so that pointing the
// server at a browser holding a real session can be made safe.
func (s *Server) checkNavigable(raw string) *CallToolResult {
	allowed := s.cfg.Browser.AllowNavigateHosts
	if len(allowed) == 0 {
		return nil
	}
	host := urlHost(raw)
	if host == "" {
		// about:blank and the like carry no host and are harmless.
		if !strings.Contains(raw, "://") {
			return nil
		}
		return toolError("could not read a hostname out of %q", raw)
	}
	for _, entry := range allowed {
		if entry == "*" || host == entry || strings.HasSuffix(host, "."+entry) {
			return nil
		}
	}
	return toolError("this server is configured to navigate only to %s, so %s is not allowed. "+
		"Change browser.allow_navigate_hosts in config.json to widen it",
		strings.Join(allowed, ", "), host)
}
