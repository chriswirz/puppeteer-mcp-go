package main

// DefaultInstructions is what the server tells a client about itself when the
// config names nothing better. It is the one place a model is told the shape of
// the workflow rather than of an individual tool, so it says which tool to
// reach for first and which habits waste a session.
const DefaultInstructions = `Drives a Chrome browser for testing web apps, debugging pages and building Chrome extensions.

Start with browser_status: it says whether a browser is attached or launched, headless or headed, which extensions are loaded, and which pages are open, without starting anything. Every tool acts on the current page unless you pass page_id; browser_pages lists them and browser_select_page changes which one is current.

Prefer browser_snapshot over browser_screenshot when you want to know what is on the page. The snapshot is an outline of the page's elements with a ref on each, and those refs are what browser_click and browser_type take - there is no CSS selector to guess at and no image to interpret. Take a screenshot when the question is genuinely visual: layout, styling, a rendering bug. Refs go stale when the page re-renders, so snapshot again after anything that changes it.

When something does not work, browser_console and browser_network already hold what the page reported since it loaded. Reading them is faster and more reliable than re-running the interaction and watching. browser_clear_events before a step you want to study keeps the record to that step alone.

When a person is watching the browser, you are taking turns with them rather than working alone. browser_status says whether that is the case, and browser_pages marks the tab they are looking at. Two things follow from it. Check where the browser actually is before answering a question about "this page" - they may have navigated since your last call. And when you need something only they can do - a login, a two-factor prompt, a decision that is not yours - call browser_wait_for_user with a plain sentence saying what you need, rather than guessing at a delay or driving through a screen you cannot pass. browser_notify says what you are doing without waiting.

For extension work, browser_extensions lists what Chrome has loaded with each id, and browser_extension_eval runs code inside the background service worker, where the chrome.* APIs actually exist - evaluating chrome.storage from an ordinary page will not work, because the page has no access to it. browser_extension_page opens a popup or options page as a normal tab you can snapshot and debug.`
