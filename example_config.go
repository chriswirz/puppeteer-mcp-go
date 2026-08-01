package main

// ExampleConfig is written by --example-config. It is a working, fully
// populated configuration rather than a minimal one, so it doubles as the
// reference for every available setting.
const ExampleConfig = `{
  "server": {
    "name": "puppeteer-mcp",
    "version": "",
    "instructions": "",
    "transport": "stdio",
    "url": "http://127.0.0.1:8770/mcp",
    "allowed_origins": ["http://127.0.0.1", "http://localhost"],
    "allowed_headers": [],
    "allow_private_network": true,
    "auth_token": "",
    "screenshot_path": "",
    "screenshot_listing": false,
    "tls_cert_file": "",
    "tls_key_file": "",
    "tls_self_signed": false,
    "legacy_compatibility": true,
    "session_timeout_seconds": 7200
  },
  "tunnel": {
    "enabled": false,
    "server_url": "https://tunnel.example.com",
    "api_key": "",
    "api_key_env": "TUNNEL_API_KEY",
    "subdomain": "",
    "session_id": "",
    "session_file": ".puppeteer-mcp-tunnel-session",
    "only": false,
    "client_info": ""
  },
  "browser": {
    "cdp_url": "",
    "interactive": false,
    "start_url": "",
    "follow_active_tab": null,
    "headless": false,
    "channel": "chrome",
    "executable_path": "",
    "profile_dir": "profile",
    "extensions": [],
    "args": [],
    "ignore_default_args": ["--enable-automation"],
    "user_agent": "",
    "viewport_width": 0,
    "viewport_height": 0,
    "default_timeout_ms": 15000,
    "navigation_timeout_ms": 45000,
    "event_buffer_size": 500,
    "slow_mo_ms": 0,
    "screenshot_dir": "screenshots",
    "max_screenshot_bytes": 1048576,
    "allow_downloads": false,
    "download_dir": "downloads",
    "allow_eval": true,
    "allow_navigate_hosts": []
  }
}
`
