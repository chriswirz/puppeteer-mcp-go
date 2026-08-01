package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// registerEvalTools adds the two escape hatches: run JavaScript in the page,
// and send a raw DevTools command.
//
// Every other tool in this server is a convenience over one of these. They are
// here because a debugging session always turns up something no tool
// anticipated, and the alternative to an escape hatch is a dead end.
func (s *Server) registerEvalTools() {
	if !s.cfg.Browser.AllowEval {
		return
	}

	s.RegisterTool(Tool{
		Name:  "browser_evaluate",
		Title: "Run JavaScript in the page",
		Description: "Evaluate a JavaScript expression in the page and return its value. An arrow function is " +
			"called for you; a bare expression is evaluated as-is. A returned promise is awaited. The value must " +
			"survive JSON, so return the fields you want rather than a DOM node - a node comes back as an empty " +
			"object.\n\n" +
			"Pass ref or selector to evaluate against one element instead: the function then receives it as its " +
			"first argument.\n\n" +
			"This runs in the page's own world, so it sees the app's globals, but not a content script's isolated " +
			"world and not the chrome.* APIs - use browser_extension_eval for those.",
		InputSchema: schema([]string{"expression"}, elementProps(map[string]any{
			"expression": prop("string", "JavaScript to evaluate, e.g. \"() => document.title\" or "+
				"\"el => el.getBoundingClientRect()\"."),
			"arg": map[string]any{"description": "Optional JSON value passed as the last argument to the function."},
		})),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			Expression string `json:"expression"`
			Arg        any    `json:"arg"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if strings.TrimSpace(args.Expression) == "" {
			return toolError("expression is required"), nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		mark := st.Mark()

		var value any
		if args.Ref != "" || args.Selector != "" {
			loc, err := args.target().resolve(st)
			if err != nil {
				return toolError("%v", err), nil
			}
			value, err = loc.Evaluate(args.Expression, args.Arg,
				playwright.LocatorEvaluateOptions{Timeout: s.timeout(args.TimeoutMs)})
			if err != nil {
				return toolError("evaluating against %s: %v", args.target().describe(), err), nil
			}
		} else {
			value, err = st.Page.Evaluate(args.Expression, args.Arg)
			if err != nil {
				return toolError("evaluating in %s: %v%s", st.ID, err, suffixTrouble(st, mark)), nil
			}
		}
		return toolResultJSON(map[string]any{"page_id": st.ID, "value": value}), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_cdp",
		Title: "Send a DevTools command",
		Description: "Send a raw Chrome DevTools Protocol command to the page and return its result - " +
			"Network.getAllCookies, Page.captureSnapshot, Emulation.setCPUThrottlingRate, Debugger.*, " +
			"Performance.*, anything DevTools itself can do. This is the last resort when no other tool " +
			"covers what you need, and it is the reason this server can go anywhere DevTools can. Many " +
			"domains must be enabled first with their own .enable command.",
		InputSchema: schema([]string{"method"}, map[string]any{
			"method":  prop("string", "CDP method name, e.g. \"Network.enable\" or \"Page.getLayoutMetrics\"."),
			"params":  map[string]any{"type": "object", "description": "Parameters for the method."},
			"page_id": prop("string", "Page whose target to send to. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			PageID string         `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if args.Method == "" {
			return toolError("method is required"), nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		browserCtx, err := s.browser.Context()
		if err != nil {
			return toolError("%v", err), nil
		}
		session, err := browserCtx.NewCDPSession(st.Page)
		if err != nil {
			return toolError("opening a CDP session on %s: %v", st.ID, err), nil
		}
		defer session.Detach()

		value, err := session.Send(args.Method, args.Params)
		if err != nil {
			return toolError("%s: %v", args.Method, err), nil
		}
		return toolResultJSON(map[string]any{
			"page_id": st.ID,
			"method":  args.Method,
			"result":  value,
		}), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_add_init_script",
		Title: "Run a script before every page load",
		Description: "Register JavaScript to run in every page of this browser context before any of the page's " +
			"own scripts. This is how you stub something the app depends on - a clock, a random source, a global " +
			"an API would otherwise set - so that the behaviour you are chasing is reproducible. It applies to " +
			"pages loaded from now on, not to what is already open, so reload afterwards.",
		InputSchema: schema([]string{"script"}, map[string]any{
			"script": prop("string", "JavaScript to run at document start in every page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Script string `json:"script"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if strings.TrimSpace(args.Script) == "" {
			return toolError("script is required"), nil
		}
		browserCtx, err := s.browser.Context()
		if err != nil {
			return toolError("%v", err), nil
		}
		if err := browserCtx.AddInitScript(playwright.Script{Content: playwright.String(args.Script)}); err != nil {
			return toolError("registering the init script: %v", err), nil
		}
		return toolResult("Registered. It runs at document start in every page loaded from now on; " +
			"reload the current page for it to take effect there."), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_route",
		Title: "Intercept a URL pattern",
		Description: "Intercept requests matching a glob pattern and answer them without going to the network: " +
			"fulfil with a canned body, fail them outright, or abort them. This is how you test an error path " +
			"you cannot make the real API produce, and how you check what a page does when a dependency is down.\n\n" +
			"Patterns are globs against the full URL, e.g. \"**/api/users\" or \"https://cdn.example.com/**\".",
		InputSchema: schema([]string{"pattern"}, map[string]any{
			"pattern": prop("string", "URL glob to intercept."),
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"fulfill", "abort", "remove"},
				"description": "fulfill answers with the body given here; abort fails the request; remove cancels a previous interception of this pattern.",
				"default":     "fulfill",
			},
			"status":       propDefault("integer", "Status code to answer with, for fulfill.", 200),
			"body":         prop("string", "Response body, for fulfill."),
			"content_type": propDefault("string", "Content-Type of the body.", "application/json"),
			"error_code":   propDefault("string", "Failure to report, for abort: failed, timedout, connectionrefused, accessdenied, internetdisconnected.", "failed"),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Pattern     string `json:"pattern"`
			Action      string `json:"action"`
			Status      int    `json:"status"`
			Body        string `json:"body"`
			ContentType string `json:"content_type"`
			ErrorCode   string `json:"error_code"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if args.Pattern == "" {
			return toolError("pattern is required"), nil
		}
		browserCtx, err := s.browser.Context()
		if err != nil {
			return toolError("%v", err), nil
		}
		switch orDefault(args.Action, "fulfill") {
		case "remove":
			if err := browserCtx.Unroute(args.Pattern); err != nil {
				return toolError("removing the route for %s: %v", args.Pattern, err), nil
			}
			return toolResult("Requests matching " + args.Pattern + " go to the network again."), nil

		case "abort":
			code := orDefault(args.ErrorCode, "failed")
			if err := browserCtx.Route(args.Pattern, func(route playwright.Route) {
				route.Abort(code)
			}); err != nil {
				return toolError("routing %s: %v", args.Pattern, err), nil
			}
			return toolResult(fmt.Sprintf("Requests matching %s now fail with %s.", args.Pattern, code)), nil

		default:
			status := args.Status
			if status == 0 {
				status = 200
			}
			contentType := orDefault(args.ContentType, "application/json")
			body := args.Body
			if err := browserCtx.Route(args.Pattern, func(route playwright.Route) {
				route.Fulfill(playwright.RouteFulfillOptions{
					Status:      playwright.Int(status),
					ContentType: playwright.String(contentType),
					Body:        body,
				})
			}); err != nil {
				return toolError("routing %s: %v", args.Pattern, err), nil
			}
			return toolResult(fmt.Sprintf("Requests matching %s are now answered with %d %s (%d bytes), "+
				"without reaching the network.", args.Pattern, status, contentType, len(body))), nil
		}
	})
}
