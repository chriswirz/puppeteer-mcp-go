package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// registerExtensionTools adds the tools for working on a Chrome extension.
//
// An extension is not one program but three or four that barely share an
// address space: a background service worker holding the chrome.* APIs, a
// content script in an isolated world alongside the page, and popup or options
// pages that are ordinary documents on a chrome-extension:// origin. Most of
// the confusion in extension debugging comes from asking the wrong one a
// question - reading chrome.storage from the page, where it does not exist, or
// expecting a content script's variables to be visible to the page's own
// scripts. These tools name each part so that a question goes where it can be
// answered.
func (s *Server) registerExtensionTools() {
	s.RegisterTool(Tool{
		Name:  "browser_extensions",
		Title: "List loaded extensions",
		Description: "List the extensions Chrome has loaded, with each one's id, name, version and manifest " +
			"version, and say which of them currently has a running service worker. The id is what " +
			"chrome-extension:// URLs are built from and what browser_extension_eval takes.\n\n" +
			"An extension with no running service worker is normal, not broken: Chrome stops an idle MV3 worker " +
			"and starts it again on the next event. browser_extension_eval wakes it.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema(nil, nil),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		extensions, err := s.listExtensions()
		if err != nil {
			return toolError("%v", err), nil
		}
		if len(extensions) == 0 {
			return toolResult("No extensions are loaded.\n\n" +
				"In launch mode, list the unpacked directories in browser.extensions in config.json (Chrome " +
				"cannot load one in headless mode, so the browser will be headed). In attach mode, load the " +
				"extension in your own Chrome at chrome://extensions with developer mode on, then reconnect."), nil
		}
		return toolResultJSON(map[string]any{"extensions": extensions}), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_extension_eval",
		Title: "Run code in an extension worker",
		Description: "Evaluate JavaScript inside an extension's background service worker, where the chrome.* " +
			"APIs actually live. This is how you read chrome.storage, inspect the worker's own state, or send " +
			"a message through chrome.runtime as the extension itself.\n\n" +
			"Awaiting is handled: return a promise and its resolved value comes back, so " +
			"\"() => chrome.storage.local.get(null)\" does what you would hope. The value must survive JSON.\n\n" +
			"If the worker is asleep this wakes it first. A worker that will not start at all is usually a syntax " +
			"error in it - browser_console on its own target, or chrome://extensions in a headed window, will say.",
		InputSchema: schema([]string{"expression"}, map[string]any{
			"expression":   prop("string", "JavaScript to evaluate in the worker, e.g. \"() => chrome.storage.local.get(null)\"."),
			"extension_id": prop("string", "Which extension. Omitted is fine when exactly one is loaded."),
			"arg":          map[string]any{"description": "Optional JSON value passed to the function."},
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		if !s.cfg.Browser.AllowEval {
			return toolError("evaluation is disabled on this server (browser.allow_eval is false)"), nil
		}
		var args struct {
			Expression  string `json:"expression"`
			ExtensionID string `json:"extension_id"`
			Arg         any    `json:"arg"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if strings.TrimSpace(args.Expression) == "" {
			return toolError("expression is required"), nil
		}
		worker, id, bad := s.extensionWorker(args.ExtensionID)
		if bad != nil {
			return bad, nil
		}
		value, err := worker.Evaluate(args.Expression, args.Arg)
		if err != nil {
			return toolError("evaluating in the service worker of %s: %v", id, err), nil
		}
		return toolResultJSON(map[string]any{"extension_id": id, "value": value}), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_extension_reload",
		Title: "Reload an extension",
		Description: "Reload an extension in place, the equivalent of the reload button on chrome://extensions. " +
			"Do this after editing the extension's code: the new background script takes effect immediately, but " +
			"a content script is only injected on load, so reload the pages you are testing on afterwards too.\n\n" +
			"It works by calling chrome.runtime.reload() from inside the extension, so the extension must have a " +
			"service worker that can be woken.",
		Annotations: &ToolAnnotations{DestructiveHint: true},
		InputSchema: schema(nil, map[string]any{
			"extension_id": prop("string", "Which extension. Omitted is fine when exactly one is loaded."),
			"reload_pages": propDefault("boolean",
				"Also reload every open tab afterwards, so content scripts are re-injected.", false),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			ExtensionID string `json:"extension_id"`
			ReloadPages bool   `json:"reload_pages"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		worker, id, bad := s.extensionWorker(args.ExtensionID)
		if bad != nil {
			return bad, nil
		}
		// The worker is torn down by its own reload call, so the evaluation
		// returning an error is the expected outcome, not a failure.
		worker.Evaluate("() => chrome.runtime.reload()")

		reloaded := 0
		if args.ReloadPages {
			for _, st := range s.browser.Pages() {
				if strings.HasPrefix(st.Page.URL(), "chrome-extension://") {
					continue
				}
				if _, err := st.Page.Reload(); err == nil {
					reloaded++
				}
			}
		}
		msg := fmt.Sprintf("Reloaded extension %s.", id)
		if args.ReloadPages {
			msg += fmt.Sprintf(" Reloaded %d page(s) so content scripts are re-injected.", reloaded)
		} else {
			msg += " Content scripts are only injected on page load, so reload any page you are testing on."
		}
		return toolResult(msg), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_extension_page",
		Title: "Open an extension page",
		Description: "Open one of the extension's own pages - its popup, options page, or any other HTML file it " +
			"ships - in a tab, and make it current. A popup opened this way is a normal page you can snapshot, " +
			"click and read the console of, which is the only practical way to debug one: a real popup closes the " +
			"moment it loses focus.",
		InputSchema: schema(nil, map[string]any{
			"extension_id": prop("string", "Which extension. Omitted is fine when exactly one is loaded."),
			"path": propDefault("string",
				"Path within the extension, as in the manifest, e.g. popup.html or options.html.", "popup.html"),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			ExtensionID string `json:"extension_id"`
			Path        string `json:"path"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		id := args.ExtensionID
		if id == "" {
			resolved, bad := s.soleExtensionID()
			if bad != nil {
				return bad, nil
			}
			id = resolved
		}
		path := strings.TrimPrefix(orDefault(args.Path, "popup.html"), "/")
		url := fmt.Sprintf("chrome-extension://%s/%s", id, path)

		st, err := s.browser.NewPage()
		if err != nil {
			return toolError("%v", err), nil
		}
		if _, err := st.Page.Goto(url, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		}); err != nil {
			return toolError("opening %s: %v\n\n"+
				"Check the path against the extension's manifest - a page that is not listed there and not "+
				"in web_accessible_resources may still open, but a path that does not exist will not",
				url, err), nil
		}
		return toolResult(fmt.Sprintf("Opened %s as %s; it is now the current page.", url, st.ID)), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_extension_storage",
		Title: "Read or write extension storage",
		Description: "Read or write chrome.storage for an extension - local, sync or session. This is the " +
			"extension's own store, which is not the same thing as the localStorage of any page and is not " +
			"reachable from one; browser_storage will not show it.",
		InputSchema: schema(nil, map[string]any{
			"action":       map[string]any{"type": "string", "enum": []string{"get", "set", "remove", "clear"}, "description": "What to do. Default get.", "default": "get"},
			"area":         map[string]any{"type": "string", "enum": []string{"local", "sync", "session"}, "description": "Which storage area. Default local.", "default": "local"},
			"key":          prop("string", "Key to read, set or remove. Omitted with get returns everything."),
			"value":        map[string]any{"description": "JSON value to store under key."},
			"extension_id": prop("string", "Which extension. Omitted is fine when exactly one is loaded."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Action      string `json:"action"`
			Area        string `json:"area"`
			Key         string `json:"key"`
			Value       any    `json:"value"`
			ExtensionID string `json:"extension_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		worker, id, bad := s.extensionWorker(args.ExtensionID)
		if bad != nil {
			return bad, nil
		}
		value, err := worker.Evaluate(extensionStorageJS, map[string]any{
			"action": orDefault(args.Action, "get"),
			"area":   orDefault(args.Area, "local"),
			"key":    args.Key,
			"value":  args.Value,
		})
		if err != nil {
			return toolError("chrome.storage on %s: %v", id, err), nil
		}
		return toolResultJSON(map[string]any{"extension_id": id, "result": value}), nil
	})
}

// extensionInfo is one row of browser_extensions.
type extensionInfo struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	Version         string `json:"version,omitempty"`
	ManifestVersion int    `json:"manifest_version,omitempty"`
	WorkerRunning   bool   `json:"service_worker_running"`
	WorkerURL       string `json:"service_worker_url,omitempty"`
}

// listExtensions asks each running worker and background page to identify
// itself. There is no CDP call that lists installed extensions, so what is
// discoverable is what is running or has a target.
func (s *Server) listExtensions() ([]extensionInfo, error) {
	browserCtx, err := s.browser.Context()
	if err != nil {
		return nil, err
	}
	byID := map[string]extensionInfo{}

	for _, worker := range browserCtx.ServiceWorkers() {
		id := extensionIDFromURL(worker.URL())
		if id == "" {
			continue
		}
		info := extensionInfo{ID: id, WorkerRunning: true, WorkerURL: worker.URL()}
		if value, err := worker.Evaluate(manifestJS); err == nil {
			var m struct {
				Name            string `json:"name"`
				Version         string `json:"version"`
				ManifestVersion int    `json:"manifest_version"`
			}
			if remarshal(value, &m) == nil {
				info.Name, info.Version, info.ManifestVersion = m.Name, m.Version, m.ManifestVersion
			}
		}
		byID[id] = info
	}

	// An MV2 extension has a background page rather than a service worker, and
	// any extension page open in a tab identifies its extension too.
	for _, page := range append(browserCtx.BackgroundPages(), browserCtx.Pages()...) {
		id := extensionIDFromURL(page.URL())
		if id == "" {
			continue
		}
		if _, seen := byID[id]; !seen {
			byID[id] = extensionInfo{ID: id}
		}
	}

	var out []extensionInfo
	for _, info := range byID {
		out = append(out, info)
	}
	return out, nil
}

// manifestJS reads the extension's own manifest from inside it.
const manifestJS = `() => chrome.runtime.getManifest()`

// extensionStorageJS is the chrome.storage read/write, run inside the worker.
const extensionStorageJS = `async (op) => {
  const area = chrome.storage[op.area];
  if (!area) throw new Error('no chrome.storage.' + op.area + ' in this extension');
  switch (op.action) {
    case 'set': {
      if (!op.key) throw new Error('key is required to set');
      await area.set({ [op.key]: op.value });
      return { set: op.key };
    }
    case 'remove': {
      if (!op.key) throw new Error('key is required to remove');
      await area.remove(op.key);
      return { removed: op.key };
    }
    case 'clear': {
      await area.clear();
      return { cleared: op.area };
    }
    default:
      return await area.get(op.key ? op.key : null);
  }
}`

// extensionWorker finds an extension's service worker, waking it if Chrome has
// let it go idle.
func (s *Server) extensionWorker(id string) (playwright.Worker, string, *CallToolResult) {
	browserCtx, err := s.browser.Context()
	if err != nil {
		return nil, "", toolError("%v", err)
	}
	if id == "" {
		resolved, bad := s.soleExtensionID()
		if bad != nil {
			return nil, "", bad
		}
		id = resolved
	}
	for _, worker := range browserCtx.ServiceWorkers() {
		if extensionIDFromURL(worker.URL()) == id {
			return worker, id, nil
		}
	}

	// Chrome stops an idle MV3 worker and starts it again on demand. Opening
	// one of the extension's own pages is a demand: it wakes the worker, and
	// the page is closed again once it has.
	if worker := s.wakeExtensionWorker(browserCtx, id); worker != nil {
		return worker, id, nil
	}
	return nil, "", toolError("extension %s has no running service worker, and it did not start when woken.\n\n"+
		"An MV3 worker that will not start is usually a syntax error in it, or a manifest with no background "+
		"service_worker at all. An MV2 extension has a background page instead and cannot be reached this way. "+
		"Open chrome://extensions in a headed window to see the error Chrome recorded", id)
}

// wakeExtensionWorker opens an extension page briefly so Chrome starts the
// worker, then waits for it to appear.
func (s *Server) wakeExtensionWorker(browserCtx playwright.BrowserContext, id string) playwright.Worker {
	page, err := browserCtx.NewPage()
	if err != nil {
		return nil
	}
	defer page.Close()
	// Any URL on the extension's origin will do; it need not exist, because
	// the origin is what starts the worker.
	page.Goto("chrome-extension://"+id+"/manifest.json", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(5000),
	})
	deadline := 20
	for i := 0; i < deadline; i++ {
		for _, worker := range browserCtx.ServiceWorkers() {
			if extensionIDFromURL(worker.URL()) == id {
				return worker
			}
		}
		page.WaitForTimeout(100)
	}
	return nil
}

// soleExtensionID resolves an omitted extension_id, which is unambiguous only
// when exactly one extension is loaded.
func (s *Server) soleExtensionID() (string, *CallToolResult) {
	extensions, err := s.listExtensions()
	if err != nil {
		return "", toolError("%v", err)
	}
	switch len(extensions) {
	case 0:
		return "", toolError("no extension is loaded in this browser. " +
			"List its unpacked directory in browser.extensions in config.json, or load it by hand at " +
			"chrome://extensions in the Chrome you are attached to")
	case 1:
		return extensions[0].ID, nil
	default:
		var ids []string
		for _, e := range extensions {
			ids = append(ids, fmt.Sprintf("%s (%s)", e.ID, orDefault(e.Name, "unnamed")))
		}
		return "", toolError("several extensions are loaded, so name one with extension_id: %s",
			strings.Join(ids, ", "))
	}
}

// extensionIDFromURL pulls the id out of a chrome-extension:// URL.
func extensionIDFromURL(raw string) string {
	const prefix = "chrome-extension://"
	if !strings.HasPrefix(raw, prefix) {
		return ""
	}
	rest := raw[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}
