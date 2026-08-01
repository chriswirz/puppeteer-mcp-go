package main

import (
	"context"
	"fmt"
	"sort"
)

// Prompts are the recurring workflows: the sequences worth having written down
// because getting the order wrong wastes a browser session. They are templates
// for the model, not commands - a client offers them to its user by name.

type promptDef struct {
	Prompt Prompt
	Render func(args map[string]string) string
}

func promptDefs() map[string]promptDef {
	return map[string]promptDef{
		"debug_page": {
			Prompt: Prompt{
				Name:        "debug_page",
				Title:       "Debug a page that is not working",
				Description: "Work out why a page is broken, using what it already reported rather than guesswork.",
				Arguments: []PromptArgument{
					{Name: "url", Description: "The page to look at.", Required: true},
					{Name: "symptom", Description: "What is going wrong, in the user's words."},
				},
			},
			Render: func(args map[string]string) string {
				symptom := args["symptom"]
				if symptom == "" {
					symptom = "(not described - find out what is wrong)"
				}
				return fmt.Sprintf(`Debug this page: %s
Reported symptom: %s

Work in this order, and stop as soon as the cause is clear:

1. browser_navigate to the URL. Read the status and anything reported back with it.
2. browser_console, level=error. An uncaught exception during load explains most
   "the page is blank" and "the button does nothing" reports outright.
3. browser_network with failures_only=true. A 404 on a script, a 500 from the API,
   a request blocked by CORS - each has a different fix and they look identical
   from the outside.
4. browser_snapshot. Compare what is actually on the page with what should be.
   If an element you expect is missing, that is the thread to pull.
5. Only if the problem is visual - layout, styling, something overlapping -
   take a browser_screenshot.

Then say what is wrong and where, quoting the error or the failed request.
If the cause is in code you can see, name the file and line.`, args["url"], symptom)
			},
		},

		"test_flow": {
			Prompt: Prompt{
				Name:        "test_flow",
				Title:       "Walk through a user flow",
				Description: "Drive a flow end to end and report what a person would experience.",
				Arguments: []PromptArgument{
					{Name: "url", Description: "Where the flow starts.", Required: true},
					{Name: "flow", Description: "The steps to carry out, in plain words.", Required: true},
				},
			},
			Render: func(args map[string]string) string {
				return fmt.Sprintf(`Walk through this flow and report what happens.

Start at: %s
The flow: %s

How to do it:
- browser_navigate, then browser_snapshot to see what is there. Act on the refs
  the snapshot gives you rather than inventing CSS selectors.
- browser_clear_events before each step you care about, so the console and network
  records you read afterwards belong to that step alone.
- After each step, take a new snapshot: refs from a previous one stop resolving
  once the page re-renders.
- Wait on conditions with browser_wait_for, not on delays.

Report, step by step: what you did, what the page did, and anything it logged that
a person would not see but would care about - an error swallowed by a catch, a
failed request the UI reports as "something went wrong". End with a plain verdict:
does the flow work, and if not, where does it break.`, args["url"], args["flow"])
			},
		},

		"debug_extension": {
			Prompt: Prompt{
				Name:        "debug_extension",
				Title:       "Debug a Chrome extension",
				Description: "Find out which part of an extension is failing - worker, content script, or page.",
				Arguments: []PromptArgument{
					{Name: "symptom", Description: "What the extension is doing wrong.", Required: true},
					{Name: "test_url", Description: "A page the extension should act on."},
				},
			},
			Render: func(args map[string]string) string {
				testURL := args["test_url"]
				if testURL == "" {
					testURL = "(pick one the extension's content script matches)"
				}
				return fmt.Sprintf(`Debug this Chrome extension.

Symptom: %s
Test page: %s

An extension is several programs that share a name and almost nothing else, so the
first job is to find out which one is failing:

1. browser_extensions. Confirm it is loaded and note the id. A stopped service
   worker is normal for MV3 - Chrome sleeps idle ones.
2. browser_extension_eval with "() => chrome.runtime.getManifest()". If this works,
   the worker runs and the chrome.* APIs are reachable; if it does not, the worker
   is failing to start and that is the whole bug.
3. Check the worker's own state: chrome.storage through browser_extension_storage,
   and whatever the worker keeps in memory through browser_extension_eval.
4. browser_navigate to the test page, then browser_console on it. A content script
   throws into the page's console, not the worker's - this is where a content
   script bug shows up.
5. If a popup or options page is involved, browser_extension_page opens it as an
   ordinary tab you can snapshot and read the console of. A real popup closes the
   moment it loses focus, which is why debugging one in place is hopeless.

After a code change: browser_extension_reload with reload_pages=true. The worker
picks up changes immediately, but a content script is only injected on page load.

Report which part is at fault and what it is doing wrong.`, args["symptom"], testURL)
			},
		},

		"check_responsive": {
			Prompt: Prompt{
				Name:        "check_responsive",
				Title:       "Check a page at several widths",
				Description: "Look for layout problems across phone, tablet and desktop widths.",
				Arguments: []PromptArgument{
					{Name: "url", Description: "The page to check.", Required: true},
				},
			},
			Render: func(args map[string]string) string {
				return fmt.Sprintf(`Check how this page holds up across widths: %s

For each of 390x844 (phone), 768x1024 (tablet) and 1440x900 (desktop):
- browser_set_viewport, then browser_navigate (or reload if already there, so the
  page lays out at the new size from scratch).
- browser_screenshot. This is a genuinely visual question, so the image earns its
  cost here.
- browser_evaluate "() => ({ overflow: document.documentElement.scrollWidth >
  window.innerWidth, width: document.documentElement.scrollWidth })" to catch the
  most common failure, a page that scrolls sideways.

Report per width: anything cut off, overlapping, unreadably small, or scrolling
horizontally. Name the element, not just the symptom.`, args["url"])
			},
		},
	}
}

func (s *Server) listPrompts(ctx context.Context) any {
	defs := promptDefs()
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	prompts := make([]Prompt, 0, len(names))
	for _, name := range names {
		prompts = append(prompts, defs[name].Prompt)
	}
	return &ListPromptsResult{
		Result:  s.completeResult(ctx).cacheable(3600000, CacheScopePublic),
		Prompts: prompts,
	}
}

func (s *Server) getPrompt(ctx context.Context, req *Request) (any, *RPCError) {
	var params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := req.Bind(&params); err != nil {
		return nil, err
	}
	def, ok := promptDefs()[params.Name]
	if !ok {
		return nil, Errorf(CodeInvalidParams, "unknown prompt: %s", params.Name)
	}
	for _, arg := range def.Prompt.Arguments {
		if arg.Required && params.Arguments[arg.Name] == "" {
			return nil, Errorf(CodeInvalidParams, "prompt %s needs the %s argument", params.Name, arg.Name)
		}
	}
	return &GetPromptResult{
		Result:      s.completeResult(ctx),
		Description: def.Prompt.Description,
		Messages: []PromptMessage{{
			Role:    "user",
			Content: Content{Type: "text", Text: def.Render(params.Arguments)},
		}},
	}, nil
}
