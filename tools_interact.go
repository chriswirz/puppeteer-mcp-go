package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// elementArgs are the arguments every tool that acts on an element shares.
type elementArgs struct {
	Ref       string `json:"ref"`
	Selector  string `json:"selector"`
	Frame     string `json:"frame"`
	Index     int    `json:"index"`
	TimeoutMs int    `json:"timeout_ms"`
	PageID    string `json:"page_id"`
}

func (a elementArgs) target() target {
	return target{Ref: a.Ref, Selector: a.Selector, Frame: a.Frame, Index: a.Index}
}

// elementProps is the shared half of those tools' input schemas.
func elementProps(extra map[string]any) map[string]any {
	props := map[string]any{
		"ref": prop("string", "Element ref from browser_snapshot. This is the reliable way to name an element: "+
			"it came from the page itself rather than being guessed."),
		"selector": prop("string", "CSS selector, when you already know the markup. Playwright's other engines "+
			"work too: text=Save, role=button[name=\"Save\"], xpath=//button."),
		"frame": prop("string", "CSS selector for an iframe to search inside. Omitted means search every frame, "+
			"which is normally what you want."),
		"index":      propDefault("integer", "Which match to act on when the selector matches several. 0 is the first.", 0),
		"timeout_ms": prop("integer", "How long to wait for the element before giving up."),
		"page_id":    prop("string", "Page to act on. Omitted means the current page."),
	}
	for k, v := range extra {
		props[k] = v
	}
	return props
}

// registerInteractionTools adds the tools that do something to the page.
func (s *Server) registerInteractionTools() {
	s.RegisterTool(Tool{
		Name:  "browser_click",
		Title: "Click an element",
		Description: "Click an element, naming it by the ref from a snapshot or by a selector. The element is " +
			"scrolled into view and waited for, so a click on something not yet rendered is a wait rather than " +
			"a failure. Anything the page logged as a result comes back with the answer.",
		InputSchema: schema(nil, elementProps(map[string]any{
			"button":       map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}, "description": "Mouse button. Default left.", "default": "left"},
			"click_count":  propDefault("integer", "2 for a double click.", 1),
			"modifiers":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"Alt", "Control", "Meta", "Shift"}}, "description": "Modifier keys to hold."},
			"force":        propDefault("boolean", "Click even if the element is covered or not considered actionable. A last resort: a covered element usually means something else is wrong.", false),
			"clear_events": propDefault("boolean", "Empty the console and network buffers first, so what comes back is only about this click.", false),
		})),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			Button      string   `json:"button"`
			ClickCount  int      `json:"click_count"`
			Modifiers   []string `json:"modifiers"`
			Force       bool     `json:"force"`
			ClearEvents bool     `json:"clear_events"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, loc, bad := s.locate(args.elementArgs)
		if bad != nil {
			return bad, nil
		}
		mark := st.Mark()
		if args.ClearEvents {
			st.ClearEvents()
		}
		opts := playwright.LocatorClickOptions{Timeout: s.timeout(args.TimeoutMs)}
		if args.Button == "right" {
			opts.Button = playwright.MouseButtonRight
		} else if args.Button == "middle" {
			opts.Button = playwright.MouseButtonMiddle
		}
		if args.ClickCount > 1 {
			opts.ClickCount = playwright.Int(args.ClickCount)
		}
		if args.Force {
			opts.Force = playwright.Bool(true)
		}
		for _, m := range args.Modifiers {
			opts.Modifiers = append(opts.Modifiers, playwright.KeyboardModifier(m))
		}
		if err := loc.Click(opts); err != nil {
			return toolError("clicking %s: %v%s", args.target().describe(), err, suffixTrouble(st, mark)), nil
		}
		return toolResult(fmt.Sprintf("Clicked %s. The page is at %s%s",
			args.target().describe(), st.Page.URL(), suffixTrouble(st, mark))), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_type",
		Title: "Type into a field",
		Description: "Put text into an input, textarea or contenteditable element. By default the field is " +
			"cleared and the value set in one go, which is fast and reliable; set typing=true to send real " +
			"keystrokes instead, which is what you need when the page reacts to each key - an autocomplete, a " +
			"validation-as-you-type field, a key handler you are debugging.",
		InputSchema: schema([]string{"text"}, elementProps(map[string]any{
			"text":   prop("string", "The text to enter."),
			"typing": propDefault("boolean", "Send individual keystrokes rather than setting the value.", false),
			"delay_ms": propDefault("integer",
				"Delay between keystrokes when typing. Only meaningful with typing=true.", 0),
			"clear":  propDefault("boolean", "Clear the field first.", true),
			"submit": propDefault("boolean", "Press Enter afterwards.", false),
		})),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			Text    string `json:"text"`
			Typing  bool   `json:"typing"`
			DelayMs int    `json:"delay_ms"`
			Clear   *bool  `json:"clear"`
			Submit  bool   `json:"submit"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, loc, bad := s.locate(args.elementArgs)
		if bad != nil {
			return bad, nil
		}
		mark := st.Mark()
		timeout := s.timeout(args.TimeoutMs)
		if args.Typing {
			if args.Clear == nil || *args.Clear {
				if err := loc.Fill("", playwright.LocatorFillOptions{Timeout: timeout}); err != nil {
					return toolError("clearing %s: %v", args.target().describe(), err), nil
				}
			}
			opts := playwright.LocatorPressSequentiallyOptions{Timeout: timeout}
			if args.DelayMs > 0 {
				opts.Delay = playwright.Float(float64(args.DelayMs))
			}
			if err := loc.PressSequentially(args.Text, opts); err != nil {
				return toolError("typing into %s: %v%s", args.target().describe(), err, suffixTrouble(st, mark)), nil
			}
		} else {
			if err := loc.Fill(args.Text, playwright.LocatorFillOptions{Timeout: timeout}); err != nil {
				return toolError("filling %s: %v%s", args.target().describe(), err, suffixTrouble(st, mark)), nil
			}
		}
		if args.Submit {
			if err := loc.Press("Enter", playwright.LocatorPressOptions{Timeout: timeout}); err != nil {
				return toolError("pressing Enter in %s: %v", args.target().describe(), err), nil
			}
		}
		return toolResult(fmt.Sprintf("Entered %d characters into %s.%s",
			len(args.Text), args.target().describe(), suffixTrouble(st, mark))), nil
	})

	s.RegisterTool(Tool{
		Name:        "browser_press_key",
		Title:       "Press a key",
		Description: "Send a key to an element, or to the page when no element is named. Keys are Playwright names: Enter, Escape, Tab, ArrowDown, Control+A, Shift+Tab.",
		InputSchema: schema([]string{"key"}, elementProps(map[string]any{
			"key": prop("string", "Key or chord to press, e.g. Enter, Escape, Control+A."),
		})),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			Key string `json:"key"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if args.Key == "" {
			return toolError("key is required"), nil
		}
		if args.Ref == "" && args.Selector == "" {
			st, err := s.browser.PageByID(args.PageID)
			if err != nil {
				return toolError("%v", err), nil
			}
			mark := st.Mark()
			if err := st.Page.Keyboard().Press(args.Key); err != nil {
				return toolError("pressing %s: %v", args.Key, err), nil
			}
			return toolResult(fmt.Sprintf("Pressed %s.%s", args.Key, suffixTrouble(st, mark))), nil
		}
		st, loc, bad := s.locate(args.elementArgs)
		if bad != nil {
			return bad, nil
		}
		mark := st.Mark()
		if err := loc.Press(args.Key, playwright.LocatorPressOptions{Timeout: s.timeout(args.TimeoutMs)}); err != nil {
			return toolError("pressing %s in %s: %v", args.Key, args.target().describe(), err), nil
		}
		return toolResult(fmt.Sprintf("Pressed %s in %s.%s", args.Key, args.target().describe(), suffixTrouble(st, mark))), nil
	})

	s.RegisterTool(Tool{
		Name:        "browser_hover",
		Title:       "Hover over an element",
		Description: "Move the pointer over an element, which is how a menu or tooltip that only appears on hover is opened.",
		InputSchema: schema(nil, elementProps(nil)),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args elementArgs
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, loc, bad := s.locate(args)
		if bad != nil {
			return bad, nil
		}
		mark := st.Mark()
		if err := loc.Hover(playwright.LocatorHoverOptions{Timeout: s.timeout(args.TimeoutMs)}); err != nil {
			return toolError("hovering over %s: %v", args.target().describe(), err), nil
		}
		return toolResult("Hovering over " + args.target().describe() + "." + suffixTrouble(st, mark)), nil
	})

	s.RegisterTool(Tool{
		Name:        "browser_select_option",
		Title:       "Choose from a dropdown",
		Description: "Select one or more options in a <select> element, by their visible label or their value.",
		InputSchema: schema(nil, elementProps(map[string]any{
			"labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Option labels to select, as shown to a person."},
			"values": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Option values to select, as in the HTML."},
		})),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			Labels []string `json:"labels"`
			Values []string `json:"values"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if len(args.Labels) == 0 && len(args.Values) == 0 {
			return toolError("give labels or values to select"), nil
		}
		st, loc, bad := s.locate(args.elementArgs)
		if bad != nil {
			return bad, nil
		}
		mark := st.Mark()
		selected, err := loc.SelectOption(playwright.SelectOptionValues{
			Labels: &args.Labels,
			Values: &args.Values,
		}, playwright.LocatorSelectOptionOptions{Timeout: s.timeout(args.TimeoutMs)})
		if err != nil {
			return toolError("selecting in %s: %v", args.target().describe(), err), nil
		}
		return toolResult(fmt.Sprintf("Selected %s.%s", strings.Join(selected, ", "), suffixTrouble(st, mark))), nil
	})

	s.RegisterTool(Tool{
		Name:        "browser_check",
		Title:       "Check or uncheck a box",
		Description: "Set a checkbox or radio button to a state. Unlike a click this is idempotent: asking for checked=true on an already-checked box does nothing rather than toggling it off.",
		InputSchema: schema(nil, elementProps(map[string]any{
			"checked": propDefault("boolean", "The state to leave it in.", true),
		})),
		Annotations: &ToolAnnotations{IdempotentHint: true},
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			Checked *bool `json:"checked"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		st, loc, bad := s.locate(args.elementArgs)
		if bad != nil {
			return bad, nil
		}
		mark := st.Mark()
		want := args.Checked == nil || *args.Checked
		var err error
		if want {
			err = loc.Check(playwright.LocatorCheckOptions{Timeout: s.timeout(args.TimeoutMs)})
		} else {
			err = loc.Uncheck(playwright.LocatorUncheckOptions{Timeout: s.timeout(args.TimeoutMs)})
		}
		if err != nil {
			return toolError("setting %s to checked=%v: %v", args.target().describe(), want, err), nil
		}
		return toolResult(fmt.Sprintf("%s is now checked=%v.%s", args.target().describe(), want, suffixTrouble(st, mark))), nil
	})

	s.RegisterTool(Tool{
		Name:        "browser_upload_file",
		Title:       "Attach a file",
		Description: "Set the files on a file input. Give absolute paths to files on the machine this server runs on.",
		InputSchema: schema([]string{"paths"}, elementProps(map[string]any{
			"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Absolute paths of the files to attach."},
		})),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			Paths []string `json:"paths"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if len(args.Paths) == 0 {
			return toolError("paths is required"), nil
		}
		st, loc, bad := s.locate(args.elementArgs)
		if bad != nil {
			return bad, nil
		}
		mark := st.Mark()
		if err := loc.SetInputFiles(args.Paths, playwright.LocatorSetInputFilesOptions{
			Timeout: s.timeout(args.TimeoutMs),
		}); err != nil {
			return toolError("attaching %s: %v", strings.Join(args.Paths, ", "), err), nil
		}
		return toolResult(fmt.Sprintf("Attached %d file(s).%s", len(args.Paths), suffixTrouble(st, mark))), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_scroll",
		Title: "Scroll",
		Description: "Scroll the page by an amount, or scroll an element into view. Scrolling to an element is " +
			"usually the better call: clicks scroll on their own, so the reason to scroll explicitly is to make " +
			"something visible for a screenshot or to trigger lazy loading.",
		InputSchema: schema(nil, elementProps(map[string]any{
			"dy": propDefault("integer", "Pixels to scroll vertically; negative scrolls up. Ignored when an element is named.", 0),
			"dx": propDefault("integer", "Pixels to scroll horizontally.", 0),
			"to": map[string]any{"type": "string", "enum": []string{"top", "bottom"}, "description": "Jump to the top or bottom of the page."},
		})),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			elementArgs
			DX int    `json:"dx"`
			DY int    `json:"dy"`
			To string `json:"to"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if args.Ref != "" || args.Selector != "" {
			_, loc, bad := s.locate(args.elementArgs)
			if bad != nil {
				return bad, nil
			}
			if err := loc.ScrollIntoViewIfNeeded(playwright.LocatorScrollIntoViewIfNeededOptions{
				Timeout: s.timeout(args.TimeoutMs),
			}); err != nil {
				return toolError("scrolling %s into view: %v", args.target().describe(), err), nil
			}
			return toolResult("Scrolled " + args.target().describe() + " into view."), nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		switch args.To {
		case "top":
			_, err = st.Page.Evaluate("() => { window.scrollTo(0, 0); return [window.scrollX, window.scrollY]; }")
		case "bottom":
			_, err = st.Page.Evaluate("() => { window.scrollTo(0, document.body.scrollHeight); return [window.scrollX, window.scrollY]; }")
		default:
			if args.DX == 0 && args.DY == 0 {
				return toolError("give dx/dy, to, or an element to scroll into view"), nil
			}
			err = st.Page.Mouse().Wheel(float64(args.DX), float64(args.DY))
		}
		if err != nil {
			return toolError("scrolling: %v", err), nil
		}
		pos, _ := st.Page.Evaluate("() => ({ x: window.scrollX, y: window.scrollY, height: document.body.scrollHeight })")
		return toolResultJSON(map[string]any{"scrolled": true, "position": pos}), nil
	})

	s.RegisterTool(Tool{
		Name:  "browser_set_viewport",
		Title: "Resize the viewport",
		Description: "Set the page's viewport size, which is how you check a responsive layout at a phone or " +
			"tablet width without resizing anything by hand.",
		InputSchema: schema([]string{"width", "height"}, map[string]any{
			"width":   prop("integer", "Viewport width in CSS pixels."),
			"height":  prop("integer", "Viewport height in CSS pixels."),
			"page_id": prop("string", "Page to resize. Omitted means the current page."),
		}),
	}, func(ctx context.Context, raw json.RawMessage) (*CallToolResult, *RPCError) {
		var args struct {
			Width  int    `json:"width"`
			Height int    `json:"height"`
			PageID string `json:"page_id"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return bad, nil
		}
		if args.Width <= 0 || args.Height <= 0 {
			return toolError("width and height must both be positive"), nil
		}
		st, err := s.browser.PageByID(args.PageID)
		if err != nil {
			return toolError("%v", err), nil
		}
		if err := st.Page.SetViewportSize(args.Width, args.Height); err != nil {
			return toolError("resizing the viewport: %v", err), nil
		}
		return toolResult(fmt.Sprintf("Viewport is now %dx%d.", args.Width, args.Height)), nil
	})
}

// locate resolves the page and the element for a tool call, reporting either
// failure as a tool error the model can act on.
func (s *Server) locate(args elementArgs) (*PageState, playwright.Locator, *CallToolResult) {
	st, err := s.browser.PageByID(args.PageID)
	if err != nil {
		return nil, nil, toolError("%v", err)
	}
	loc, err := args.target().resolve(st)
	if err != nil {
		return nil, nil, toolError("%v", err)
	}
	return st, loc, nil
}

// timeout is the wait for one call: the caller's, or the configured default.
func (s *Server) timeout(ms int) *float64 {
	if ms > 0 {
		return playwright.Float(float64(ms))
	}
	return playwright.Float(float64(s.cfg.Browser.DefaultTimeoutMs))
}
