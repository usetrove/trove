package cli

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// gatewayPointerStatus runs doctor with a given ANTHROPIC_BASE_URL and returns the gateway address check.
func gatewayPointerStatus(t *testing.T, baseURL string) check {
	t.Helper()
	h := newHarness(t, map[string]string{"ANTHROPIC_BASE_URL": baseURL})
	dir := filepath.Join(t.TempDir(), "data")
	if code := h.run("doctor", "--json", "--data-dir", dir); code != 0 {
		t.Fatalf("doctor exit %d: %s", code, h.err)
	}
	return find(decodeDoctor(t, h), "gateway address")
}

func TestDoctorWarnsWhenBaseURLPointsAtAStoppedLocalGateway(t *testing.T) {
	// Find a free loopback port, then release it so nothing is listening there.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	c := gatewayPointerStatus(t, "http://127.0.0.1:"+strconv.Itoa(port)+"/anthropic")
	if c.Status != statusWarn || c.Fix == "" {
		t.Fatalf("a stopped local gateway should warn with a fix, got %+v", c)
	}
	if strings.Contains(c.Detail, "/anthropic") {
		t.Errorf("the detail should show the address only, not the path: %q", c.Detail)
	}
}

func TestDoctorAcceptsARunningLocalGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/trove/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if c := gatewayPointerStatus(t, srv.URL+"/anthropic"); c.Status != statusOK {
		t.Fatalf("a running gateway should be ok, got %+v", c)
	}
}

func TestDoctorIgnoresABaseURLThatIsNotLocal(t *testing.T) {
	if c := gatewayPointerStatus(t, "https://api.anthropic.com"); c.Status == statusWarn {
		t.Fatalf("a non-local address must not warn about a gateway, got %+v", c)
	}
}
