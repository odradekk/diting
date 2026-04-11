// Debug utility: dump raw bytes received from a utls connection to a single
// site, to diagnose why http.ReadResponse fails in the smoke test.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	utls "github.com/refraction-networking/utls"
)

func main() {
	host := "github.com"
	if len(os.Args) > 1 {
		host = os.Args[1]
	}
	addr := host + ":443"

	fmt.Fprintf(os.Stderr, "--- dialing %s ---\n", addr)
	tcpConn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(
		context.Background(), "tcp", addr,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial: %v\n", err)
		os.Exit(1)
	}
	defer tcpConn.Close()

	tlsConn := utls.UClient(tcpConn, &utls.Config{
		ServerName: host,
		NextProtos: []string{"http/1.1"},
	}, utls.HelloChrome_120)

	fmt.Fprintf(os.Stderr, "--- handshaking ---\n")
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "handshake: %v\n", err)
		os.Exit(1)
	}

	state := tlsConn.ConnectionState()
	fmt.Fprintf(os.Stderr, "  version: %x\n", state.Version)
	fmt.Fprintf(os.Stderr, "  cipher:  %x\n", state.CipherSuite)
	fmt.Fprintf(os.Stderr, "  alpn:    %q\n", state.NegotiatedProtocol)
	fmt.Fprintf(os.Stderr, "  sni:     %q\n", state.ServerName)

	// Write a minimal HTTP/1.1 request manually.
	req := fmt.Sprintf(
		"GET / HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"User-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36\r\n"+
			"Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8\r\n"+
			"Accept-Language: en-US,en;q=0.9\r\n"+
			"Accept-Encoding: identity\r\n"+
			"Connection: close\r\n"+
			"\r\n",
		host,
	)
	fmt.Fprintf(os.Stderr, "--- writing request (%d bytes) ---\n%s", len(req), req)

	if _, err := tlsConn.Write([]byte(req)); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}

	// Dump raw bytes.
	fmt.Fprintf(os.Stderr, "--- reading response (first 512 bytes) ---\n")
	_ = tlsConn.SetReadDeadline(time.Now().Add(15 * time.Second))
	buf := make([]byte, 512)
	n, err := io.ReadFull(tlsConn, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		fmt.Fprintf(os.Stderr, "read: %v (got %d bytes)\n", err, n)
	}
	fmt.Fprintf(os.Stderr, "got %d bytes\n", n)
	if n > 0 {
		// Print printable ASCII preview + hex dump of first 64 bytes.
		fmt.Fprintf(os.Stderr, "\nASCII preview:\n")
		preview := buf[:n]
		for _, b := range preview {
			if b >= 32 && b < 127 {
				os.Stderr.Write([]byte{b})
			} else if b == '\r' {
				os.Stderr.WriteString("\\r")
			} else if b == '\n' {
				os.Stderr.WriteString("\\n\n")
			} else {
				fmt.Fprintf(os.Stderr, "\\x%02x", b)
			}
		}
		fmt.Fprintf(os.Stderr, "\n\nHex (first 64 bytes):\n")
		for i := 0; i < n && i < 64; i++ {
			fmt.Fprintf(os.Stderr, "%02x ", buf[i])
			if (i+1)%16 == 0 {
				fmt.Fprintln(os.Stderr)
			}
		}
		fmt.Fprintln(os.Stderr)
	}
}
