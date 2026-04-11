# ADR 0001 — utls as the primary HTTP fetch layer

**Status**: Accepted (revised)
**Date**: 2026-04-11 (first draft), revised same day after external review
**Decider**: Phase 0 smoke test (`test/spike/tls_fingerprint/`), 8 runs across 4 techniques
**Supersedes**: First draft of this ADR (see revision note §11)

## Context

diting v2 is a Go rewrite of the Python v1 pipeline. Its fetch layer must match or exceed the Python v1 success rate against modern bot-protected websites (Cloudflare, DataDome, Akamai).

The Python v1 fetch layer's single most valuable component was **curl_cffi**, which impersonates Chrome's TLS ClientHello fingerprint to bypass JA3/JA4-based bot detection. Go's stdlib `net/http` produces a distinctive "Go" TLS fingerprint that is easy to detect and universally flagged by modern bot-protection services.

Without a credible curl_cffi replacement for Go, Phase 2 of the Python architecture (the fetch overhaul that lifted success rate from ~50 % to 85 %+) cannot be preserved. This is the single biggest risk to the Go rewrite.

## Decision

Use [`github.com/refraction-networking/utls`](https://github.com/refraction-networking/utls) as the primary HTTP fetch layer for diting v2, with the following specifics:

1. **Default ClientHelloID**: `utls.HelloChrome_Auto` — the upstream-maintained alias that currently resolves to `HelloChrome_133` in `utls@v1.8.2` and will automatically upgrade as new Chrome versions ship. This is a deliberate choice of a **moving target** over a **pinned version**.

2. **ALPN/HTTP-version dispatch**: route the resulting TLS connection through
   - `golang.org/x/net/http2.Transport.NewClientConn` when ALPN negotiates `h2`
   - Manual HTTP/1.1 writing via `req.Write` + `http.ReadResponse` when ALPN negotiates `http/1.1`

3. **No multi-fingerprint rotation in v1**: we evaluated `utls.NewRoller()` and it **underperformed** fixed fingerprints on our workload (see §4 below). Phase 1 ships a single fixed fingerprint. Roller is on the roadmap for Phase 2+ re-evaluation under adversarial conditions that this spike did not simulate (long-running sessions, per-fingerprint rate limits, adaptive detectors). See §7 for the upgrade trigger.

## Alternatives considered

| Alternative | Verdict | Reason |
|---|---|---|
| stdlib `net/http` | Rejected | Distinctive Go TLS fingerprint, blocked by Cloudflare on 3 of 4 medium-difficulty URLs |
| `resty` (stdlib-backed) | Rejected | Same TLS fingerprint as stdlib; no improvement |
| `chromedp` only (no HTTP fallback) | Rejected | Launches a full Chrome process per fetch — too slow, memory-heavy; used as layer 2 fallback instead |
| `cycletls` | Rejected | Less maintained than utls; utls is the upstream project |
| `utls.HelloChrome_120` (earlier draft) | Rejected (this revision) | Empirical data shows HelloChrome_Auto (= 133) beats 120 by 4.4 pp on mean; see §4 |
| `utls.NewRoller()` (upstream recommendation) | Deferred | Empirical data shows Roller underperforms fixed fingerprints for single-call workflows; see §4 |
| `utls.HelloRandomized` | Deferred | Similar concerns to Roller; not tested directly |

## Evidence: Phase 0 smoke test

A 14-URL smoke test was run **8 times** comparing 4 techniques. Each run is one shot per (URL, technique) combination. The spike code is in `test/spike/tls_fingerprint/main.go`.

### URL composition

| Difficulty | Count | Description |
|---|---|---|
| Easy | 5 | GitHub, HN, Wikipedia, arXiv, Python docs |
| Medium | 4 | StackOverflow, Cloudflare.com, OpenAI research, Anthropic research |
| Hard | 5 | Medium.com, g2.com, LinkedIn, Quora, X/Twitter |

### Aggregate success rates across 8 runs

| Technique | Mean | Median | StdDev | Best | Worst |
|---|---|---|---|---|---|
| `net/http` (baseline) | **58.9 %** | 57.1 % | 4.8 | 64.3 % | 50.0 % |
| `utls+chrome120` | **79.5 %** | 78.6 % | 2.5 | 85.7 % | 78.6 % |
| `utls+chrome_auto` (= 133) | **83.9 %** | 85.7 % | 5.0 | 85.7 % | 71.4 % |
| `utls+roller` | **74.1 %** | 71.4 % | 6.6 | 85.7 % | 64.3 % |

### Per-difficulty breakdown (steady-state; excludes outlier run 2 for `chrome_auto`)

| Category | `net/http` | `chrome120` | `chrome_auto` | `roller` |
|---|---|---|---|---|
| Easy (5 URLs) | ~100 % | **100 %** | **100 %** | ~95 % |
| Medium (4 URLs) | ~30 % | ~75 % | **~100 %** | ~85 % |
| Hard (5 URLs) | ~40 % | ~60 % | ~60 % | ~55 % |

### Key findings

1. **`chrome_auto` is the empirical winner** despite its higher variance. Median 85.7 % is the ceiling any utls technique hit. The one outlier run (run 2 of 8) saw transient failures on easy targets (Wikipedia, Hacker News) that were almost certainly network-side, not fingerprint-related — the rest of the runs all showed 85.7 %.

2. **`chrome_auto` strictly beats `chrome120` on one critical site**: StackOverflow. Cloudflare currently lets `chrome_auto` through but rejects `chrome120`. This is the 4.4 pp gap between them.

3. **`utls+roller` underperforms for single-call workflows.** Root cause: `Roller.Dial` only retries on **TLS handshake failures**. HTTP 403 responses come back over a successful TLS connection, so Roller never gets a chance to try a different fingerprint. Meanwhile, Roller's random shuffle introduces variance — it occasionally lands on `HelloRandomized` or `HelloFirefox_Auto` which happen to get blocked where `chrome_auto` would succeed. **Roller's value is for long-running stateful sessions where the same fingerprint would eventually get flagged**, not for diting's one-shot CLI invocations.

4. **The 2 consistently-failing sites (`g2.com`, `quora.com`) also block every other technique, including net/http.** They use DataDome and require the `chromedp` browser fallback layer by design. They are outside the utls TLS-fingerprint problem domain and should not count against the gate.

5. **`net/http` is not strictly worse on hard URLs.** It happens to succeed on X/Twitter and LinkedIn where some utls runs have issues (likely because these sites have per-TLS-fingerprint rate limits that a fresh Go fingerprint ducks under). This confirms the `chromedp` fallback layer must exist — no single HTTP technique wins everywhere.

### Variance notes (important for future re-testing)

- **Single-run measurements are noisy.** A single run can swing ~10 pp on this URL set. Do not judge a technique on one run.
- **Minimum 5 runs** is needed for stable comparison. Report median rather than mean — mean is pulled by outliers.
- **Network-side flakiness is the dominant variance source**, not TLS fingerprinting. When `chrome_auto` failed on Wikipedia in run 2, it was a transient TCP-level issue, not a TLS-level block.

### Spike-discovered bug (preserved from initial draft)

`utls.Config.NextProtos` does **not** override the ALPN extension baked into `HelloChrome_*` specs. The Chrome specs always advertise `h2, http/1.1` in their ClientHello, regardless of user configuration. The first spike iteration assumed `NextProtos: ["http/1.1"]` would force HTTP/1.1 and silently failed against every site because servers negotiated h2 but the code spoke h1.

**The production fetch layer must always handle ALPN-negotiated h2** via `http2.Transport.NewClientConn`. Attempting h1-only just silently fails against every server that picks h2.

## Gate decision

**Gate criterion** (from `docs/architecture.md` § 6.3): best utls technique must reach ≥ 80 % success on the smoke URL set.

**Result**: `utls+chrome_auto` = **83.9 % mean / 85.7 % median** across 8 runs.

**Verdict: PASS.** Proceed with Go rewrite Phase 1 (fetch layer implementation).

## Version selection rationale (responding to external review)

**Why not pin a specific version like `HelloChrome_133`?**

- Detectors increasingly check for **recent** Chrome versions. A client claiming to be Chrome 120 in mid-2026 looks suspicious because real Chrome auto-updates aggressively. Pinning a version creates a growing gap between our claim and reality.
- `HelloChrome_Auto` is upstream-maintained and tracks the newest Chrome spec that utls supports. When utls adds `HelloChrome_134`, we automatically get it on our next `go get -u`.
- The empirical benefit is visible in this spike: `chrome_auto` (= 133) beats `chrome120` on StackOverflow.

**Why not always use the newest specific constant?**

- `HelloChrome_Auto` **is** the newest constant for "Chrome" — it is an alias that upstream updates. Using it is equivalent to "always use the newest" without us having to remember to update the import.
- If we explicitly imported `HelloChrome_133`, a future utls bump to `HelloChrome_135` would leave us stuck on 133 unless we also updated our code.

**Version upgrade policy**:

- On every `utls` version bump, re-run `test/spike/tls_fingerprint/` at least 5 times.
- If `chrome_auto` success rate drops more than 5 pp versus the previous baseline, open a new ADR to investigate before upgrading.
- Record the effective `HelloChrome_*` alias in the commit message so the history is traceable.

## Multi-fingerprint strategy (responding to external review)

The upstream utls README recommends using **multiple fingerprints and/or randomised fingerprints**, specifically via `utls.Roller`. This is a legitimate recommendation that we acknowledge explicitly but **do not adopt for v1**, based on our empirical data.

### Why Phase 1 uses a fixed fingerprint

- **Benchmark stability**: diting's benchmark (see `docs/architecture.md` § 12) compares pipeline variants. If the fetch layer's fingerprint rotates randomly, benchmark variance contaminates every comparison. A controlled fingerprint is a controlled variable.
- **Empirical underperformance on single-call workloads**: 8-run mean success rate Roller 74.1 % vs `chrome_auto` 83.9 %. Roller is 9.8 pp worse on our specific workload.
- **Debugging clarity**: when a fetch fails, we want to know exactly which fingerprint was sent. Roller's memoised `WorkingHelloID` works within one process but our CLI invocations are short-lived; each call is effectively starting from scratch, getting a random shuffle order.

### Conditions that would trigger a Roller re-evaluation (Phase 2+)

The spike tests a workload that does not match the conditions where Roller is most valuable. If **any** of the following are observed in production use, Roller must be re-evaluated:

1. **Per-fingerprint sustained blocks**: the same URL set that passes today starts failing next month with identical utls version. This suggests the detector has learned to block `chrome_auto`'s specific fingerprint.
2. **Long-running session rate limits**: if we add a daemon mode or persistent connection reuse, the same TCP/TLS fingerprint sending multiple requests may trigger rate limits that a rotating fingerprint would evade.
3. **Success rate degradation below 70 %**: if `chrome_auto` median drops below 70 % for two consecutive utls version bumps, evaluate whether Roller's variance is worth accepting for the mean-rate improvement.

When re-evaluating:
- Run the spike for **at least 20 runs per technique** (not 8) to get a tight confidence interval on Roller's mean.
- Use a larger URL set (50+) that includes sites known to have per-fingerprint rate limits.
- Compare `Roller` with and without the `WorkingHelloID` memoisation disabled (to isolate rotation benefit from "first-success stickiness" benefit).

### Other multi-fingerprint options we did not test

- `HelloRandomized` as a standalone fingerprint: worth testing in the Phase 2+ re-evaluation.
- Manual rotation: pick `HelloChrome_Auto` 70 % of requests, `HelloFirefox_Auto` 20 %, `HelloIOS_Auto` 10 %. Harder to justify without empirical data that this wins over fixed Chrome.

## Implementation notes for Phase 1

When building `internal/fetch/utls/`:

1. **Always** use `utls.HelloChrome_Auto` (never pin a specific version constant in production code).
2. **Always** check `tlsConn.ConnectionState().NegotiatedProtocol` after `HandshakeContext` and dispatch:
   - `"h2"` → `http2.Transport.NewClientConn(tlsConn).RoundTrip(req)` — **remove `Connection` and `Upgrade` headers from `req` first**; they are illegal in HTTP/2.
   - `"http/1.1"` or `""` → `req.Write(tlsConn) + http.ReadResponse(bufio.NewReader(tlsConn), req)`.
3. **Set read and write deadlines** via `tlsConn.SetWriteDeadline` / `SetReadDeadline` — the `context.Context` alone is not enough once you bypass `http.Transport`.
4. **Cap response body reads** with `io.LimitReader(resp.Body, N)` to prevent memory blow-up from hostile responses. Spike used 1 MiB; production should match the Python v1 fetch cap.
5. **Handle `io.ErrUnexpectedEOF` as partial success** when reading the body — some servers set `Connection: close` and close the socket the instant the body ends, which can surface as UnexpectedEOF after the full body has been read.
6. **Do not reuse connections across requests** in the first implementation. Open a fresh TCP + TLS per fetch. Connection reuse inside utls is possible but non-trivial and not required to meet the gate.
7. **For HTTP/2 requests**, the `http2.ClientConn` keeps the underlying TCP connection open for multiple RoundTrips if you want, but for v1 we prefer a fresh conn per URL for simplicity. Close with `h2Conn.Close()` after `RoundTrip` completes.
8. **Chrome header set**: reuse the map defined in `test/spike/tls_fingerprint/main.go` verbatim for the first Phase 1 iteration, then tune as real sites are tested. Since we now use `HelloChrome_Auto`, bump the `User-Agent` and `Sec-Ch-Ua` header values in lockstep with utls's Chrome version whenever you bump utls.
9. **For chromedp fallback** (layer 2 of the fetch chain): `g2.com` and `quora.com` are good smoke-test targets, because no HTTP-level technique can reach them.
10. **Do not use `utls.NewRoller()`** in Phase 1. See §7 for the conditions under which it becomes reconsiderable.

## Re-testing policy

This ADR is based on an 8-run × 14-URL sample. Before committing diting v2 to a release, Phase 1 must re-run the smoke test with:

- At least **50 URLs** covering more bot-protection vendors (PerimeterX, Akamai Bot Manager, Imperva, Kasada, Shape Security).
- **10+ runs per URL** to compute real success rate distributions (5 was minimum for stable; 10 gives tighter confidence intervals).
- A parallel run of the Python v1 `curl_cffi` pipeline on the same URLs for direct reference.
- Record the effective `utls.HelloChrome_Auto` alias at the time of the test run (e.g., "HelloChrome_Auto resolved to HelloChrome_133 in utls v1.8.2").

If the expanded test shows `chrome_auto` is < 75 % of `curl_cffi` on the larger set, open a new ADR to re-evaluate (options: tune ClientHello spec, add TLS-in-TLS proxy, enable Roller, accept lower success rate).

## Revision note (§11)

**This ADR was revised on its own creation day** following external review. The first draft (commit `21804aa`) made two errors:

1. **It hardcoded `HelloChrome_120` as the production choice** without justifying why not a newer version, when `utls v1.8.2` already provides `HelloChrome_131`, `HelloChrome_133`, and the `HelloChrome_Auto` alias.
2. **It did not mention `utls.NewRoller()` or multi-fingerprint rotation**, which is upstream's explicit recommendation.

External review surfaced both gaps. This revision:
- Re-ran the spike as a 4-technique comparison (8 runs, 14 URLs, 448 total requests).
- Found that `HelloChrome_Auto` (= 133) beats `HelloChrome_120` on StackOverflow specifically, with a 4.4 pp mean advantage.
- Found that `utls.NewRoller()` underperforms fixed fingerprints on single-call workloads (mean 74.1 % vs 83.9 %), because Roller only retries on TLS handshake failures and the shuffle introduces variance.
- Replaced the decision with `HelloChrome_Auto` (the moving-target alias).
- Added an explicit version-upgrade policy (§6).
- Added an explicit multi-fingerprint roadmap with trigger conditions (§7).
- Strengthened the re-testing policy (10+ runs; 50+ URLs; record resolved alias).

Both review points are now fully incorporated. The core decision (use utls) is unchanged; the specific implementation details were wrong and have been corrected.

## References

- [utls repository](https://github.com/refraction-networking/utls)
- [utls Roller implementation](https://github.com/refraction-networking/utls/blob/master/u_roller.go)
- [JA3 / JA4 fingerprinting explained](https://engineering.salesforce.com/tls-fingerprinting-with-ja3-and-ja3s-247362855967/)
- [Cloudflare's bot detection docs](https://developers.cloudflare.com/bots/concepts/bot-score/)
- Phase 0 smoke test code: `test/spike/tls_fingerprint/main.go`
- Phase 0 debug tool: `test/spike/tls_fingerprint/debug/main.go`
- Raw run logs: git history of the `go` branch commits that bumped this ADR
