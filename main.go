// puppeteer-mcp-go is a Model Context Protocol server that drives a Chrome
// browser, for testing web apps, debugging pages and building extensions.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const appName = "puppeteer-mcp"

const usage = `puppeteer-mcp - a Model Context Protocol server that drives a Chrome browser

It gives a coding agent a real browser: navigate, click, type, read the
accessibility tree, screenshot, watch the console and the network, run
JavaScript, send raw DevTools commands, and work on an unpacked Chrome
extension.

Usage:
  puppeteer-mcp [flags]

Two ways to get a browser
  Launch (the default). This server starts Chrome against its own profile
  directory. Reproducible, can run headless, right for a test run.

  Attach. You start Chrome yourself with a debugging port, and this server
  connects to it:

      chrome.exe --remote-debugging-port=9222 --user-data-dir=D:\chrome-debug
      puppeteer-mcp --cdp http://127.0.0.1:9222

  Right for debugging: it is your window, you are already signed in, your
  extension is already loaded, and you watch what the agent does. The
  --user-data-dir is not optional - without it Chrome hands the command line to
  an already-running instance and never opens the port.

It speaks MCP revision ` + ProtocolVersion + `, which is stateless: there is no
initialize handshake and no session id. Older clients are served too - one that
opens with an initialize handshake gets the revision it negotiates. Pass
--no-legacy to serve only the current revision.

Flags:
  -c, --config <path>      configuration file to read (default: config.json in
                           the working directory; a missing default file is not
                           an error - the server runs on its defaults)
      --cdp <url>          attach to a Chrome already running with
                           --remote-debugging-port, e.g. http://127.0.0.1:9222,
                           instead of launching one
      --interactive        a person is watching this browser and will take
                           turns with the model in it. Forces a window, follows
                           whichever tab that person is looking at, and makes
                           browser_wait_for_user able to hand control back
      --follow-active-tab  act on whichever tab has focus rather than the one
                           a previous call selected. On by default with
                           --interactive; --no-follow-active-tab turns it off
      --no-follow-active-tab
                           keep acting on the selected tab even in interactive
                           mode
      --start-url <url>    open this URL when the browser starts, so an
                           interactive session begins on the page you care
                           about rather than a blank tab
      --headless           launch the browser with no window. Ignored when
                           attaching, and refused alongside --extension: Chrome
                           will not load an unpacked extension headless
      --headed             launch with a window (the default)
      --channel <name>     which browser to launch: chrome (default),
                           chrome-beta, msedge, or chromium for Playwright's
                           bundled build
      --profile <dir>      persistent user-data directory for launch mode
                           (default: ./profile), so logins survive a restart
      --extension <dir>    unpacked extension directory to load, the equivalent
                           of --load-extension. Repeat for several
      --no-eval            refuse browser_evaluate, browser_cdp and
                           browser_extension_eval. Worth setting when pointing
                           this at a browser holding a session you care about
      --allow-host <host>  restrict browser_navigate to these hosts. A host
                           matches its subdomains too. Comma-separated
      --tunnel <url>       expose this server through an https-tunnel server at
                           this URL, e.g. https://tunnel.example.com. The MCP
                           handler is served in process by the tunnel client, so
                           the browser becomes drivable from a client that
                           cannot see this network. Set a --token as well: the
                           tunnel is public and these tools drive a real browser
                           holding your real sessions
      --tunnel-key <key>   API key for that tunnel server (default: the
                           TUNNEL_API_KEY environment variable)
      --tunnel-subdomain <label>
                           subdomain to ask for. It is granted when free and a
                           random one is issued otherwise, so read the URL the
                           server prints when the tunnel comes up
      --tunnel-session-file <path>
                           keep the session id in this file instead of in
                           config.json. Without it the id is written into the
                           tunnel section of the config; naming a file makes
                           that file the only store, and the config is left
                           alone
      --tunnel-only        serve the tunnel alone, binding no local port. By
                           default the same handler is served both ways
      --transport <name>   "stdio" (default) to speak JSON-RPC on stdin/stdout
                           for a client that launches this server, or "http"
  -u, --url <url>          full URL clients connect to over http, and what the
                           server binds to (default: http://127.0.0.1:8770/mcp)
      --token <token>      require this bearer token on every HTTP request
      --allow-origin <o>   comma-separated origins a browser may call this
                           server from. "*" allows any
      --allow-header <h>   comma-separated request headers a browser may send
      --tls-cert <path>    TLS certificate to serve with, and
      --tls-key <path>     its private key
      --tls-self-signed    generate a self-signed certificate on startup
      --no-legacy          serve only protocol version ` + ProtocolVersion + `
      --example-config     write a complete example config.json to stdout
      --check              load the config, report what would be served, and
                           exit without listening or starting a browser
  -v, --version            print the version and exit
  -h, --help               show this help and exit

Examples:
  puppeteer-mcp
  puppeteer-mcp --headless
  puppeteer-mcp --interactive
  puppeteer-mcp --cdp http://127.0.0.1:9222
  puppeteer-mcp --extension ./my-extension
  puppeteer-mcp --transport http --url http://127.0.0.1:8770/mcp
  puppeteer-mcp --cdp http://127.0.0.1:9222 --no-eval --allow-host example.com
  puppeteer-mcp --tunnel https://tunnel.example.com --token "$MCP_TOKEN" --no-eval
  puppeteer-mcp --example-config > config.json
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", appName, err)
		os.Exit(1)
	}
}

type options struct {
	configPath     string
	cdp            string
	interactive    bool
	followActive   bool
	noFollowActive bool
	headless       bool
	headed         bool
	channel        string
	profile        string
	startURL       string
	extensions     stringList
	noEval         bool
	allowHost      string
	tunnel         string
	tunnelKey      string
	tunnelSub      string
	tunnelSession  string
	tunnelOnly     bool
	transport      string
	url            string
	token          string
	allowOrigin    string
	allowHeader    string
	tlsCert        string
	tlsKey         string
	tlsSelfSigned  bool
	noLegacy       bool
	exampleConfig  bool
	check          bool
	showVersion    bool
	showHelp       bool
}

// stringList collects a flag given more than once, which is how several
// extensions are named.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func parseFlags(args []string) (*options, error) {
	var opts options
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	str := func(target *string, def string, names ...string) {
		for _, name := range names {
			fs.StringVar(target, name, def, "")
		}
	}
	boolean := func(target *bool, names ...string) {
		for _, name := range names {
			fs.BoolVar(target, name, false, "")
		}
	}
	str(&opts.configPath, "", "config", "c")
	str(&opts.cdp, "", "cdp")
	boolean(&opts.interactive, "interactive")
	boolean(&opts.followActive, "follow-active-tab")
	boolean(&opts.noFollowActive, "no-follow-active-tab")
	boolean(&opts.headless, "headless")
	boolean(&opts.headed, "headed")
	str(&opts.channel, "", "channel")
	str(&opts.profile, "", "profile")
	str(&opts.startURL, "", "start-url")
	fs.Var(&opts.extensions, "extension", "")
	boolean(&opts.noEval, "no-eval")
	str(&opts.allowHost, "", "allow-host")
	str(&opts.tunnel, "", "tunnel")
	str(&opts.tunnelKey, "", "tunnel-key")
	str(&opts.tunnelSub, "", "tunnel-subdomain")
	str(&opts.tunnelSession, "", "tunnel-session-file")
	boolean(&opts.tunnelOnly, "tunnel-only")
	str(&opts.transport, "", "transport")
	str(&opts.url, "", "url", "u")
	str(&opts.token, "", "token")
	str(&opts.allowOrigin, "", "allow-origin")
	str(&opts.allowHeader, "", "allow-header")
	str(&opts.tlsCert, "", "tls-cert")
	str(&opts.tlsKey, "", "tls-key")
	boolean(&opts.tlsSelfSigned, "tls-self-signed")
	boolean(&opts.noLegacy, "no-legacy")
	boolean(&opts.exampleConfig, "example-config")
	boolean(&opts.check, "check")
	boolean(&opts.showVersion, "version", "v")
	boolean(&opts.showHelp, "help", "h")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			opts.showHelp = true
			return &opts, nil
		}
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q; %s takes flags only", fs.Arg(0), appName)
	}
	if opts.headless && opts.headed {
		return nil, fmt.Errorf("--headless and --headed contradict each other")
	}
	if opts.followActive && opts.noFollowActive {
		return nil, fmt.Errorf("--follow-active-tab and --no-follow-active-tab contradict each other")
	}
	if opts.headless && opts.interactive {
		return nil, fmt.Errorf("--headless and --interactive contradict each other: " +
			"interactive mode is for a browser a person is watching")
	}
	if opts.headless && len(opts.extensions) > 0 {
		return nil, fmt.Errorf("--headless cannot be combined with --extension: " +
			"Chrome will not load an unpacked extension with no window")
	}
	return &opts, nil
}

func run(args []string) error {
	opts, err := parseFlags(args)
	if err != nil {
		return err
	}
	switch {
	case opts.showHelp:
		fmt.Print(usage)
		return nil
	case opts.showVersion:
		fmt.Printf("%s %s (MCP %s)\n", appName, version, ProtocolVersion)
		return nil
	case opts.exampleConfig:
		fmt.Print(ExampleConfig)
		return nil
	}

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("could not determine the working directory: %w", err)
	}
	configPath := opts.configPath
	explicit := configPath != ""
	if !explicit {
		configPath = joinPath(dir, "config.json")
	}
	cfg, err := LoadConfig(configPath, explicit)
	if err != nil {
		return err
	}
	applyOptions(&cfg, opts)
	if err := cfg.Normalize(dir); err != nil {
		return err
	}

	// On stdio, stdout carries the protocol: everything human-readable has to
	// go to stderr or it corrupts the stream.
	banner := os.Stdout
	if cfg.Server.Transport == "stdio" {
		banner = os.Stderr
	}
	logger := log.New(os.Stderr, appName+": ", log.LstdFlags)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := NewServer(cfg.Server.Name, cfg.Server.Version, cfg.Server.Instructions, cfg.Server.LegacyCompatibility)
	srv.cfg = cfg
	srv.browser = NewBrowser(cfg.Browser, logger)
	defer srv.browser.Close()

	srv.registerPageTools()
	srv.registerNavigationTools()
	srv.registerInteractionTools()
	srv.registerInspectionTools()
	srv.registerDebugTools()
	srv.registerEvalTools()
	srv.registerExtensionTools()
	srv.registerInteractiveTools()

	// Built before the banner so that --check reports a bad certificate rather
	// than a server that only fails once someone tries to connect.
	var tlsConfig *tls.Config
	if cfg.Server.Transport == "http" {
		tlsConfig, err = cfg.TLSConfig()
		if err != nil {
			return err
		}
	}

	printBanner(banner, &cfg, srv)
	if opts.check {
		fmt.Fprintln(banner, "\nConfiguration is valid. Exiting because --check was given.")
		return nil
	}

	// In interactive mode the window is half the point: a person cannot
	// navigate somewhere and ask about it if nothing has opened yet. Lazy
	// starting is right when the browser is the model's own, and wrong here.
	//
	// A browser that will not start is reported and then left alone. The
	// server is still useful - browser_start retries once the problem is
	// fixed - and refusing to serve would take the MCP client down with it.
	if cfg.Browser.Interactive {
		if err := srv.browser.Start(); err != nil {
			logger.Printf("the browser did not start: %v", err)
			logger.Printf("serving anyway; call browser_start to retry")
		} else {
			openStartURL(srv, cfg.Browser.StartURL, logger)
		}
	}

	if cfg.Server.Transport == "stdio" {
		return NewStdioTransport(srv, os.Stdin, os.Stdout, cfg.Server.LegacyCompatibility, logger).Serve(ctx)
	}
	listeners, err := cfg.Listeners()
	if err != nil {
		return err
	}
	transport := NewHTTPTransport(srv, cfg.Server, listeners[0].Paths[0], logger)
	if !cfg.Tunnel.Enabled {
		return transport.ServeAll(ctx, listeners, tlsConfig)
	}

	// The tunnel serves the same handler in process, on every path the local
	// listeners answer on, so a client reaches the same endpoint either way.
	tunnel, err := NewTunnel(&cfg, transport.HandlerFor(endpointPaths(listeners)...), logger)
	if err != nil {
		return err
	}
	if cfg.Tunnel.Only {
		return tunnel.Run(ctx)
	}

	// Either half failing takes the other down: a server that is half up is
	// worse than one that refuses to start, because a client connected to the
	// surviving half has no way to know.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- transport.ServeAll(ctx, listeners, tlsConfig) }()
	go func() { errs <- tunnel.Run(ctx) }()
	var first error
	for range 2 {
		if err := <-errs; err != nil && first == nil {
			first = err
			cancel()
		}
	}
	return first
}

// openStartURL navigates the browser's first page to the configured start URL.
// A failure here is reported and nothing more: the browser is up, which is what
// interactive mode promised, and a page that would not load is the person's to
// look at rather than a reason to refuse to serve.
func openStartURL(srv *Server, url string, logger *log.Logger) {
	if url == "" {
		return
	}
	st, err := srv.browser.CurrentPage()
	if err != nil {
		logger.Printf("could not open %s: %v", url, err)
		return
	}
	if _, err := st.Page.Goto(url); err != nil {
		logger.Printf("could not open %s: %v", url, err)
		return
	}
	logger.Printf("browser open at %s", url)
}

// endpointPaths is every distinct path the configured listeners answer on.
func endpointPaths(listeners []Listener) []string {
	var paths []string
	seen := map[string]bool{}
	for _, l := range listeners {
		for _, path := range l.Paths {
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	return paths
}

// applyOptions lets the command line override the configuration file.
func applyOptions(cfg *Config, opts *options) {
	if opts.cdp != "" {
		cfg.Browser.CDPURL = opts.cdp
	}
	if opts.interactive {
		cfg.Browser.Interactive = true
	}
	// A flag beats the config file, and an explicit off beats the default that
	// interactive mode would otherwise apply.
	if opts.followActive {
		cfg.Browser.FollowActiveTab = boolPtr(true)
	}
	if opts.noFollowActive {
		cfg.Browser.FollowActiveTab = boolPtr(false)
	}
	if opts.headless {
		cfg.Browser.Headless = true
	}
	if opts.headed {
		cfg.Browser.Headless = false
	}
	if opts.channel != "" {
		cfg.Browser.Channel = opts.channel
	}
	if opts.profile != "" {
		cfg.Browser.ProfileDir = opts.profile
	}
	if opts.startURL != "" {
		cfg.Browser.StartURL = opts.startURL
	}
	if len(opts.extensions) > 0 {
		cfg.Browser.Extensions = append(cfg.Browser.Extensions, opts.extensions...)
	}
	if opts.noEval {
		cfg.Browser.AllowEval = false
	}
	if opts.allowHost != "" {
		cfg.Browser.AllowNavigateHosts = splitList(opts.allowHost)
	}
	if opts.transport != "" {
		cfg.Server.Transport = opts.transport
	}
	if opts.tunnel != "" {
		cfg.Tunnel.Enabled = true
		cfg.Tunnel.ServerURL = opts.tunnel
	}
	if opts.tunnelKey != "" {
		cfg.Tunnel.APIKey = opts.tunnelKey
	}
	if opts.tunnelSub != "" {
		cfg.Tunnel.Subdomain = opts.tunnelSub
	}
	if opts.tunnelSession != "" {
		cfg.Tunnel.SessionFile = opts.tunnelSession
	}
	if opts.tunnelOnly {
		cfg.Tunnel.Enabled = true
		cfg.Tunnel.Only = true
	}
	// The tunnel serves the HTTP handler, so asking for one is asking for the
	// HTTP transport. Making the operator say both would only ever produce a
	// confusing validation error.
	if cfg.Tunnel.Enabled && opts.transport == "" && cfg.Server.Transport == "stdio" {
		cfg.Server.Transport = "http"
	}
	if opts.url != "" {
		cfg.Server.URL = opts.url
		cfg.Server.AdditionalURLs = nil
		// Naming a URL is how the HTTP transport is asked for; requiring
		// --transport http as well would be a papercut and nothing more.
		if opts.transport == "" {
			cfg.Server.Transport = "http"
		}
	}
	if opts.token != "" {
		cfg.Server.AuthToken = opts.token
	}
	if opts.allowOrigin != "" {
		cfg.Server.AllowedOrigins = splitList(opts.allowOrigin)
	}
	if opts.allowHeader != "" {
		cfg.Server.AllowedHeaders = splitList(opts.allowHeader)
	}
	if opts.tlsCert != "" {
		cfg.Server.TLSCertFile = opts.tlsCert
	}
	if opts.tlsKey != "" {
		cfg.Server.TLSKeyFile = opts.tlsKey
	}
	if opts.tlsSelfSigned {
		cfg.Server.TLSSelfSigned = true
	}
	// Asking for TLS without saying so in the URL is a contradiction the
	// operator almost certainly did not mean; upgrade the scheme instead of
	// silently serving plaintext.
	if (cfg.Server.TLSSelfSigned || cfg.Server.TLSCertFile != "") && !cfg.Server.IsTLS() {
		cfg.Server.URL = strings.Replace(cfg.Server.URL, "http://", "https://", 1)
	}
	if opts.noLegacy {
		cfg.Server.LegacyCompatibility = false
	}
}

// printBanner is the startup summary: what browser is being driven, how it is
// reached, and - the thing an operator actually needs - the URL to connect to.
func printBanner(w *os.File, cfg *Config, srv *Server) {
	fmt.Fprintf(w, "%s %s  (MCP protocol %s)\n", appName, version, ProtocolVersion)
	if cfg.Server.LegacyCompatibility {
		fmt.Fprintf(w, "  versions   %s\n", strings.Join(SupportedVersions, ", "))
	} else {
		fmt.Fprintf(w, "  versions   %s only (--no-legacy)\n", ProtocolVersion)
	}

	b := cfg.Browser
	if b.CDPURL != "" {
		fmt.Fprintf(w, "  browser    attaching to Chrome at %s\n", b.CDPURL)
		fmt.Fprintf(w, "             start it with --remote-debugging-port and its own --user-data-dir,\n")
		fmt.Fprintf(w, "             or the connection will be refused\n")
	} else {
		mode := "headed"
		if b.Headless {
			mode = "headless"
		}
		if b.Interactive {
			mode += ", interactive"
		}
		fmt.Fprintf(w, "  browser    launching %s (%s)\n", b.Channel, mode)
		fmt.Fprintf(w, "  profile    %s\n", b.ProfileDir)
	}
	if len(b.Extensions) > 0 {
		fmt.Fprintf(w, "  extensions %s\n", wrapList(b.Extensions, 60, "             "))
	}
	if !b.AllowEval {
		fmt.Fprintf(w, "  eval       disabled; browser_evaluate, browser_cdp and browser_extension_eval are not served\n")
	}
	if len(b.AllowNavigateHosts) > 0 {
		fmt.Fprintf(w, "  hosts      navigation restricted to %s\n", strings.Join(b.AllowNavigateHosts, ", "))
	}
	if b.FollowsActiveTab() {
		fmt.Fprintf(w, "  focus      tools act on whichever tab has focus\n")
	}
	fmt.Fprintf(w, "  timeouts   %dms default, %dms navigation\n", b.DefaultTimeoutMs, b.NavigationTimeoutMs)

	fmt.Fprintf(w, "  transport  %s\n", cfg.Server.Transport)
	if cfg.Server.Transport == "stdio" {
		fmt.Fprintf(w, "  endpoint   stdin/stdout (JSON-RPC, newline delimited)\n")
	} else if endpoints, err := cfg.Endpoints(); err == nil {
		var addrs []string
		for _, e := range endpoints {
			addrs = append(addrs, e.Addr)
		}
		fmt.Fprintf(w, "  listening  %s\n", strings.Join(addrs, ", "))
		fmt.Fprintf(w, "\n  Connect your MCP client to:  %s\n\n", endpoints[0].URL)
		if cfg.Server.AuthToken != "" {
			fmt.Fprintf(w, "  Authentication: send Authorization: Bearer <token>\n")
		}
		origins := strings.Join(cfg.Server.AllowedOrigins, ", ")
		if origins == "" {
			origins = "none (browser clients will be refused)"
		}
		fmt.Fprintf(w, "  origins    %s\n", origins)
	}

	names := srv.ToolNames()
	sort.Strings(names)
	fmt.Fprintf(w, "  tools (%d)  %s\n", len(names), wrapList(names, 60, "              "))
	if cfg.Browser.Interactive {
		if b.CDPURL != "" {
			fmt.Fprintf(w, "\n  Connecting to your Chrome now; it is yours and the model's to share.\n")
		} else {
			fmt.Fprintf(w, "\n  The browser window opens now, for you and the model to share.\n")
		}
		if b.StartURL != "" {
			fmt.Fprintf(w, "  It opens at %s\n", b.StartURL)
		}
	} else {
		fmt.Fprintf(w, "\n  The browser starts on the first tool call that needs it, not now.\n")
	}
}

// wrapList renders a long list of names over several indented lines.
func wrapList(items []string, width int, indent string) string {
	var lines []string
	var current string
	for _, item := range items {
		switch {
		case current == "":
			current = item
		case len(current)+len(item)+2 <= width:
			current += ", " + item
		default:
			lines = append(lines, current)
			current = item
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return strings.Join(lines, "\n"+indent)
}

// splitList parses a comma-separated flag value, dropping empty entries.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func joinPath(dir, name string) string {
	if strings.HasSuffix(dir, string(os.PathSeparator)) {
		return dir + name
	}
	return dir + string(os.PathSeparator) + name
}
