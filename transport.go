package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper uses a fresh Chrome-like HTTP/2 connection for every probe.
// A dedicated connection lets rotating proxy services choose a new exit per attempt.
type utlsRoundTripper struct {
	dialer proxy.Dialer
}

type closeConnectionBody struct {
	io.ReadCloser
	closeConnection func() error
	once            sync.Once
	err             error
}

func (b *closeConnectionBody) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() {
		var errConnection error
		if b.closeConnection != nil {
			errConnection = b.closeConnection()
		}
		var errBody error
		if b.ReadCloser != nil {
			errBody = b.ReadCloser.Close()
		}
		b.err = errors.Join(errBody, errConnection)
	})
	return b.err
}

func newUTLSHTTPClient(proxyURL string) *http.Client {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild == nil && mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &http.Client{Transport: &utlsRoundTripper{dialer: dialer}}
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	h2Conn, errCreate := t.createConnection(req.Context(), hostname, net.JoinHostPort(hostname, port))
	if errCreate != nil {
		return nil, errCreate
	}
	resp, errRoundTrip := h2Conn.RoundTrip(req)
	if errRoundTrip != nil {
		_ = h2Conn.Close()
		return nil, errRoundTrip
	}
	if resp == nil {
		_ = h2Conn.Close()
		return nil, errors.New("utls: upstream returned an empty response")
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	resp.Body = &closeConnectionBody{ReadCloser: resp.Body, closeConnection: h2Conn.Close}
	return resp, nil
}

func (t *utlsRoundTripper) createConnection(ctx context.Context, host, addr string) (*http2.ClientConn, error) {
	contextDialer, ok := t.dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("utls: proxy dialer does not support context cancellation")
	}
	conn, errDial := contextDialer.DialContext(ctx, "tcp", addr)
	if errDial != nil {
		return nil, fmt.Errorf("utls: dial upstream: %w", errDial)
	}
	tlsConn := tls.UClient(conn, &tls.Config{ServerName: host}, tls.HelloChrome_Auto)
	if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("utls: TLS handshake: %w", errHandshake)
	}
	h2Conn, errHTTP2 := (&http2.Transport{}).NewClientConn(tlsConn)
	if errHTTP2 != nil {
		_ = tlsConn.Close()
		return nil, fmt.Errorf("utls: initialize HTTP/2 connection: %w", errHTTP2)
	}
	return h2Conn, nil
}
