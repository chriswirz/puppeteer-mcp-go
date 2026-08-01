package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/playwright-community/playwright-go"
)

// Browser owns the one browser this server drives, the pages open in it, and
// the events each page has reported.
//
// It is started lazily. A model that calls browser_status first should get an
// answer without a Chrome window appearing, and an operator who starts the
// server from an editor's MCP config does not want a browser launched at boot
// and left running all day. The first tool that actually needs a page brings
// the browser up.
type Browser struct {
	cfg    BrowserConfig
	logger *log.Logger

	mu      sync.Mutex
	pw      *playwright.Playwright
	browser playwright.Browser // set only when attached over CDP
	ctx     playwright.BrowserContext

	pages   map[string]*PageState
	order   []string // page ids in the order they were seen
	current string
	nextID  int

	mode      string // "attached" or "launched"
	startedAt time.Time
}

// PageState is one page and everything this server remembers about it.
type PageState struct {
	ID   string
	Page playwright.Page

	mu       sync.Mutex
	console  []ConsoleEntry
	errors   []ErrorEntry
	network  []NetworkEntry
	dialogs  []string
	capacity int

	// refSeq numbers the snapshots taken of this page, so refs from an old
	// snapshot are recognisably stale rather than silently matching a
	// different element after the page has changed.
	refSeq int
}

// ConsoleEntry is one message the page logged.
type ConsoleEntry struct {
	Time     string `json:"time"`
	Type     string `json:"type"`
	Text     string `json:"text"`
	Location string `json:"location,omitempty"`
}

// ErrorEntry is one uncaught exception from the page.
type ErrorEntry struct {
	Time  string `json:"time"`
	Error string `json:"error"`
}

// NetworkEntry is one request the page made, recorded when it settles.
type NetworkEntry struct {
	Time     string `json:"time"`
	Method   string `json:"method"`
	URL      string `json:"url"`
	Status   int    `json:"status,omitempty"`
	Type     string `json:"resource_type,omitempty"`
	Failure  string `json:"failure,omitempty"`
	FromCach bool   `json:"from_cache,omitempty"`
}

// NewBrowser prepares the session without starting anything.
func NewBrowser(cfg BrowserConfig, logger *log.Logger) *Browser {
	return &Browser{
		cfg:    cfg,
		logger: logger,
		pages:  make(map[string]*PageState),
	}
}

// Running reports whether the browser has been started.
func (b *Browser) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ctx != nil
}

// Mode is "attached", "launched" or "" when nothing is running yet.
func (b *Browser) Mode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mode
}

// Context returns the running browser context, starting the browser if needed.
func (b *Browser) Context() (playwright.BrowserContext, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.startLocked(); err != nil {
		return nil, err
	}
	return b.ctx, nil
}

// Start brings the browser up if it is not already.
func (b *Browser) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startLocked()
}

func (b *Browser) startLocked() error {
	if b.ctx != nil {
		return nil
	}
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("starting playwright (run `go run github.com/playwright-community/playwright-go/cmd/playwright@latest install --with-deps chromium` once, or set browser.channel to a Chrome you already have): %w", err)
	}
	b.pw = pw

	if b.cfg.CDPURL != "" {
		err = b.attachLocked()
	} else {
		err = b.launchLocked()
	}
	if err != nil {
		pw.Stop()
		b.pw = nil
		return err
	}
	b.startedAt = time.Now()
	b.adoptExistingLocked()
	// Pages the browser opens later - a target=_blank link, a popup, a tab the
	// operator opens by hand in attach mode - are tracked from the moment they
	// appear, so their console output is complete rather than starting
	// wherever the model happened to look.
	b.ctx.OnPage(func(p playwright.Page) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.trackLocked(p)
	})
	return nil
}

// attachLocked connects to a Chrome that is already running with
// --remote-debugging-port.
func (b *Browser) attachLocked() error {
	// Chrome's DevTools endpoint binds to 127.0.0.1 only, and "localhost" can
	// resolve to ::1 first and be refused. Normalize to the IPv4 loopback.
	endpoint := strings.Replace(b.cfg.CDPURL, "localhost", "127.0.0.1", 1)
	br, err := b.pw.Chromium.ConnectOverCDP(endpoint)
	if err != nil {
		return fmt.Errorf("connecting to Chrome at %s: %w\n\n"+
			"Start Chrome with a debugging port and its own profile directory, for example:\n"+
			"  chrome.exe --remote-debugging-port=9222 --user-data-dir=%q\n"+
			"The --user-data-dir is not optional: without it Chrome hands the command to an "+
			"already-running instance and never opens the port",
			endpoint, err, b.cfg.ProfileDir)
	}
	b.browser = br
	contexts := br.Contexts()
	if len(contexts) == 0 {
		ctx, err := br.NewContext()
		if err != nil {
			br.Close()
			b.browser = nil
			return fmt.Errorf("opening a context on the attached browser: %w", err)
		}
		b.ctx = ctx
	} else {
		b.ctx = contexts[0]
	}
	b.mode = "attached"
	b.applyTimeoutsLocked()
	return nil
}

// launchLocked starts a browser of our own against the persistent profile.
func (b *Browser) launchLocked() error {
	if err := os.MkdirAll(b.cfg.ProfileDir, 0o755); err != nil {
		return fmt.Errorf("creating the profile directory: %w", err)
	}
	opts := playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(b.cfg.Headless),
		Args:     b.launchArgs(),
	}
	if b.cfg.Channel != "" && b.cfg.Channel != "chromium" {
		opts.Channel = playwright.String(b.cfg.Channel)
	}
	if b.cfg.ExecutablePath != "" {
		opts.ExecutablePath = playwright.String(b.cfg.ExecutablePath)
		opts.Channel = nil
	}
	if len(b.cfg.IgnoreDefaultArgs) > 0 {
		opts.IgnoreDefaultArgs = b.cfg.IgnoreDefaultArgs
	}
	if b.cfg.UserAgent != "" {
		opts.UserAgent = playwright.String(b.cfg.UserAgent)
	}
	if b.cfg.SlowMoMs > 0 {
		opts.SlowMo = playwright.Float(float64(b.cfg.SlowMoMs))
	}
	if b.cfg.ViewportWidth > 0 && b.cfg.ViewportHeight > 0 {
		opts.Viewport = &playwright.Size{Width: b.cfg.ViewportWidth, Height: b.cfg.ViewportHeight}
	}
	if b.cfg.AllowDownloads {
		opts.AcceptDownloads = playwright.Bool(true)
		opts.DownloadsPath = playwright.String(b.cfg.DownloadDir)
	}

	ctx, err := b.pw.Chromium.LaunchPersistentContext(b.cfg.ProfileDir, opts)
	if err != nil && opts.Channel != nil {
		// The named Chrome may not be installed. Falling back to Playwright's
		// bundled Chromium keeps the server usable, but say so: an extension
		// or a codec that works in Chrome may not work here.
		b.logger.Printf("channel %q could not be launched (%v); falling back to the bundled Chromium", b.cfg.Channel, err)
		opts.Channel = nil
		ctx, err = b.pw.Chromium.LaunchPersistentContext(b.cfg.ProfileDir, opts)
	}
	if err != nil {
		return fmt.Errorf("launching the browser: %w", err)
	}
	b.ctx = ctx
	b.mode = "launched"
	b.applyTimeoutsLocked()
	return nil
}

// launchArgs is the Chrome command line: the automation flags dropped, the
// extensions loaded, and whatever the operator added.
func (b *Browser) launchArgs() []string {
	args := []string{"--disable-blink-features=AutomationControlled"}
	if len(b.cfg.Extensions) > 0 {
		joined := strings.Join(b.cfg.Extensions, ",")
		// Both flags are needed: --load-extension installs it,
		// --disable-extensions-except keeps Chrome from disabling everything
		// else it would otherwise consider unrequested.
		args = append(args,
			"--disable-extensions-except="+joined,
			"--load-extension="+joined,
		)
	}
	return append(args, b.cfg.Args...)
}

func (b *Browser) applyTimeoutsLocked() {
	b.ctx.SetDefaultTimeout(float64(b.cfg.DefaultTimeoutMs))
	b.ctx.SetDefaultNavigationTimeout(float64(b.cfg.NavigationTimeoutMs))
}

// adoptExistingLocked registers the pages that were already open, which in
// attach mode is every tab in the operator's window.
func (b *Browser) adoptExistingLocked() {
	for _, p := range b.ctx.Pages() {
		b.trackLocked(p)
	}
	if len(b.order) == 0 {
		// A launched persistent context normally comes with one blank page, but
		// not always; an attached browser with no pages is possible too.
		if p, err := b.ctx.NewPage(); err == nil {
			b.trackLocked(p)
		}
	}
}

// trackLocked starts recording a page's events and makes it the current page
// if there is none.
func (b *Browser) trackLocked(p playwright.Page) {
	for _, st := range b.pages {
		if st.Page == p {
			return
		}
	}
	b.nextID++
	st := &PageState{
		ID:       "page-" + strconv.Itoa(b.nextID),
		Page:     p,
		capacity: b.cfg.EventBufferSize,
	}
	b.pages[st.ID] = st
	b.order = append(b.order, st.ID)
	if b.current == "" {
		b.current = st.ID
	}
	b.recordEvents(st)
}

// recordEvents wires the page's event handlers into its ring buffers. This is
// what makes browser_console and browser_network answer about what already
// happened, instead of asking the model to reproduce a failure while watching.
func (b *Browser) recordEvents(st *PageState) {
	p := st.Page

	p.OnConsole(func(m playwright.ConsoleMessage) {
		entry := ConsoleEntry{Time: nowStamp(), Type: m.Type(), Text: m.Text()}
		if loc := m.Location(); loc != nil && loc.URL != "" {
			entry.Location = fmt.Sprintf("%s:%d:%d", loc.URL, loc.LineNumber, loc.ColumnNumber)
		}
		st.mu.Lock()
		st.console = appendRing(st.console, entry, st.capacity)
		st.mu.Unlock()
	})

	p.OnPageError(func(err error) {
		st.mu.Lock()
		st.errors = appendRing(st.errors, ErrorEntry{Time: nowStamp(), Error: err.Error()}, st.capacity)
		st.mu.Unlock()
	})

	p.OnResponse(func(r playwright.Response) {
		req := r.Request()
		entry := NetworkEntry{
			Time:   nowStamp(),
			Method: req.Method(),
			URL:    r.URL(),
			Status: r.Status(),
			Type:   req.ResourceType(),
		}
		st.mu.Lock()
		st.network = appendRing(st.network, entry, st.capacity)
		st.mu.Unlock()
	})

	p.OnRequestFailed(func(req playwright.Request) {
		entry := NetworkEntry{
			Time:   nowStamp(),
			Method: req.Method(),
			URL:    req.URL(),
			Type:   req.ResourceType(),
		}
		if f := req.Failure(); f != nil {
			entry.Failure = f.Error()
		} else {
			entry.Failure = "failed"
		}
		st.mu.Lock()
		st.network = appendRing(st.network, entry, st.capacity)
		st.mu.Unlock()
	})

	// A dialog with no handler blocks the page until it times out, which looks
	// to the model like a hung click. Dismissing it and recording that it
	// happened turns a hang into a fact the model can read.
	p.OnDialog(func(d playwright.Dialog) {
		st.mu.Lock()
		st.dialogs = appendRing(st.dialogs,
			fmt.Sprintf("%s  %s: %s (dismissed)", nowStamp(), d.Type(), d.Message()), st.capacity)
		st.mu.Unlock()
		d.Dismiss()
	})

	p.OnClose(func(playwright.Page) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.forgetLocked(st.ID)
	})
}

// forgetLocked drops a closed page and moves the selection off it.
func (b *Browser) forgetLocked(id string) {
	if _, ok := b.pages[id]; !ok {
		return
	}
	delete(b.pages, id)
	for i, existing := range b.order {
		if existing == id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
	if b.current == id {
		b.current = ""
		if len(b.order) > 0 {
			b.current = b.order[len(b.order)-1]
		}
	}
}

// CurrentPage returns the page tools act on by default, starting the browser
// and opening a page if necessary.
//
// In interactive mode this retargets onto whichever tab has focus first. A
// person who opens a new tab and asks "what is wrong with this page" means the
// one in front of them, and answering about a tab they selected ten minutes
// ago is worse than useless: it is confidently about the wrong thing.
func (b *Browser) CurrentPage() (*PageState, error) {
	if err := b.Start(); err != nil {
		return nil, err
	}
	if b.cfg.FollowsActiveTab() {
		if st := b.focusedPage(); st != nil {
			b.mu.Lock()
			b.current = st.ID
			b.mu.Unlock()
			return st, nil
		}
	}
	return b.selectedPage()
}

// focusedPage is the tab the person is looking at, or nil when that cannot be
// determined - every tab hidden, or a browser nobody has focused.
//
// The pages are copied out from under the lock before any of them is asked,
// because asking crosses to the browser and back: holding the lock across that
// would block the page-open and page-close handlers that need it.
func (b *Browser) focusedPage() *PageState {
	b.mu.Lock()
	candidates := make([]*PageState, 0, len(b.order))
	for _, id := range b.order {
		if st, ok := b.pages[id]; ok && !st.Page.IsClosed() {
			candidates = append(candidates, st)
		}
	}
	b.mu.Unlock()

	// Newest first: when several tabs claim visibility - which happens when
	// the window is not focused at all and every tab reports "visible" - the
	// most recently opened is the better guess at what is being looked at.
	for i := len(candidates) - 1; i >= 0; i-- {
		st := candidates[i]
		value, err := st.Page.Evaluate(`() => document.visibilityState === 'visible' && document.hasFocus()`)
		if err != nil {
			continue // navigating, closing, or a page that will not answer
		}
		if focused, _ := value.(bool); focused {
			return st
		}
	}
	return nil
}

// selectedPage is the page a previous call selected, validated and replaced if
// it has gone.
func (b *Browser) selectedPage() (*PageState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.startLocked(); err != nil {
		return nil, err
	}
	// Pages can close without the event having been delivered yet, so the
	// selection is validated rather than trusted.
	if st, ok := b.pages[b.current]; ok && !st.Page.IsClosed() {
		return st, nil
	}
	for i := len(b.order) - 1; i >= 0; i-- {
		if st, ok := b.pages[b.order[i]]; ok && !st.Page.IsClosed() {
			b.current = st.ID
			return st, nil
		}
	}
	p, err := b.ctx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("opening a page: %w", err)
	}
	b.trackLocked(p)
	b.current = b.order[len(b.order)-1]
	return b.pages[b.current], nil
}

// PageByID returns a named page, or the current one when id is empty.
func (b *Browser) PageByID(id string) (*PageState, error) {
	if id == "" {
		return b.CurrentPage()
	}
	b.mu.Lock()
	st, ok := b.pages[id]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no page %q is open; call browser_pages to list them", id)
	}
	if st.Page.IsClosed() {
		return nil, fmt.Errorf("page %s has been closed", id)
	}
	return st, nil
}

// Pages returns every open page, oldest first.
func (b *Browser) Pages() []*PageState {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*PageState, 0, len(b.order))
	for _, id := range b.order {
		if st, ok := b.pages[id]; ok {
			out = append(out, st)
		}
	}
	return out
}

// CurrentID is the id of the page tools act on by default.
func (b *Browser) CurrentID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current
}

// Select makes a page current.
func (b *Browser) Select(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.pages[id]
	if !ok {
		return fmt.Errorf("no page %q is open; call browser_pages to list them", id)
	}
	if st.Page.IsClosed() {
		return fmt.Errorf("page %s has been closed", id)
	}
	b.current = id
	return nil
}

// NewPage opens a page and makes it current.
func (b *Browser) NewPage() (*PageState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.startLocked(); err != nil {
		return nil, err
	}
	p, err := b.ctx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("opening a page: %w", err)
	}
	b.trackLocked(p)
	id := b.order[len(b.order)-1]
	b.current = id
	return b.pages[id], nil
}

// Close shuts the browser down. In attach mode the browser belongs to the
// operator, so only the connection is dropped - closing their window out from
// under them would be the worst possible way for this server to exit.
func (b *Browser) Close() {
	// The state is taken and cleared under the lock, and the closing happens
	// outside it. Closing a context fires a close event for every page, and
	// those handlers take this same lock - holding it across the close would
	// deadlock the shutdown.
	b.mu.Lock()
	ctx, browser, pw, mode := b.ctx, b.browser, b.pw, b.mode
	b.ctx, b.browser, b.pw = nil, nil, nil
	b.pages = make(map[string]*PageState)
	b.order, b.current, b.mode = nil, "", ""
	b.mu.Unlock()

	if ctx != nil && mode == "launched" {
		ctx.Close()
	}
	if browser != nil {
		browser.Close()
	}
	if pw != nil {
		pw.Stop()
	}
}

// Restart closes the browser and starts a fresh one.
func (b *Browser) Restart() error {
	b.Close()
	return b.Start()
}

// Console returns the messages the page has logged, newest last.
func (p *PageState) Console() []ConsoleEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ConsoleEntry(nil), p.console...)
}

// Errors returns the page's uncaught exceptions.
func (p *PageState) Errors() []ErrorEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ErrorEntry(nil), p.errors...)
}

// Network returns the requests the page has made.
func (p *PageState) Network() []NetworkEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]NetworkEntry(nil), p.network...)
}

// Dialogs returns the alert/confirm/prompt dialogs that were dismissed.
func (p *PageState) Dialogs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.dialogs...)
}

// eventMark is how much a page had reported at a moment in time. A tool takes
// one before acting so that what it reports afterwards is what its own action
// caused, rather than every failure the page has had since it loaded.
type eventMark struct {
	console, errors, network int
}

// Mark records the current depth of the buffers.
func (p *PageState) Mark() eventMark {
	p.mu.Lock()
	defer p.mu.Unlock()
	return eventMark{console: len(p.console), errors: len(p.errors), network: len(p.network)}
}

// Since returns the errors and network records added after the mark. Entries
// dropped from the ring in the meantime are simply not reported: a burst that
// overflowed the buffer during one action is a rare shape, and reporting the
// wrong entries would be worse than reporting fewer.
func (p *PageState) Since(mark eventMark) ([]ErrorEntry, []NetworkEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var errs []ErrorEntry
	if mark.errors < len(p.errors) {
		errs = append(errs, p.errors[mark.errors:]...)
	}
	var net []NetworkEntry
	if mark.network < len(p.network) {
		net = append(net, p.network[mark.network:]...)
	}
	return errs, net
}

// ClearEvents empties the buffers, which is how a model marks the point an
// interaction starts so that what follows is only about that interaction.
func (p *PageState) ClearEvents() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.console, p.errors, p.network, p.dialogs = nil, nil, nil, nil
}

// NextRefSeq advances the snapshot generation and returns it.
func (p *PageState) NextRefSeq() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refSeq++
	return p.refSeq
}

// RefSeq is the generation of the most recent snapshot.
func (p *PageState) RefSeq() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refSeq
}

// appendRing appends to a bounded buffer, dropping the oldest entry when full.
func appendRing[T any](buf []T, item T, capacity int) []T {
	if capacity <= 0 {
		capacity = 500
	}
	buf = append(buf, item)
	if len(buf) > capacity {
		buf = buf[len(buf)-capacity:]
	}
	return buf
}

func nowStamp() string { return time.Now().Format("15:04:05.000") }
