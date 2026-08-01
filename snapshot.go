package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// The snapshot is this server's answer to "what is on the page".
//
// A screenshot is the obvious answer and the wrong one most of the time: it
// costs a large image, it tells a model nothing it can act on, and the model
// then has to invent a CSS selector to click anything it saw. The snapshot
// instead walks the DOM, stamps every element worth naming with a ref
// attribute, and returns an indented outline of roles and names. The refs come
// back to browser_click and browser_type as-is, so there is no selector to
// guess at and no image to interpret.
//
// refAttr is the attribute the stamping uses. It is left on the elements
// deliberately: a ref stays valid until the page replaces the node, which is
// exactly the lifetime a caller expects.
const refAttr = "data-pmcp-ref"

// snapshotJS walks a document and returns a flat, ordered list of nodes with a
// depth on each, having stamped a ref onto every element it reports. It is
// passed the frame's ref prefix and the caps, and runs inside the page.
const snapshotJS = `(opts) => {
  const { prefix, maxNodes, visibleOnly } = opts;
  const out = [];
  let seq = 0;

  const isVisible = (el) => {
    if (!el.isConnected) return false;
    const style = getComputedStyle(el);
    if (style.visibility === 'hidden' || style.display === 'none' || style.opacity === '0') return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };

  // A coarse implicit role. The full ARIA algorithm is far more than is needed
  // here: the point is to tell a model what kind of thing it is looking at.
  const roleOf = (el) => {
    const explicit = el.getAttribute('role');
    if (explicit) return explicit;
    const tag = el.tagName.toLowerCase();
    switch (tag) {
      case 'a': return el.hasAttribute('href') ? 'link' : 'generic';
      case 'button': return 'button';
      case 'select': return el.multiple ? 'listbox' : 'combobox';
      case 'textarea': return 'textbox';
      case 'img': return 'image';
      case 'nav': return 'navigation';
      case 'main': return 'main';
      case 'header': return 'banner';
      case 'footer': return 'contentinfo';
      case 'aside': return 'complementary';
      case 'form': return 'form';
      case 'table': return 'table';
      case 'ul': case 'ol': return 'list';
      case 'li': return 'listitem';
      case 'summary': return 'summary';
      case 'dialog': return 'dialog';
      case 'iframe': return 'iframe';
      case 'label': return 'label';
      case 'h1': case 'h2': case 'h3': case 'h4': case 'h5': case 'h6': return 'heading';
      case 'input': {
        const t = (el.type || 'text').toLowerCase();
        if (t === 'checkbox') return 'checkbox';
        if (t === 'radio') return 'radio';
        if (t === 'submit' || t === 'button' || t === 'reset') return 'button';
        if (t === 'range') return 'slider';
        if (t === 'file') return 'file-input';
        if (t === 'hidden') return '';
        return 'textbox';
      }
    }
    return '';
  };

  // The accessible name, near enough: what a person would call this element.
  const nameOf = (el) => {
    const aria = el.getAttribute('aria-label');
    if (aria) return aria.trim();
    const labelledBy = el.getAttribute('aria-labelledby');
    if (labelledBy) {
      const parts = labelledBy.split(/\s+/)
        .map((id) => (el.ownerDocument.getElementById(id) || {}).textContent || '')
        .join(' ').trim();
      if (parts) return parts;
    }
    if (el.labels && el.labels.length) {
      const t = Array.from(el.labels).map((l) => l.textContent).join(' ').trim();
      if (t) return t;
    }
    const tag = el.tagName.toLowerCase();
    if (tag === 'img') return (el.getAttribute('alt') || '').trim();
    if (tag === 'input' || tag === 'textarea') {
      return (el.getAttribute('placeholder') || el.getAttribute('name') || el.getAttribute('title') || '').trim();
    }
    if (tag === 'iframe') return (el.getAttribute('name') || el.getAttribute('src') || '').trim();
    // Own text only: an ancestor's text would name every element the same.
    let text = '';
    for (const node of el.childNodes) {
      if (node.nodeType === 3) text += node.textContent;
    }
    text = text.replace(/\s+/g, ' ').trim();
    if (!text && el.children.length === 0) {
      text = (el.textContent || '').replace(/\s+/g, ' ').trim();
    }
    return text;
  };

  const interesting = (el, role) => {
    if (role) return true;
    if (el.hasAttribute('onclick') || el.hasAttribute('tabindex')) return true;
    if (el.getAttribute('contenteditable') === 'true') return true;
    // A leaf carrying text is worth reporting; a wrapper div is not.
    return el.children.length === 0 && (el.textContent || '').trim().length > 0;
  };

  const stateOf = (el) => {
    const bits = [];
    if (el.disabled) bits.push('disabled');
    if (el.checked) bits.push('checked');
    if (el.readOnly) bits.push('readonly');
    if (el.required) bits.push('required');
    if (el.getAttribute('aria-expanded') === 'true') bits.push('expanded');
    if (el.getAttribute('aria-selected') === 'true') bits.push('selected');
    if (el === el.ownerDocument.activeElement) bits.push('focused');
    return bits;
  };

  const walk = (el, depth) => {
    if (out.length >= maxNodes) return;
    const tag = el.tagName.toLowerCase();
    if (tag === 'script' || tag === 'style' || tag === 'noscript' || tag === 'template') return;
    if (el.getAttribute('aria-hidden') === 'true') return;
    if (visibleOnly && !isVisible(el)) return;

    const role = roleOf(el);
    let childDepth = depth;
    if (interesting(el, role)) {
      const ref = prefix + (++seq);
      el.setAttribute('` + refAttr + `', ref);
      const node = { ref, depth, tag, role, name: nameOf(el).slice(0, 200) };
      if (tag === 'input' || tag === 'textarea' || tag === 'select') {
        node.value = String(el.value == null ? '' : el.value).slice(0, 200);
      }
      if (tag === 'a' && el.href) node.href = el.href;
      if (role === 'heading') node.level = Number(tag.slice(1)) || Number(el.getAttribute('aria-level')) || 0;
      const state = stateOf(el);
      if (state.length) node.state = state;
      out.push(node);
      childDepth = depth + 1;
    }
    for (const child of el.children) walk(child, childDepth);
  };

  if (document.documentElement) walk(document.body || document.documentElement, 0);
  return { nodes: out, truncated: out.length >= maxNodes, title: document.title, url: location.href };
}`

// snapshotNode is one element in a snapshot.
type snapshotNode struct {
	Ref   string   `json:"ref"`
	Depth int      `json:"depth"`
	Tag   string   `json:"tag"`
	Role  string   `json:"role"`
	Name  string   `json:"name"`
	Value string   `json:"value,omitempty"`
	Href  string   `json:"href,omitempty"`
	Level int      `json:"level,omitempty"`
	State []string `json:"state,omitempty"`
}

// frameSnapshot is one document's worth of snapshot.
type frameSnapshot struct {
	Frame     string         `json:"frame"`
	URL       string         `json:"url"`
	Title     string         `json:"title,omitempty"`
	Nodes     []snapshotNode `json:"nodes"`
	Truncated bool           `json:"truncated,omitempty"`
}

// snapshotOptions bound what a snapshot covers.
type snapshotOptions struct {
	AllFrames   bool
	VisibleOnly bool
	MaxNodes    int
}

// takeSnapshot walks the page - and, when asked, every frame in it - stamping
// refs and collecting nodes.
//
// Refs are prefixed per frame ("e12" in the main document, "f2e12" in the
// second frame) so that one ref names one element across the whole page, and
// resolving it is a matter of finding the frame that has it.
func takeSnapshot(st *PageState, opts snapshotOptions) ([]frameSnapshot, error) {
	if opts.MaxNodes <= 0 {
		opts.MaxNodes = 800
	}
	seq := st.NextRefSeq()

	frames := []playwright.Frame{st.Page.MainFrame()}
	if opts.AllFrames {
		frames = st.Page.Frames()
	}

	var out []frameSnapshot
	for i, fr := range frames {
		prefix := fmt.Sprintf("s%de", seq)
		name := "main"
		if i > 0 || fr != st.Page.MainFrame() {
			prefix = fmt.Sprintf("s%df%de", seq, i)
			name = fmt.Sprintf("frame[%d] %s", i, fr.URL())
		}
		raw, err := fr.Evaluate(snapshotJS, map[string]any{
			"prefix":      prefix,
			"maxNodes":    opts.MaxNodes,
			"visibleOnly": opts.VisibleOnly,
		})
		if err != nil {
			// A frame can be cross-origin, detached mid-walk, or still blank.
			// One unreadable frame is not a reason to fail the whole snapshot,
			// so it is reported in place and the walk continues.
			out = append(out, frameSnapshot{Frame: name, URL: fr.URL(),
				Nodes: []snapshotNode{{Role: "error", Name: "could not read this frame: " + err.Error()}}})
			continue
		}
		var decoded struct {
			Nodes     []snapshotNode `json:"nodes"`
			Truncated bool           `json:"truncated"`
			Title     string         `json:"title"`
			URL       string         `json:"url"`
		}
		if err := remarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("decoding the snapshot of %s: %w", name, err)
		}
		if len(decoded.Nodes) == 0 && i > 0 {
			continue // an empty subframe is noise
		}
		out = append(out, frameSnapshot{
			Frame:     name,
			URL:       decoded.URL,
			Title:     decoded.Title,
			Nodes:     decoded.Nodes,
			Truncated: decoded.Truncated,
		})
	}
	return out, nil
}

// renderSnapshot formats the snapshot as the indented outline a model reads.
func renderSnapshot(frames []frameSnapshot) string {
	var b strings.Builder
	for i, fr := range frames {
		if i > 0 {
			b.WriteString("\n")
		}
		if fr.Title != "" {
			fmt.Fprintf(&b, "# %s - %s\n", fr.Title, fr.URL)
		} else {
			fmt.Fprintf(&b, "# %s\n", fr.URL)
		}
		if fr.Frame != "main" {
			fmt.Fprintf(&b, "# in %s\n", fr.Frame)
		}
		for _, n := range fr.Nodes {
			b.WriteString(strings.Repeat("  ", n.Depth))
			b.WriteString("- ")
			role := n.Role
			if role == "" {
				role = n.Tag
			}
			b.WriteString(role)
			if n.Level > 0 {
				fmt.Fprintf(&b, " %d", n.Level)
			}
			if n.Name != "" {
				fmt.Fprintf(&b, " %q", n.Name)
			}
			if n.Value != "" {
				fmt.Fprintf(&b, " value=%q", n.Value)
			}
			if len(n.State) > 0 {
				fmt.Fprintf(&b, " [%s]", strings.Join(n.State, " "))
			}
			if n.Ref != "" {
				fmt.Fprintf(&b, "  ref=%s", n.Ref)
			}
			if n.Href != "" && n.Href != n.Name {
				fmt.Fprintf(&b, "  -> %s", truncate(n.Href, 120))
			}
			b.WriteString("\n")
		}
		if fr.Truncated {
			b.WriteString("... (truncated; raise max_nodes or narrow with visible_only)\n")
		}
	}
	return b.String()
}

// target names the element a tool acts on: a ref from a snapshot, or a
// selector when the caller already knows the markup.
type target struct {
	Ref      string `json:"ref"`
	Selector string `json:"selector"`
	Frame    string `json:"frame"`
	Index    int    `json:"index"`
}

// resolve turns a target into a locator, searching every frame when the
// element is named by ref.
//
// Scanning frames rather than tracking which frame a ref came from is
// deliberate: a page can replace its frames between the snapshot and the
// click, and a ref that has moved is better found than reported missing.
func (t target) resolve(st *PageState) (playwright.Locator, error) {
	if t.Ref == "" && t.Selector == "" {
		return nil, fmt.Errorf("name the element with either ref (from browser_snapshot) or selector")
	}
	if t.Ref != "" && t.Selector != "" {
		return nil, fmt.Errorf("give ref or selector, not both")
	}

	selector := t.Selector
	byRef := t.Ref != ""
	if byRef {
		selector = fmt.Sprintf("[%s=%q]", refAttr, t.Ref)
	}

	// An explicit frame selector pins the search to one iframe.
	if t.Frame != "" {
		loc := st.Page.FrameLocator(t.Frame).Locator(selector)
		return t.nth(loc), nil
	}

	frames := st.Page.Frames()
	for _, fr := range frames {
		loc := fr.Locator(selector)
		// Count does not wait, so a frame still loading reports zero and the
		// search moves on rather than spending the whole timeout there.
		n, err := loc.Count()
		if err != nil || n == 0 {
			continue
		}
		return t.nth(loc), nil
	}

	if byRef {
		return nil, fmt.Errorf("ref %s is not on the page any more, in any of its %d frames. "+
			"The page has probably re-rendered since the snapshot; take a new browser_snapshot and use the ref from that",
			t.Ref, len(frames))
	}
	// A selector that matches nothing right now may still be about to appear,
	// so it is handed to Playwright to wait on rather than refused here.
	return t.nth(st.Page.MainFrame().Locator(selector)), nil
}

func (t target) nth(loc playwright.Locator) playwright.Locator {
	if t.Index > 0 {
		return loc.Nth(t.Index)
	}
	return loc.First()
}

// describe is how the target reads in a message about it.
func (t target) describe() string {
	switch {
	case t.Ref != "":
		return "ref " + t.Ref
	case t.Selector != "":
		return "selector " + strconv.Quote(t.Selector)
	default:
		return "element"
	}
}

// remarshal moves a value decoded as any into a typed struct.
func remarshal(from any, to any) error {
	data, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, to)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
