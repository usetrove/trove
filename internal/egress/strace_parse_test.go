package egress

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"testing"
)

var (
	inet4Re = regexp.MustCompile(`inet_addr\("([^"]*)"\)`)
	inet6Re = regexp.MustCompile(`inet_pton\(AF_INET6, "([^"]*)"`)
)

// remoteAddrs returns every non-loopback network address that strace shows a
// process connecting or sending to, and an error for any network connection
// whose address it cannot read (unknown counts as a failure, never a pass).
// Unix-domain sockets are local and are ignored.
func remoteAddrs(straceLog string) (remote []string, err error) {
	for _, line := range strings.Split(straceLog, "\n") {
		if !strings.Contains(line, "AF_INET") {
			continue // not a network address (AF_UNIX, AF_NETLINK, or not a socket call)
		}
		if strings.Contains(line, "= -1 EAFNOSUPPORT") {
			continue
		}
		var addr string
		if m := inet4Re.FindStringSubmatch(line); m != nil {
			addr = m[1]
		} else if m := inet6Re.FindStringSubmatch(line); m != nil {
			addr = m[1]
		} else {
			return remote, fmt.Errorf("cannot read the address in this strace line, so it counts as a failure: %s", line)
		}
		ip := net.ParseIP(addr)
		if ip == nil {
			return remote, fmt.Errorf("unparseable address %q in: %s", addr, line)
		}
		if ip.IsLoopback() {
			continue
		}
		// An unspecified address (0.0.0.0, ::) as a connect target means "this host".
		if ip.IsUnspecified() {
			continue
		}
		remote = append(remote, addr)
	}
	return remote, nil
}

func TestRemoteAddrsParsing(t *testing.T) {
	for name, tc := range map[string]struct {
		log     string
		remote  []string
		wantErr bool
	}{
		"loopback v4":  {log: `1234 connect(3, {sa_family=AF_INET, sin_port=htons(8787), sin_addr=inet_addr("127.0.0.1")}, 16) = 0`},
		"loopback v6":  {log: `connect(5, {sa_family=AF_INET6, sin6_port=htons(8787), sin6_flowinfo=htonl(0), inet_pton(AF_INET6, "::1", &sin6_addr), sin6_scope_id=0}, 28) = 0`},
		"unix socket":  {log: `connect(4, {sa_family=AF_UNIX, sun_path="/var/run/nscd/socket"}, 110) = -1 ENOENT`},
		"remote v4":    {log: `connect(3, {sa_family=AF_INET, sin_port=htons(443), sin_addr=inet_addr("93.184.216.34")}, 16) = -1 EINPROGRESS`, remote: []string{"93.184.216.34"}},
		"remote v6":    {log: `connect(3, {sa_family=AF_INET6, sin6_port=htons(443), sin6_flowinfo=htonl(0), inet_pton(AF_INET6, "2606:2800:220:1::1", &sin6_addr), sin6_scope_id=0}, 28) = 0`, remote: []string{"2606:2800:220:1::1"}},
		"dns over udp": {log: `sendto(5, "\1\0\1\0", 28, MSG_NOSIGNAL, {sa_family=AF_INET, sin_port=htons(53), sin_addr=inet_addr("8.8.8.8")}, 16) = 28`, remote: []string{"8.8.8.8"}},
		"mixed":        {log: "connect(3, {sa_family=AF_INET, sin_port=htons(1), sin_addr=inet_addr(\"127.0.0.1\")}, 16) = 0\nconnect(4, {sa_family=AF_INET, sin_port=htons(80), sin_addr=inet_addr(\"10.0.0.5\")}, 16) = 0", remote: []string{"10.0.0.5"}},
		"unreadable":   {log: `connect(3, {sa_family=AF_INET, sin_port=htons(80), sin_addr=0x1234}, 16) = 0`, wantErr: true},
		"no network":   {log: `openat(AT_FDCWD, "/etc/hosts", O_RDONLY) = 3`},
	} {
		got, err := remoteAddrs(tc.log)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", name, err, tc.wantErr)
		}
		if strings.Join(got, ",") != strings.Join(tc.remote, ",") {
			t.Errorf("%s: remote = %v, want %v", name, got, tc.remote)
		}
	}
}
