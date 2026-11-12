package stress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// The stress suite talks only to a gateway it starts itself on a loopback listener, with a fake upstream. This file
// is the one place in the package that builds an HTTP client or request, so that "the suite never leaves this
// machine" is enforced in one spot (and the egress test allowlists this file alone). The dialer refuses any address
// that is not a loopback IP literal, so a mistaken URL fails instead of connecting. No proxy is used.

// loopbackClient returns an HTTP client that can only connect to loopback addresses. maxIdle sets the idle
// connections kept per host; 0 uses the default.
func loopbackClient(timeout time.Duration, maxIdle int) *http.Client {
	d := &net.Dialer{Timeout: timeout}
	tr := &http.Transport{
		MaxIdleConnsPerHost: maxIdle,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("the stress suite only connects to loopback addresses, not %q", host)
			}
			return d.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// newRequest builds a request. It dials nothing; the client it is sent with decides where it can go.
func newRequest(method, url string, body io.Reader) (*http.Request, error) {
	return http.NewRequest(method, url, body)
}
