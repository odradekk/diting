// Package utls implements a fetch.Fetcher that speaks TLS with a Chrome
// ClientHello fingerprint via github.com/refraction-networking/utls, then
// dispatches the resulting connection to HTTP/1.1 or HTTP/2 based on ALPN.
//
// This is the primary fetch layer for diting v2. It replaces Python v1's
// curl_cffi. See docs/adr/0001-utls-fetch-layer.md for the decision rationale
// and §8 of that ADR for the 10 implementation constraints this file honours.
//
// Critical correctness points (from ADR 0001 §4 "Spike-discovered bug"):
//
//  1. The HelloChrome_* specs always advertise "h2, http/1.1" in ALPN
//     regardless of utls.Config.NextProtos — callers must inspect
//     NegotiatedProtocol and dispatch accordingly, or servers that pick h2
//     will silently return EOF.
//  2. HTTP/2 requests must not carry "Connection" or "Upgrade" headers
//     (illegal in h2).
//  3. Deadlines must be set on the raw TLS conn via SetDeadline — ctx alone
//     is not enough once http.Transport is bypassed.
//  4. One TCP+TLS connection per fetch; no connection reuse.
//  5. io.ErrUnexpectedEOF after a full body read is treated as success —
//     some servers close the socket the instant the body ends.
package utls

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	fetchpkg "github.com/odradekk/diting/internal/fetch"
	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// LayerName is the value assigned to fetch.Result.LayerUsed when this layer
// wins a chain race. Exported so callers / tests can match against it.
const LayerName = "utls"

// Default configuration values. Exported so callers and tests can reference
// them without duplicating magic numbers.
const (
	DefaultTimeout      = 15 * time.Second
	DefaultMaxBodyBytes = 1 << 20 // 1 MiB
	DefaultMaxRedirects = 5
)

// Options configures the utls Fetcher. Zero values are replaced with the
// Default* constants at New time.
type Options struct {
	// Timeout is the per-request overall budget. It applies to the dial,
	// TLS handshake, and request/response cycle combined.
	Timeout time.Duration

	// MaxBodyBytes caps the number of bytes read from any single response
	// body. The truncated body is returned as Content; the cap also applies
	// across redirects.
	MaxBodyBytes int64

	// MaxRedirects caps the length of redirect chains.
	MaxRedirects int

	// ClientHelloID overrides the fingerprint sent in the TLS ClientHello.
	// Defaults to utls.HelloChrome_Auto (the moving-target alias). Tests may
	// inject other fingerprints; production code must leave this nil.
	ClientHelloID *utls.ClientHelloID

	// Headers replaces the default Chrome header set. If nil, DefaultHeaders
	// is used. Intended for tests and future per-site tuning.
	Headers map[string]string

	// InsecureSkipVerify disables TLS certificate verification. Tests only.
	// Never set this in production — the whole point of utls is that TLS
	// identity is preserved while the ClientHello fingerprint is spoofed.
	InsecureSkipVerify bool
}

// Fetcher implements fetch.Fetcher using utls + http2.
type Fetcher struct {
	opts Options
}

// New constructs a Fetcher. Zero-valued fields in opts are replaced with
// their Default* constants. The Headers map is always cloned so that
// callers cannot mutate it under an in-flight Fetch (and so two fetchers
// can never corrupt each other via a shared reference to DefaultHeaders).
func New(opts Options) *Fetcher {
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if opts.MaxRedirects == 0 {
		opts.MaxRedirects = DefaultMaxRedirects
	}
	src := opts.Headers
	if src == nil {
		src = DefaultHeaders
	}
	cloned := make(map[string]string, len(src))
	for k, v := range src {
		cloned[k] = v
	}
	opts.Headers = cloned
	return &Fetcher{opts: opts}
}

// Fetch retrieves the given URL, following up to MaxRedirects redirects. It
// honours ctx for cancellation and deadlines; a per-layer Timeout is also
// applied on top of ctx via context.WithTimeout (the shorter of the two wins).
func (f *Fetcher) Fetch(ctx context.Context, targetURL string) (*fetchpkg.Result, error) {
	start := time.Now()

	// Cap ctx at our own timeout. context.WithTimeout respects any shorter
	// deadline already on ctx, so this is always safe.
	ctx, cancel := context.WithTimeout(ctx, f.opts.Timeout)
	defer cancel()

	current := targetURL
	redirects := 0
	for {
		u, err := url.Parse(current)
		if err != nil {
			return nil, f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("parse url %q: %w", current, err))
		}
		if u.Scheme != "https" {
			return nil, f.wrapErr(targetURL, fetchpkg.ErrUnknown,
				fmt.Errorf("unsupported scheme %q (utls layer speaks HTTPS only)", u.Scheme))
		}
		if u.Host == "" {
			return nil, f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("empty host in url %q", current))
		}

		once, doneErr := f.doOnce(ctx, u)
		if doneErr != nil {
			return nil, f.wrapErr(targetURL, classifyTransportError(doneErr), doneErr)
		}

		// Redirect handling.
		if once.status >= 300 && once.status < 400 && once.location != "" {
			redirects++
			if redirects > f.opts.MaxRedirects {
				return nil, f.wrapErr(targetURL, fetchpkg.ErrUnknown,
					fmt.Errorf("too many redirects (%d > %d)", redirects, f.opts.MaxRedirects))
			}
			next, err := url.Parse(once.location)
			if err != nil {
				return nil, f.wrapErr(targetURL, fetchpkg.ErrParse,
					fmt.Errorf("invalid redirect Location %q: %w", once.location, err))
			}
			if !next.IsAbs() {
				next = u.ResolveReference(next)
			}
			current = next.String()
			continue
		}

		// Classify the final status.
		if kind, ok := classifyStatus(once.status); ok {
			return nil, f.wrapErr(targetURL, kind, fmt.Errorf("http %d", once.status))
		}

		return &fetchpkg.Result{
			URL:         targetURL,
			FinalURL:    current,
			Content:     string(once.body),
			ContentType: once.contentType,
			LatencyMs:   time.Since(start).Milliseconds(),
			// LayerUsed is set by the Chain.
		}, nil
	}
}

// FetchMany loops over urls and calls Fetch serially. The Chain provides
// parallelism on top, so there is no benefit to a custom concurrent
// implementation at this layer. On ctx cancellation/deadline, the loop
// stops immediately — remaining URLs are not attempted and the joined
// error is a single wrapped LayerError rather than N duplicates.
func (f *Fetcher) FetchMany(ctx context.Context, urls []string) ([]*fetchpkg.Result, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	results := make([]*fetchpkg.Result, len(urls))
	var errs []error
	for i, u := range urls {
		if err := ctx.Err(); err != nil {
			// Wrap ctx error consistently with single-URL Fetch and stop
			// attempting further URLs — callers see one canonical error,
			// not one per remaining URL.
			errs = append(errs, f.wrapErr(u, classifyTransportError(err), err))
			break
		}
		r, err := f.Fetch(ctx, u)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		results[i] = r
	}
	if len(errs) == 0 {
		return results, nil
	}
	return results, errors.Join(errs...)
}

// Close is a no-op: this layer uses fresh TCP+TLS per fetch (see ADR 0001 §8
// point 6), so there is no persistent state to release.
func (f *Fetcher) Close() error { return nil }

// wrapErr constructs a *fetch.LayerError tagged with this layer's name.
func (f *Fetcher) wrapErr(targetURL string, kind fetchpkg.ErrKind, err error) error {
	return &fetchpkg.LayerError{
		Layer: LayerName,
		URL:   targetURL,
		Kind:  kind,
		Err:   err,
	}
}

// --- single-request plumbing ------------------------------------------------

type onceResult struct {
	status      int
	body        []byte
	contentType string
	location    string
	proto       string // "h2" or "h1"
}

// doOnce performs exactly one TCP dial + TLS handshake + HTTP request/
// response cycle. Redirects are handled one level up in Fetch.
func (f *Fetcher) doOnce(ctx context.Context, u *url.URL) (*onceResult, error) {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	dialer := &net.Dialer{Timeout: f.opts.Timeout}
	tcpConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcp dial: %w", err)
	}

	helloID := utls.HelloChrome_Auto
	if f.opts.ClientHelloID != nil {
		helloID = *f.opts.ClientHelloID
	}

	cfg := &utls.Config{
		ServerName:         host,
		NextProtos:         []string{"h2", "http/1.1"}, // cosmetic; HelloChrome_* overrides this
		InsecureSkipVerify: f.opts.InsecureSkipVerify,
	}
	tlsConn := utls.UClient(tcpConn, cfg, helloID)

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = tcpConn.Close()
		return nil, fmt.Errorf("tls handshake: %w", err)
	}

	return f.finishOverUTLS(ctx, tlsConn, u)
}

// finishOverUTLS takes an already-handshake'd utls conn and drives either
// the h2 or h1 request/response cycle based on ALPN. It always closes the
// underlying conn before returning.
func (f *Fetcher) finishOverUTLS(ctx context.Context, tlsConn *utls.UConn, u *url.URL) (*onceResult, error) {
	proto := tlsConn.ConnectionState().NegotiatedProtocol

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		_ = tlsConn.Close()
		return nil, fmt.Errorf("new request: %w", err)
	}
	applyHeaders(req, f.opts.Headers)

	// Apply a deadline on the raw conn. ADR 0001 §8 point 3: ctx alone is
	// not enough once http.Transport is bypassed.
	deadline := time.Now().Add(f.opts.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = tlsConn.SetDeadline(deadline)

	switch proto {
	case "h2":
		return f.doH2(tlsConn, req, f.opts.MaxBodyBytes)
	default:
		return f.doH1(tlsConn, req, f.opts.MaxBodyBytes)
	}
}

// doH1 speaks HTTP/1.1 directly over the raw TLS conn.
func (f *Fetcher) doH1(tlsConn net.Conn, req *http.Request, maxBody int64) (*onceResult, error) {
	defer tlsConn.Close()

	// Request conn-close so the server doesn't try to keep the (fresh) conn
	// alive — we'll drop it after this single RoundTrip anyway.
	req.Header.Set("Connection", "close")

	if err := req.Write(tlsConn); err != nil {
		return nil, fmt.Errorf("h1 write: %w", err)
	}

	br := bufio.NewReader(tlsConn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, fmt.Errorf("h1 read response: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := readBody(resp.Body, resp.ContentLength, resp.Header.Get("Content-Encoding"), maxBody)
	if readErr != nil {
		return nil, fmt.Errorf("h1 read body: %w", readErr)
	}
	return &onceResult{
		proto:       "h1",
		status:      resp.StatusCode,
		body:        body,
		contentType: resp.Header.Get("Content-Type"),
		location:    resp.Header.Get("Location"),
	}, nil
}

// doH2 wraps the raw TLS conn in an http2.ClientConn and drives a single
// RoundTrip.
func (f *Fetcher) doH2(tlsConn net.Conn, req *http.Request, maxBody int64) (*onceResult, error) {
	transport := &http2.Transport{AllowHTTP: false}

	h2Conn, err := transport.NewClientConn(tlsConn)
	if err != nil {
		_ = tlsConn.Close()
		return nil, fmt.Errorf("h2 new client conn: %w", err)
	}
	defer h2Conn.Close() // also closes the underlying tlsConn

	// Connection / Upgrade are illegal in h2.
	req.Header.Del("Connection")
	req.Header.Del("Upgrade")

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("h2 roundtrip: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := readBody(resp.Body, resp.ContentLength, resp.Header.Get("Content-Encoding"), maxBody)
	if readErr != nil {
		return nil, fmt.Errorf("h2 read body: %w", readErr)
	}
	return &onceResult{
		proto:       "h2",
		status:      resp.StatusCode,
		body:        body,
		contentType: resp.Header.Get("Content-Type"),
		location:    resp.Header.Get("Location"),
	}, nil
}

// readBody reads up to maxBody bytes from body, transparently decompressing
// per contentEncoding.
//
// Truncation handling splits along the identity / compressed axis:
//
//   - Identity bodies (no encoding, or "identity"): apply the late-EOF
//     tolerance policy documented below. contentLength (wire bytes) and
//     len(data) (decoded bytes) are the same quantity, so the comparisons
//     are well-defined.
//   - Compressed bodies (gzip/deflate/br/zstd): the decoder is the
//     authoritative signal for stream completeness. Any reader error —
//     including ErrUnexpectedEOF — is treated as a real corruption and
//     surfaced up the chain. We do NOT compare decoded length against
//     contentLength (which is the compressed wire length and has no fixed
//     relationship to the decoded byte count, so the comparison is
//     meaningless and would silently accept truncated gzip/br/zstd).
//
// Identity-body late-EOF tolerance policy:
//
//  1. If contentLength < 0 (close-delimited or unknown), forgive it. The
//     server wasn't obligated to tell us when to stop reading.
//  2. If we received at least maxBody bytes, forgive it. We stopped reading,
//     not the server; the underlying stream may still be healthy.
//  3. If we received at least contentLength bytes, forgive it. The body
//     arrived in full; the late EOF is just a graceless socket teardown
//     (ADR 0001 §8 point 5).
//  4. Otherwise, surface the error — the response was genuinely truncated.
func readBody(body io.ReadCloser, contentLength int64, contentEncoding string, maxBody int64) ([]byte, error) {
	reader, closer, err := decompressor(body, contentEncoding)
	if err != nil {
		return nil, fmt.Errorf("decompress (%s): %w", contentEncoding, err)
	}
	if closer != nil {
		defer closer.Close()
	}

	data, readErr := io.ReadAll(io.LimitReader(reader, maxBody))
	if readErr == nil {
		return data, nil
	}

	if !isIdentityEncoding(contentEncoding) {
		// Compressed stream: any error from the decoder means the stream
		// is corrupt. The chain will classify this as ErrTransport and
		// fall through to the next layer.
		return data, fmt.Errorf("decode compressed body (%s): %w", contentEncoding, readErr)
	}

	if !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return data, readErr
	}
	// Identity body, late UnexpectedEOF: tri-clause forgiveness.
	if contentLength < 0 {
		return data, nil
	}
	if int64(len(data)) >= maxBody {
		return data, nil
	}
	if int64(len(data)) >= contentLength {
		return data, nil
	}
	return data, fmt.Errorf("truncated body: got %d of %d bytes: %w", len(data), contentLength, readErr)
}

// isIdentityEncoding reports whether an HTTP Content-Encoding value names
// the identity (no-op) encoding. Empty string, "identity", and whitespace
// variants all count.
func isIdentityEncoding(encoding string) bool {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return true
	default:
		return false
	}
}

// decompressor wraps r with the appropriate decoder for encoding. It returns
// the decoded reader and an optional closer that the caller should Close
// when done (for decoders that hold resources — zstd, gzip, zlib). The
// closer is nil when no extra cleanup is needed.
//
// Unknown encodings fall back to identity (pass-through) rather than erroring,
// because real-world servers sometimes send non-standard names and garbled
// content is usually preferable to a hard error for a single request.
// Content-Encoding: identity or "" returns the body unchanged.
func decompressor(body io.ReadCloser, encoding string) (io.Reader, io.Closer, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body, nil, nil
	case "gzip", "x-gzip":
		gz, err := gzip.NewReader(body)
		if err != nil {
			return nil, nil, err
		}
		return gz, gz, nil
	case "deflate":
		// RFC 7230 §4.2.2 says HTTP "deflate" means zlib-wrapped DEFLATE
		// (RFC 1950). A small number of historical servers send raw
		// DEFLATE (RFC 1951) under the same name; Chrome tolerates both.
		// We disambiguate by running the full RFC 1950 header validity
		// check (CM, CINFO, FCHECK) — see isZlibHeader below. Random
		// raw-DEFLATE prefixes satisfy all three conditions with
		// probability on the order of 1/1000, and raw DEFLATE is
		// already vanishingly rare in real HTTP, so the residual
		// false-positive rate is effectively zero.
		br := bufio.NewReaderSize(body, 4)
		peek, _ := br.Peek(2)
		if isZlibHeader(peek) {
			zr, err := zlib.NewReader(br)
			if err != nil {
				return nil, nil, fmt.Errorf("deflate: zlib header parse: %w", err)
			}
			return zr, zr, nil
		}
		fr := flate.NewReader(br)
		return fr, fr, nil
	case "br":
		return brotli.NewReader(body), nil, nil
	case "zstd":
		zr, err := zstd.NewReader(body)
		if err != nil {
			return nil, nil, err
		}
		return zr, zstdCloser{zr}, nil
	default:
		// Unknown — best-effort identity pass-through.
		return body, nil, nil
	}
}

// zstdCloser adapts zstd.Decoder (which has Close returning void) to io.Closer.
type zstdCloser struct{ d *zstd.Decoder }

func (c zstdCloser) Close() error { c.d.Close(); return nil }

// isZlibHeader reports whether the first two bytes of a stream form a
// valid RFC 1950 zlib header. Used as the disambiguator for HTTP
// Content-Encoding: deflate — real zlib always satisfies all three
// conditions; random raw-DEFLATE prefixes satisfy them on the order of
// 1/1000 of the time (CM=8 is 1/16, CINFO≤7 eliminates half of the
// remaining CMF space, and FCHECK is ~1/31 — compounding to roughly
// 1/1000 over all 65536 byte pairs).
//
// Conditions (from RFC 1950 §2.2):
//  1. CMF low nibble (CM) must be 8 (deflate compression method).
//  2. CMF high nibble (CINFO) must be ≤ 7 — "Values of CINFO above 7 are
//     not allowed in this version of the specification."
//  3. (CMF*256 + FLG) must be a multiple of 31 (FCHECK checksum).
func isZlibHeader(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	cmf, flg := b[0], b[1]
	if cmf&0x0F != 8 {
		return false
	}
	if cmf>>4 > 7 {
		return false
	}
	return (uint16(cmf)<<8|uint16(flg))%31 == 0
}

// --- classification ---------------------------------------------------------

// classifyTransportError maps a dial / handshake / read error into an
// ErrKind. Context cancellation / deadlines are preserved so the Chain can
// short-circuit cleanly.
func classifyTransportError(err error) fetchpkg.ErrKind {
	if err == nil {
		return fetchpkg.ErrUnknown
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fetchpkg.ErrTimeout
	case errors.Is(err, context.Canceled):
		return fetchpkg.ErrCanceled
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fetchpkg.ErrTimeout
	}
	// TLS alerts, DNS failures, connection refused, EOF → transport.
	return fetchpkg.ErrTransport
}

// classifyStatus returns the ErrKind for a non-2xx HTTP response. The second
// return value is false for 2xx responses (which are not errors).
//
// Classification policy:
//   - 2xx                  → success (no error)
//   - 401, 403, 429        → blocked (bot protection, auth wall, rate limit)
//   - 404, 410             → not found
//   - 5xx                  → transport (server-side, often transient)
//   - other 4xx            → unknown (client error, needs human inspection)
func classifyStatus(status int) (fetchpkg.ErrKind, bool) {
	switch {
	case status >= 200 && status < 300:
		return 0, false
	case status == http.StatusUnauthorized, status == http.StatusForbidden, status == http.StatusTooManyRequests:
		return fetchpkg.ErrBlocked, true
	case status == http.StatusNotFound, status == http.StatusGone:
		return fetchpkg.ErrNotFound, true
	case status >= 500 && status < 600:
		return fetchpkg.ErrTransport, true
	default:
		return fetchpkg.ErrUnknown, true
	}
}

// --- headers ----------------------------------------------------------------

// DefaultHeaders is the Chrome-browser-style header set sent by the utls
// Fetcher. Values are tuned to match HelloChrome_Auto's current alias
// (HelloChrome_133 as of utls v1.8.2). Bump in lockstep with utls upgrades;
// see ADR 0001 §8 point 8.
var DefaultHeaders = map[string]string{
	"User-Agent":                "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
	"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
	"Accept-Language":           "en-US,en;q=0.9",
	"Accept-Encoding":           "gzip, deflate, br, zstd",
	"Sec-Ch-Ua":                 `"Not_A Brand";v="8", "Chromium";v="133", "Google Chrome";v="133"`,
	"Sec-Ch-Ua-Mobile":          "?0",
	"Sec-Ch-Ua-Platform":        `"Linux"`,
	"Sec-Fetch-Dest":            "document",
	"Sec-Fetch-Mode":            "navigate",
	"Sec-Fetch-Site":            "none",
	"Sec-Fetch-User":            "?1",
	"Upgrade-Insecure-Requests": "1",
	"Cache-Control":             "max-age=0",
}

func applyHeaders(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
}
