package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
)

// registerInspectionTools adds the tools that answer "what is on the page".
func (s *Server) registerInspectionTools() {
	s.RegisterTool(Tool{
		Name:  "browser_snapshot",
		Title: "Snapshot the page",
		Description: "Return the page as an outline of its elements - role, accessible name, value, state - with " +
			"a ref on each one. This is the tool to reach for when you want to know what is on the page: it is " +
			"far cheaper than a screenshot, and the refs it hands back are what browser_click, browser_type and " +
			"the rest take, so you never have to invent a CSS selector. Take a fresh snapshot after the page " +
			"changes; refs from an older one stop resolving once their elements are replaced.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema(nil, map[string]any{
			"all_frames": propDefault("boolean",
				"Also walk every iframe. Off by default because most pages have none; turn it on when an element "+
					"you can see is not in the snapshot.", false),
			"visible_only": propDefault("boolean",
				"Skip elements that are not rendered. Cuts a lot of noise on a page with hidden menus, but costs "+
					"a layout measurement per element.", false),
			"max_nodes": propDefault("integer", "Stop after this many elements.", 800),
			"page_id":   prop("string", "Page to snapshot. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			AllFrames   bool   `json:"all_frames"`
			VisibleOnly bool   `json:"visible_only"`
			MaxNodes    int    `json:"max_nodes"`
			PageID      string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		frames, err := takeSnapshot(st, snapshotOptions{
			AllFrames:   args.AllFrames,
			VisibleOnly: args.VisibleOnly,
			MaxNodes:    args.MaxNodes,
		})
		if err != nil {
			return toolError("snapshotting %s: %v", st.ID, err), nil
		}
		return &CallToolResult{
			Content:           textContent(renderSnapshot(frames)),
			StructuredContent: map[string]any{"page_id": st.ID, "frames": frames},
		}, nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_screenshot",
		Title: "Screenshot the page",
		Description: "Capture the page, or one element, as a PNG. Take one when the question is genuinely visual - " +
			"a layout that looks wrong, a style that is not applying, a rendering bug. When the question is what " +
			"is on the page or what to click, browser_snapshot answers it better and for a fraction of the cost. " +
			"Every capture is written to the configured screenshot directory and the path returned; a large one " +
			"is returned as that path alone rather than inlined.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema(nil, elementProps(map[string]any{
			"full_page": propDefault("boolean",
				"Capture the whole scrollable document rather than the viewport. On a long page this is a very "+
					"large image.", false),
			"path": prop("string", "Filename for the PNG. Relative paths land in the configured screenshot "+
				"directory, which is also where an unnamed capture goes, under a timestamped name."),
		})),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			FullPage bool   `json:"full_page"`
			Path     string `json:"path"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}

		var data []byte
		if args.Ref != "" || args.Selector != "" {
			loc, err := args.target().resolve(st)
			if err != nil {
				return toolError("%v", err), nil
			}
			data, err = loc.Screenshot(playwright.LocatorScreenshotOptions{Timeout: s.timeout(args.TimeoutMs)})
			if err != nil {
				return toolError("screenshotting %s: %v", args.target().describe(), err), nil
			}
		} else {
			opts := playwright.PageScreenshotOptions{Timeout: s.timeout(args.TimeoutMs)}
			if args.FullPage {
				opts.FullPage = playwright.Bool(true)
			}
			data, err = st.Page.Screenshot(opts)
			if err != nil {
				return toolError("screenshotting %s: %v", st.ID, err), nil
			}
		}

		// Every capture is kept on disk. The file is the durable copy - it
		// outlives the tool result, it is what the served directory serves,
		// and it is the only thing a person can open afterwards.
		path, err := s.writeScreenshot(args.Path, data)
		if err != nil {
			return toolError("%v", err), nil
		}
		where := path
		// A client on another machine cannot open that path, so the URL
		// serving it is given too whenever the directory is served.
		if url := s.cfg.ScreenshotURL(path); url != "" {
			where += " (served at " + url + ")"
		}

		// Whether the image also comes back inline is a separate question: a
		// megabyte of base64 in a tool result costs more than it can possibly
		// tell anyone, and most full-page captures are past that.
		if len(data) > s.cfg.Browser.MaxScreenshotBytes {
			return toolResult(fmt.Sprintf("Wrote the screenshot to %s - %d KB, over the %d KB inline limit, so it is not included here",
				where, len(data)/1024, s.cfg.Browser.MaxScreenshotBytes/1024)), nil
		}
		return &CallToolResult{Content: []Content{
			{Type: "text", Text: "Wrote the screenshot to " + where},
			{
				Type:     "image",
				Data:     base64.StdEncoding.EncodeToString(data),
				MimeType: "image/png",
			},
		}}, nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_get_text",
		Title: "Read text",
		Description: "Read the visible text of an element, or of the whole page when no element is named. Use it " +
			"to check what a page actually says, rather than inferring it from a screenshot.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema(nil, elementProps(map[string]any{
			"max_chars": propDefault("integer", "Truncate after this many characters.", 20000),
		})),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			MaxChars int `json:"max_chars"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if args.MaxChars <= 0 {
			args.MaxChars = 20000
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		var text string
		if args.Ref != "" || args.Selector != "" {
			loc, err := args.target().resolve(st)
			if err != nil {
				return toolError("%v", err), nil
			}
			text, err = loc.InnerText(playwright.LocatorInnerTextOptions{Timeout: s.timeout(args.TimeoutMs)})
			if err != nil {
				return toolError("reading %s: %v", args.target().describe(), err), nil
			}
		} else {
			value, err := st.Page.Evaluate("() => document.body ? document.body.innerText : ''")
			if err != nil {
				return toolError("reading the page text: %v", err), nil
			}
			text, _ = value.(string)
		}
		if len(text) > args.MaxChars {
			text = text[:args.MaxChars] + "\n... (truncated; raise max_chars or name a smaller element)"
		}
		return toolResult(text), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_get_html",
		Title: "Read HTML",
		Description: "Read the live HTML of an element, or of the whole document. This is the rendered DOM, not " +
			"the source the server sent, so it reflects everything scripts have done to the page - which is " +
			"usually the difference you are chasing.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema(nil, elementProps(map[string]any{
			"outer":     propDefault("boolean", "Include the element's own tag, not just its contents.", true),
			"max_chars": propDefault("integer", "Truncate after this many characters.", 20000),
		})),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			Outer    *bool `json:"outer"`
			MaxChars int   `json:"max_chars"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if args.MaxChars <= 0 {
			args.MaxChars = 20000
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		var html string
		if args.Ref != "" || args.Selector != "" {
			loc, err := args.target().resolve(st)
			if err != nil {
				return toolError("%v", err), nil
			}
			if args.Outer == nil || *args.Outer {
				value, err := loc.Evaluate("el => el.outerHTML", nil)
				if err != nil {
					return toolError("reading %s: %v", args.target().describe(), err), nil
				}
				html, _ = value.(string)
			} else {
				html, err = loc.InnerHTML(playwright.LocatorInnerHTMLOptions{Timeout: s.timeout(args.TimeoutMs)})
				if err != nil {
					return toolError("reading %s: %v", args.target().describe(), err), nil
				}
			}
		} else {
			html, err = st.Page.Content()
			if err != nil {
				return toolError("reading the page HTML: %v", err), nil
			}
		}
		if len(html) > args.MaxChars {
			html = html[:args.MaxChars] + "\n<!-- truncated; raise max_chars or name a smaller element -->"
		}
		return toolResult(html), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_query",
		Title: "Find elements by selector",
		Description: "Count and describe the elements a selector matches, with their tag, text, attributes and " +
			"whether they are visible. This is the tool for checking a selector before relying on it, and for " +
			"finding out why a click missed: nothing matched, or six things did and it clicked the wrong one.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true},
		InputSchema: schema([]string{"selector"}, map[string]any{
			"selector":   prop("string", "CSS selector, or another Playwright engine such as text= or role=."),
			"limit":      propDefault("integer", "Describe at most this many matches.", 20),
			"all_frames": propDefault("boolean", "Search every frame, not just the main document.", false),
			"page_id":    prop("string", "Page to search. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Selector  string `json:"selector"`
			Limit     int    `json:"limit"`
			AllFrames bool   `json:"all_frames"`
			PageID    string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if args.Selector == "" {
			return toolError("selector is required"), nil
		}
		if args.Limit <= 0 {
			args.Limit = 20
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		frames := []playwright.Frame{st.Page.MainFrame()}
		if args.AllFrames {
			frames = st.Page.Frames()
		}
		type match struct {
			Frame   string            `json:"frame,omitempty"`
			Tag     string            `json:"tag"`
			Text    string            `json:"text,omitempty"`
			Visible bool              `json:"visible"`
			Attrs   map[string]string `json:"attributes,omitempty"`
		}
		var matches []match
		total := 0
		for i, fr := range frames {
			loc := fr.Locator(args.Selector)
			n, err := loc.Count()
			if err != nil || n == 0 {
				continue
			}
			total += n
			for j := 0; j < n && len(matches) < args.Limit; j++ {
				el := loc.Nth(j)
				m := match{Tag: "?"}
				if i > 0 {
					m.Frame = fmt.Sprintf("frame[%d] %s", i, fr.URL())
				}
				if value, err := el.Evaluate(describeElementJS, nil); err == nil {
					var described struct {
						Tag   string            `json:"tag"`
						Text  string            `json:"text"`
						Attrs map[string]string `json:"attrs"`
					}
					if remarshal(value, &described) == nil {
						m.Tag, m.Text, m.Attrs = described.Tag, described.Text, described.Attrs
					}
				}
				m.Visible, _ = el.IsVisible()
				matches = append(matches, m)
			}
		}
		return toolResultJSON(map[string]any{
			"selector": args.Selector,
			"count":    total,
			"shown":    len(matches),
			"matches":  matches,
		}), nil
	})
}

// describeElementJS reports the few things about an element that matter when
// you are checking whether a selector found what you meant.
const describeElementJS = `el => {
  const attrs = {};
  for (const a of el.attributes) {
    if (a.name === 'style' || a.value.length > 200) continue;
    attrs[a.name] = a.value;
  }
  return {
    tag: el.tagName.toLowerCase(),
    text: (el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 200),
    attrs,
  };
}`

// writeScreenshot saves a capture, defaulting the name and the directory.
func (s *Server) writeScreenshot(path string, data []byte) (string, error) {
	dir := s.cfg.Browser.ScreenshotDir
	if path == "" {
		path = "screenshot-" + time.Now().Format("20060102-150405.000") + ".png"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if !strings.HasSuffix(strings.ToLower(path), ".png") {
		path += ".png"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating the screenshot directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("writing the screenshot: %w", err)
	}
	return path, nil
}
