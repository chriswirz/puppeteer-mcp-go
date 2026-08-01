package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// registerDebugTools adds the tools that report what the page did: what it
// logged, what it fetched, what it threw, and what it evaluates to now.
//
// These are the tools that make this server worth pointing at a browser rather
// than at a test runner. Everything the page reported since it loaded is
// already recorded, so the answer to "why did that not work" is one call away
// instead of a re-run with the DevTools panel open.
func (s *Server) registerDebugTools() {
	s.RegisterTool(Tool{
		Name:  "browser_console",
		Title: "Read the console",
		Description: "Return what the page has logged and any uncaught exceptions, oldest first. This covers the " +
			"whole life of the page, not just what happened since your last call, so a failure that happened " +
			"during load is still here when you come looking.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema(nil, map[string]any{
			"level": map[string]any{
				"type":        "string",
				"enum":        []string{"all", "error", "warning", "info", "log", "debug"},
				"description": "Only messages of this type. \"error\" also includes uncaught exceptions.",
				"default":     "all",
			},
			"contains": prop("string", "Only messages whose text contains this, case-insensitively."),
			"limit":    propDefault("integer", "Return at most this many, keeping the newest.", 100),
			"clear":    propDefault("boolean", "Empty the buffer after reading, so the next call reports only what is new.", false),
			"page_id":  prop("string", "Page to read. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Level    string `json:"level"`
			Contains string `json:"contains"`
			Limit    int    `json:"limit"`
			Clear    bool   `json:"clear"`
			PageID   string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		if args.Limit <= 0 {
			args.Limit = 100
		}
		needle := strings.ToLower(args.Contains)

		var kept []ConsoleEntry
		for _, entry := range st.Console() {
			if args.Level != "" && args.Level != "all" && !levelMatches(args.Level, entry.Type) {
				continue
			}
			if needle != "" && !strings.Contains(strings.ToLower(entry.Text), needle) {
				continue
			}
			kept = append(kept, entry)
		}
		errors := st.Errors()
		if args.Level != "" && args.Level != "all" && args.Level != "error" {
			errors = nil
		}
		if needle != "" {
			var filtered []ErrorEntry
			for _, e := range errors {
				if strings.Contains(strings.ToLower(e.Error), needle) {
					filtered = append(filtered, e)
				}
			}
			errors = filtered
		}
		if len(kept) > args.Limit {
			kept = kept[len(kept)-args.Limit:]
		}
		result := map[string]any{
			"page_id":  st.ID,
			"url":      st.Page.URL(),
			"messages": kept,
		}
		if len(errors) > 0 {
			result["uncaught_errors"] = errors
		}
		if dialogs := st.Dialogs(); len(dialogs) > 0 {
			result["dialogs"] = dialogs
		}
		if args.Clear {
			st.ClearEvents()
		}
		if len(kept) == 0 && len(errors) == 0 {
			return toolResult("The page has logged nothing matching that. If you expected output from a load, " +
				"note that messages are only recorded from the moment this server saw the page."), nil
		}
		return toolResultJSON(result), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_network",
		Title: "Read network activity",
		Description: "Return the requests the page has made, with method, URL, status and any failure. Filter to " +
			"failures alone to find the broken call behind a page that renders empty, or to the XHR/fetch types " +
			"to see what an app actually asked its API for.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema(nil, map[string]any{
			"failures_only": propDefault("boolean", "Only requests that failed or answered 4xx/5xx.", false),
			"resource_type": prop("string", "Only this resource type: xhr, fetch, document, script, stylesheet, image, font, websocket."),
			"contains":      prop("string", "Only requests whose URL contains this."),
			"limit":         propDefault("integer", "Return at most this many, keeping the newest.", 100),
			"clear":         propDefault("boolean", "Empty the buffer after reading.", false),
			"page_id":       prop("string", "Page to read. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			FailuresOnly bool   `json:"failures_only"`
			ResourceType string `json:"resource_type"`
			Contains     string `json:"contains"`
			Limit        int    `json:"limit"`
			Clear        bool   `json:"clear"`
			PageID       string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		if args.Limit <= 0 {
			args.Limit = 100
		}
		var kept []NetworkEntry
		for _, entry := range st.Network() {
			if args.FailuresOnly && entry.Failure == "" && entry.Status < 400 {
				continue
			}
			if args.ResourceType != "" && entry.Type != args.ResourceType {
				continue
			}
			if args.Contains != "" && !strings.Contains(entry.URL, args.Contains) {
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) > args.Limit {
			kept = kept[len(kept)-args.Limit:]
		}
		if args.Clear {
			st.ClearEvents()
		}
		if len(kept) == 0 {
			return toolResult("No requests match that filter."), nil
		}
		return toolResultJSON(map[string]any{
			"page_id":  st.ID,
			"requests": kept,
		}), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_clear_events",
		Title: "Clear the buffers",
		Description: "Empty the console, error and network buffers for a page. Call this immediately before an " +
			"interaction you want to study, so that everything you read afterwards belongs to it.",
		InputSchema: schema(nil, map[string]any{
			"page_id": prop("string", "Page to clear. Omitted means the current page."),
			"all":     propDefault("boolean", "Clear every open page instead of one.", false),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			PageID string `json:"page_id"`
			All    bool   `json:"all"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if args.All {
			pages := s.browser.Pages()
			for _, st := range pages {
				st.ClearEvents()
			}
			return toolResult(fmt.Sprintf("Cleared the buffers of %d page(s).", len(pages))), nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		st.ClearEvents()
		return toolResult("Cleared the buffers of " + st.ID + "."), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_storage",
		Title: "Read or write storage",
		Description: "Read localStorage, sessionStorage and cookies for the current page, or set and remove one " +
			"key. This is how you check what an app persisted, and how you put it into a state - a feature flag, " +
			"a fake session - without clicking through to get there.",
		InputSchema: schema(nil, map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"get", "set", "remove", "clear"},
				"description": "What to do. Default get.",
				"default":     "get",
			},
			"area": map[string]any{
				"type":        "string",
				"enum":        []string{"local", "session", "cookies"},
				"description": "Which store to act on. Default local. Reading with action=get returns all three.",
				"default":     "local",
			},
			"key":     prop("string", "Key to set or remove."),
			"value":   prop("string", "Value to set."),
			"page_id": prop("string", "Page to act on. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Action string `json:"action"`
			Area   string `json:"area"`
			Key    string `json:"key"`
			Value  string `json:"value"`
			PageID string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		area := orDefault(args.Area, "local")
		switch orDefault(args.Action, "get") {
		case "get":
			value, err := st.Page.Evaluate(readStorageJS)
			if err != nil {
				return toolError("reading storage: %v", err), nil
			}
			result := map[string]any{"page_id": st.ID, "url": st.Page.URL(), "storage": value}
			if ctxObj, err := s.browser.Context(); err == nil {
				if cookies, err := ctxObj.Cookies(st.Page.URL()); err == nil {
					result["cookies"] = cookies
				}
			}
			return toolResultJSON(result), nil

		case "set":
			if args.Key == "" {
				return toolError("key is required to set a value"), nil
			}
			if area == "cookies" {
				ctxObj, err := s.browser.Context()
				if err != nil {
					return toolError("%v", err), nil
				}
				if err := ctxObj.AddCookies([]playwright.OptionalCookie{{
					Name:  args.Key,
					Value: args.Value,
					URL:   playwright.String(st.Page.URL()),
				}}); err != nil {
					return toolError("setting the cookie: %v", err), nil
				}
				return toolResult(fmt.Sprintf("Set cookie %s.", args.Key)), nil
			}
			if _, err := st.Page.Evaluate(writeStorageJS, map[string]any{
				"area": area, "key": args.Key, "value": args.Value, "remove": false,
			}); err != nil {
				return toolError("setting %s: %v", args.Key, err), nil
			}
			return toolResult(fmt.Sprintf("Set %s in %sStorage.", args.Key, area)), nil

		case "remove":
			if args.Key == "" {
				return toolError("key is required to remove a value"), nil
			}
			if _, err := st.Page.Evaluate(writeStorageJS, map[string]any{
				"area": area, "key": args.Key, "remove": true,
			}); err != nil {
				return toolError("removing %s: %v", args.Key, err), nil
			}
			return toolResult(fmt.Sprintf("Removed %s from %sStorage.", args.Key, area)), nil

		case "clear":
			if area == "cookies" {
				ctxObj, err := s.browser.Context()
				if err != nil {
					return toolError("%v", err), nil
				}
				if err := ctxObj.ClearCookies(); err != nil {
					return toolError("clearing cookies: %v", err), nil
				}
				return toolResult("Cleared every cookie in this browser context."), nil
			}
			if _, err := st.Page.Evaluate(writeStorageJS, map[string]any{"area": area, "clear": true}); err != nil {
				return toolError("clearing %sStorage: %v", area, err), nil
			}
			return toolResult(fmt.Sprintf("Cleared %sStorage.", area)), nil
		}
		return toolError("action must be get, set, remove or clear"), nil
	})
}

// readStorageJS returns both web storages as plain objects.
const readStorageJS = `() => {
  const dump = (store) => {
    const out = {};
    try {
      for (let i = 0; i < store.length; i++) {
        const k = store.key(i);
        out[k] = String(store.getItem(k)).slice(0, 2000);
      }
    } catch (e) {
      return { error: String(e) };
    }
    return out;
  };
  return { local: dump(localStorage), session: dump(sessionStorage) };
}`

// writeStorageJS sets, removes or clears one web storage.
const writeStorageJS = `(op) => {
  const store = op.area === 'session' ? sessionStorage : localStorage;
  if (op.clear) { store.clear(); return true; }
  if (op.remove) { store.removeItem(op.key); return true; }
  store.setItem(op.key, op.value);
  return true;
}`

// levelMatches maps the filter name onto the console message types that count
// as it. Chrome says "warning" where a caller is as likely to write "warn".
func levelMatches(level, messageType string) bool {
	switch level {
	case "error":
		return messageType == "error" || messageType == "assert"
	case "warning", "warn":
		return messageType == "warning" || messageType == "warn"
	default:
		return messageType == level
	}
}
