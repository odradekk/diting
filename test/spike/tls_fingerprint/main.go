// Package main is a throwaway spike that validates whether utls with a
// Chrome 120 ClientHello fingerprint can reach sites that reject stdlib
// net/http (whose TLS fingerprint is distinctly Go and easily detected).
//
// This version correctly handles ALPN-negotiated HTTP/2 by routing the
// utls connection through golang.org/x/net/http2.  HelloChrome_120
// advertises "h2, http/1.1" regardless of utls.Config.NextProtos, so we
// must speak whichever protocol the server picks.
//
// Gate: utls must achieve >= 80% of the target success rate on the "hard"
// URL set.  If it does not, we re-evaluate the Go rewrite vs. staying on
// Python + CLI.
//
// Usage:
//
//	go run ./test/spike/tls_fingerprint/
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// --- URL set ---------------------------------------------------------------

type difficulty string

const (
	diffEasy   difficulty = "easy"
	diffMedium difficulty = "medium"
	diffHard   difficulty = "hard"
)

type target struct {
	url  string
	diff difficulty
	note string
}

var targets = []target{
	// --- easy baseline ---
	{"https://github.com/torvalds/linux", diffEasy, "GitHub public repo"},
	{"https://news.ycombinator.com/", diffEasy, "Hacker News"},
	{"https://en.wikipedia.org/wiki/State_space_model", diffEasy, "Wikipedia article"},
	{"https://arxiv.org/abs/2111.00396", diffEasy, "arXiv paper (S4)"},
	{"https://docs.python.org/3/library/asyncio-task.html", diffEasy, "Python docs"},

	// --- medium: Cloudflare protected but generally tolerant ---
	{"https://stackoverflow.com/questions/tagged/asyncio", diffMedium, "StackOverflow tag page (CF)"},
	{"https://www.cloudflare.com/learning/", diffMedium, "Cloudflare's own site"},
	{"https://openai.com/research/", diffMedium, "OpenAI (CF)"},
	{"https://www.anthropic.com/research", diffMedium, "Anthropic (CF)"},

	// --- hard: known aggressive bot detection ---
	{"https://medium.com/@sergioli/paging-attention-86f99b3e3fc8", diffHard, "Medium article (anti-bot)"},
	{"https://www.g2.com/products/postgresql/reviews", diffHard, "G2 (heavy CF rules)"},
	{"https://www.linkedin.com/pulse/state-space-models-ssm-brief-overview-dr-aboubaker-abdelbagi-7tdfe", diffHard, "LinkedIn public article (strict)"},
	{"https://www.quora.com/What-are-state-space-models-in-deep-learning", diffHard, "Quora (CF aggressive)"},
	{"https://x.com/karpathy", diffHard, "X/Twitter profile (hostile)"},
}

// --- Result ----------------------------------------------------------------

type result struct {
	url       string
	diff      difficulty
	technique string
	proto     string // "h1", "h2", or "" if we never negotiated
	status    int
	bodySize  int
	duration  time.Duration
	err       error
	redirects int
}

func (r result) ok() bool {
	return r.err == nil && r.status >= 200 && r.status < 400 && r.bodySize > 0
}

func (r result) statusSymbol() string {
	if r.err != nil {
		return "ERR"
	}
	switch {
	case r.status >= 200 && r.status < 300:
		return "OK"
	case r.status >= 300 && r.status < 400:
		return fmt.Sprintf("3%02d", r.status-300)
	case r.status >= 400 && r.status < 500:
		return fmt.Sprintf("4%02d", r.status-400)
	case r.status >= 500:
		return fmt.Sprintf("5%02d", r.status-500)
	}
	return fmt.Sprintf("%d", r.status)
}

// --- Headers we mimic ------------------------------------------------------

var chromeHeaders = map[string]string{
	"User-Agent":                "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
	"Accept-Language":           "en-US,en;q=0.9",
	"Accept-Encoding":           "identity",
	"Sec-Ch-Ua":                 `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`,
	"Sec-Ch-Ua-Mobile":          "?0",
	"Sec-Ch-Ua-Platform":        `"Linux"`,
	"Sec-Fetch-Dest":            "document",
	"Sec-Fetch-Mode":            "navigate",
	"Sec-Fetch-Site":            "none",
	"Sec-Fetch-User":            "?1",
	"Upgrade-Insecure-Requests": "1",
	"Cache-Control":             "max-age=0",
}

func applyChromeHeaders(req *http.Request) {
	for k, v := range chromeHeaders {
		req.Header.Set(k, v)
	}
}

// --- Technique 1: stdlib net/http ------------------------------------------

func fetchNetHTTP(targetURL string, timeout time.Duration) result {
	start := time.Now()
	r := result{url: targetURL, technique: "net/http"}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			r.redirects++
			return nil
		},
	}

	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		r.err = err
		r.duration = time.Since(start)
		return r
	}
	applyChromeHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		r.err = err
		r.duration = time.Since(start)
		return r
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		r.err = err
		r.duration = time.Since(start)
		return r
	}

	r.proto = resp.Proto
	r.status = resp.StatusCode
	r.bodySize = len(body)
	r.duration = time.Since(start)
	return r
}

// --- Technique 2: utls + Chrome 120 + transparent h2/h1 dispatch -----------

// fetchUTLS dials, does the Chrome 120 TLS handshake, then dispatches to
// either HTTP/1.1 or HTTP/2 based on what ALPN negotiated.  The HTTP/2
// path uses golang.org/x/net/http2's Transport which knows how to speak
// h2 over an already-established TLS connection.

func fetchUTLS(targetURL string, timeout time.Duration) result {
	start := time.Now()
	r := result{url: targetURL, technique: "utls+chrome120"}

	// redirect loop (max 5)
	currentURL := targetURL
	redirects := 0
	for {
		once := doUTLSOnce(currentURL, timeout)
		r.proto = once.proto

		if once.err != nil {
			r.err = once.err
			r.status = once.status
			r.bodySize = once.bodySize
			r.redirects = redirects
			r.duration = time.Since(start)
			return r
		}

		if once.status >= 300 && once.status < 400 && once.location != "" {
			redirects++
			if redirects > 5 {
				r.err = errors.New("too many redirects")
				r.redirects = redirects
				r.duration = time.Since(start)
				return r
			}
			next, err := url.Parse(once.location)
			if err != nil {
				r.err = fmt.Errorf("invalid redirect URL: %w", err)
				r.duration = time.Since(start)
				return r
			}
			if !next.IsAbs() {
				base, _ := url.Parse(currentURL)
				next = base.ResolveReference(next)
			}
			currentURL = next.String()
			continue
		}

		r.status = once.status
		r.bodySize = once.bodySize
		r.redirects = redirects
		r.duration = time.Since(start)
		return r
	}
}

type utlsOnce struct {
	proto    string
	status   int
	bodySize int
	location string
	err      error
}

func doUTLSOnce(targetURL string, timeout time.Duration) utlsOnce {
	u, err := url.Parse(targetURL)
	if err != nil {
		return utlsOnce{err: fmt.Errorf("parse url: %w", err)}
	}
	if u.Scheme != "https" {
		return utlsOnce{err: fmt.Errorf("non-https not supported: %s", u.Scheme)}
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	tcpConn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return utlsOnce{err: fmt.Errorf("tcp dial: %w", err)}
	}

	tlsConn := utls.UClient(tcpConn, &utls.Config{
		ServerName: host,
		NextProtos: []string{"h2", "http/1.1"},
	}, utls.HelloChrome_120)

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		tcpConn.Close()
		return utlsOnce{err: fmt.Errorf("tls handshake: %w", err)}
	}

	proto := tlsConn.ConnectionState().NegotiatedProtocol

	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		tcpConn.Close()
		return utlsOnce{proto: proto, err: fmt.Errorf("new request: %w", err)}
	}
	applyChromeHeaders(req)

	switch proto {
	case "h2":
		return doH2(ctx, tlsConn, req, proto, timeout)
	default:
		// "http/1.1" or "" — some servers skip ALPN; default to HTTP/1.1
		return doH1(tlsConn, req, "h1", timeout)
	}
}

// --- HTTP/1.1 over utls ---

func doH1(tlsConn net.Conn, req *http.Request, proto string, timeout time.Duration) utlsOnce {
	defer tlsConn.Close()

	req.Header.Set("Connection", "close")
	_ = tlsConn.SetWriteDeadline(time.Now().Add(timeout))
	if err := req.Write(tlsConn); err != nil {
		return utlsOnce{proto: proto, err: fmt.Errorf("h1 write: %w", err)}
	}

	_ = tlsConn.SetReadDeadline(time.Now().Add(timeout))
	br := bufio.NewReader(tlsConn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return utlsOnce{proto: proto, err: fmt.Errorf("h1 read: %w", err)}
	}
	defer resp.Body.Close()

	location := resp.Header.Get("Location")
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return utlsOnce{proto: proto, status: resp.StatusCode, bodySize: len(body), location: location,
			err: fmt.Errorf("h1 body: %w", err)}
	}
	return utlsOnce{
		proto:    proto,
		status:   resp.StatusCode,
		bodySize: len(body),
		location: location,
	}
}

// --- HTTP/2 over utls ---
//
// http2.Transport.NewClientConn wraps an already-TLS-established net.Conn
// and lets us RoundTrip HTTP/2 requests over it.  The request must not
// carry the "Connection" header (illegal in h2).

func doH2(ctx context.Context, tlsConn net.Conn, req *http.Request, proto string, timeout time.Duration) utlsOnce {
	transport := &http2.Transport{
		AllowHTTP: false,
	}
	h2Conn, err := transport.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return utlsOnce{proto: proto, err: fmt.Errorf("h2 new conn: %w", err)}
	}
	defer h2Conn.Close()

	req = req.WithContext(ctx)
	req.Header.Del("Connection")
	req.Header.Del("Upgrade")

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		return utlsOnce{proto: proto, err: fmt.Errorf("h2 rt: %w", err)}
	}
	defer resp.Body.Close()

	location := resp.Header.Get("Location")
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return utlsOnce{proto: proto, status: resp.StatusCode, bodySize: len(body), location: location,
			err: fmt.Errorf("h2 body: %w", err)}
	}
	return utlsOnce{
		proto:    proto,
		status:   resp.StatusCode,
		bodySize: len(body),
		location: location,
	}
}

// --- Runner ----------------------------------------------------------------

func runAll(targets []target, timeout time.Duration, concurrency int) []result {
	type job struct {
		idx       int
		t         target
		technique string
	}

	techniques := []string{"net/http", "utls+chrome120"}

	totalJobs := len(targets) * len(techniques)
	jobs := make(chan job, totalJobs)
	results := make([]result, totalJobs)

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				var r result
				switch j.technique {
				case "net/http":
					r = fetchNetHTTP(j.t.url, timeout)
				case "utls+chrome120":
					r = fetchUTLS(j.t.url, timeout)
				}
				r.diff = j.t.diff
				results[j.idx] = r
			}
		}()
	}

	idx := 0
	for _, t := range targets {
		for _, tech := range techniques {
			jobs <- job{idx: idx, t: t, technique: tech}
			idx++
		}
	}
	close(jobs)
	wg.Wait()

	return results
}

// --- Reporting -------------------------------------------------------------

type diffStats struct {
	total  int
	nhtOK  int
	utlsOK int
}

func report(results []result) {
	byURL := map[string][]result{}
	order := []string{}
	for _, r := range results {
		if _, ok := byURL[r.url]; !ok {
			order = append(order, r.url)
		}
		byURL[r.url] = append(byURL[r.url], r)
	}

	tgtOrder := map[string]int{}
	for i, t := range targets {
		tgtOrder[t.url] = i
	}
	sort.Slice(order, func(i, j int) bool {
		return tgtOrder[order[i]] < tgtOrder[order[j]]
	})

	fmt.Println()
	fmt.Println("╔════════════════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  diting v2 — utls TLS fingerprint smoke test                                              ║")
	fmt.Println("╚════════════════════════════════════════════════════════════════════════════════════════════╝")
	fmt.Println()

	fmt.Printf("%-6s  %-48s  %-24s  %-30s\n", "DIFF", "URL", "net/http", "utls+chrome120")
	fmt.Println(strings.Repeat("─", 116))

	var (
		nhtTotal, nhtOK   int
		utlsTotal, utlsOK int
		byDiff            = map[difficulty]*diffStats{
			diffEasy:   {},
			diffMedium: {},
			diffHard:   {},
		}
	)

	for _, u := range order {
		rs := byURL[u]
		var nht, ut result
		for _, r := range rs {
			switch r.technique {
			case "net/http":
				nht = r
			case "utls+chrome120":
				ut = r
			}
		}

		urlShort := u
		if len(urlShort) > 48 {
			urlShort = urlShort[:45] + "..."
		}
		fmt.Printf("%-6s  %-48s  %-24s  %-30s\n",
			nht.diff, urlShort, formatResult(nht), formatResult(ut))

		nhtTotal++
		if nht.ok() {
			nhtOK++
		}
		utlsTotal++
		if ut.ok() {
			utlsOK++
		}

		ds := byDiff[nht.diff]
		ds.total++
		if nht.ok() {
			ds.nhtOK++
		}
		if ut.ok() {
			ds.utlsOK++
		}
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 116))
	fmt.Println("Summary")
	fmt.Println(strings.Repeat("─", 116))

	fmt.Printf("  Overall:         net/http %2d/%-2d (%5.1f%%)    utls+chrome120 %2d/%-2d (%5.1f%%)\n",
		nhtOK, nhtTotal, pct(nhtOK, nhtTotal),
		utlsOK, utlsTotal, pct(utlsOK, utlsTotal),
	)

	for _, d := range []difficulty{diffEasy, diffMedium, diffHard} {
		ds := byDiff[d]
		if ds.total == 0 {
			continue
		}
		fmt.Printf("  %-15s  net/http %2d/%-2d (%5.1f%%)    utls+chrome120 %2d/%-2d (%5.1f%%)\n",
			string(d)+":",
			ds.nhtOK, ds.total, pct(ds.nhtOK, ds.total),
			ds.utlsOK, ds.total, pct(ds.utlsOK, ds.total),
		)
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 116))
	fmt.Println("Gate check")
	fmt.Println(strings.Repeat("─", 116))
	utlsPct := pct(utlsOK, utlsTotal)
	const gateTarget = 80.0
	fmt.Printf("  Gate:    utls+chrome120 success rate must be >= %.0f%%\n", gateTarget)
	fmt.Printf("  Actual:  %.1f%%\n", utlsPct)
	if utlsPct >= gateTarget {
		fmt.Println("  Verdict: PASS — proceed with Go rewrite Phase 1 (fetch layer)")
	} else if utlsPct >= gateTarget-10 {
		fmt.Println("  Verdict: MARGINAL — investigate hard-URL failures before committing")
	} else {
		fmt.Println("  Verdict: FAIL — re-evaluate Go vs Python CLI path")
	}
	fmt.Println()
}

func formatResult(r result) string {
	if r.technique == "" {
		return "-"
	}
	if r.err != nil {
		errStr := r.err.Error()
		if len(errStr) > 26 {
			errStr = errStr[:23] + "..."
		}
		return fmt.Sprintf("ERR %s", errStr)
	}
	kb := r.bodySize / 1024
	proto := r.proto
	if proto == "" {
		proto = "?"
	}
	return fmt.Sprintf("%s %4dKB %4dms %s", r.statusSymbol(), kb, r.duration.Milliseconds(), proto)
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}

// --- main ------------------------------------------------------------------

func main() {
	fmt.Printf("diting v2 utls smoke test — %d targets\n", len(targets))
	fmt.Println("Running 2 techniques per URL (net/http, utls+chrome120 w/ h2 dispatch)...")
	fmt.Println()

	results := runAll(targets, 20*time.Second, 4)
	report(results)
}
