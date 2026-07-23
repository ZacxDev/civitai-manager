# Scan "Stop scanning" — browser verification (v0.1.17)

Evidence for the client-side stop-button fix (`fix(web): scan Stop reliably swaps
to terminal and halts polling`) and the discovered-count feature.

Driven with Playwright (`playwright-core` + system Chromium via `executablePath`,
since the Playwright MCP browser download fails against the read-only Nix store on
this host) against the env-gated `TestBrowserHarness` streaming-scan seam.

## Real click-path exercised
1. `/library` → click **Scan for models** → HX-redirect to the Model files tab,
   scanning view bootstraps.
2. Observe the streaming progress line (discovered count).
3. Click **Stop scanning**.
4. Confirm the view swaps to the terminal "Scan stopped" result, the poller
   (`#scan-poll`) is gone, `GET /library/scan/status` polling halts, and the view
   does not flip back to scanning.

## Result: PASS
- `01-scanning-before-stop.png` — scanning view: "Scanning selected directories
  for model files… **1 / 4 discovered** · matched 1 · unmatched 0 (Stop any
  time)", exactly one `#scan-poll`.
- `02-scan-stopped-after.png` — terminal: "Scan result — **Scan stopped — 2 / 4
  discovered** · matched 2 · unmatched 0", no scanning card, `#scan-poll` count 0.
- Network: status polls at stop = 2, after a further 4s wait = 2 (delta 0 →
  polling halted). Stop POSTs = 1. No flip-back to the scanning view.
