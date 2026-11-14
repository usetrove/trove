package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// DNS-rebinding defence: only a loopback Host is served. This is the wider matrix of look-alike hosts an attacker
// page could arrange (the basic cases are in TestGuards).
func TestHostHeaderMatrix(t *testing.T) {
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	cases := map[string]int{
		"127.0.0.1": 200, "127.0.0.1:1": 200, "localhost": 200, "LOCALHOST:8787": 200, "[::1]": 200, "[::1]:8787": 200,
		"localhost.evil.com": 403, "localhost.evil.com:8787": 403, "evil.com#localhost": 403,
		"127.0.0.1@evil.com": 403, "127.0.0.1.nip.io": 403, "127.0.0.1.evil.com:8787": 403,
		"evil.com:8787@127.0.0.1": 403, "[::1].evil.com": 403, "0.0.0.0": 403, "0.0.0.0:8787": 403,
		"169.254.169.254": 403, "192.168.0.1:8787": 403, "": 403, " ": 403, "localhost. evil.com": 403,
		"2130706433": 403, "0x7f.0.0.1": 403, "[::ffff:127.0.0.1]": 403,
	}
	for host, want := range cases {
		req := httptest.NewRequest("GET", "/trove/health", nil)
		req.Host = host
		req.Header.Set("x-api-key", fakeKey)
		rr := httptest.NewRecorder()
		g.handler.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Errorf("Host %q: got %d, want %d", host, rr.Code, want)
		}
	}
}
