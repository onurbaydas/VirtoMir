package physnet

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

func TestReverseNameAndPackName(t *testing.T) {
	if got := ReverseName(netip.MustParseAddr("10.20.0.50")); got != "50.0.20.10.in-addr.arpa." {
		t.Errorf("v4 %s", got)
	}
	got := ReverseName(netip.MustParseAddr("fd00:20::5"))
	if !strings.HasPrefix(got, "5.0.0.0.0.0.0.0.") || !strings.HasSuffix(got, "0.2.0.0.0.0.d.f.ip6.arpa.") || strings.Count(got, ".") != 34 {
		t.Errorf("v6 %s", got)
	}
	b, _ := packName("Web-01.Prod.Example")
	if !bytes.Equal(b, []byte("\x06web-01\x04prod\x07example\x00")) {
		t.Errorf("packed %q", b)
	}
	if _, err := packName("a..b"); err == nil {
		t.Error("empty label packed")
	}
}

func TestDNSConfigNormalize(t *testing.T) {
	c := DNSConfig{Enabled: true, Server: "10.0.0.53", Zone: "Prod.Example", ReverseZones: []string{"20.10.in-addr.arpa"}, KeyName: "virtomir",
		Secret: base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Server != "10.0.0.53:53" || c.Zone != "prod.example." || c.ReverseZones[0] != "20.10.in-addr.arpa." || c.KeyName != "virtomir." || c.Algorithm != "hmac-sha256" || c.TTL != 300 {
		t.Errorf("%+v", c)
	}
	for i, f := range []func(*DNSConfig){
		func(c *DNSConfig) { c.Server = "" },
		func(c *DNSConfig) { c.Zone = "bad zone" },
		func(c *DNSConfig) { c.ReverseZones = []string{"example.com"} },
		func(c *DNSConfig) { c.Algorithm = "hmac-sha1" },
		func(c *DNSConfig) { c.Secret = "not base64!" },
		func(c *DNSConfig) { c.TTL = 5 },
	} {
		x := c
		x.ReverseZones = append([]string{}, c.ReverseZones...)
		f(&x)
		if err := x.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: %v", i, err)
		}
	}
}

// fakeDNS answers RFC 2136 updates: it checks the TSIG MAC itself (key "virtomir.",
// hmac-sha256) and keeps the records.
type fakeDNS struct {
	addr   string
	secret []byte
	mu     sync.Mutex
	recs   map[string]string // "name TYPE" → data
	zones  []string
}

func startDNS(t *testing.T, secret []byte) *fakeDNS {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	f := &fakeDNS{addr: conn.LocalAddr().String(), secret: secret, recs: map[string]string{}}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			msg := append([]byte{}, buf[:n]...)
			rcode := f.handle(msg)
			ans := append([]byte{}, msg[:12]...)
			binary.BigEndian.PutUint16(ans[2:], 0x8000|5<<11|uint16(rcode))
			for i := 4; i < 12; i++ {
				ans[i] = 0
			}
			_, _ = conn.WriteTo(ans, from)
		}
	}()
	return f
}

func readName(b []byte, off int) (string, int) {
	var labels []string
	for b[off] != 0 {
		l := int(b[off])
		labels = append(labels, string(b[off+1:off+1+l]))
		off += 1 + l
	}
	return strings.Join(labels, ".") + ".", off + 1
}

func (f *fakeDNS) handle(msg []byte) int {
	up := int(binary.BigEndian.Uint16(msg[8:]))
	ad := int(binary.BigEndian.Uint16(msg[10:]))
	zone, off := readName(msg, 12)
	off += 4
	type rec struct {
		name        string
		typ, class  uint16
		data        []byte
		start, stop int
	}
	var recs []rec
	for i := 0; i < up+ad; i++ {
		start := off
		name, o := readName(msg, off)
		typ, class := binary.BigEndian.Uint16(msg[o:]), binary.BigEndian.Uint16(msg[o+2:])
		l := int(binary.BigEndian.Uint16(msg[o+8:]))
		recs = append(recs, rec{name, typ, class, msg[o+10 : o+10+l], start, o + 10 + l})
		off = o + 10 + l
	}
	if f.secret != nil {
		if ad != 1 || recs[len(recs)-1].typ != typeTSIG {
			return 9
		}
		t := recs[len(recs)-1]
		alg, o := readName(t.data, 0)
		if alg != "hmac-sha256." {
			return 9
		}
		timeFudge := t.data[o : o+8]
		macLen := int(binary.BigEndian.Uint16(t.data[o+8:]))
		mac := t.data[o+10 : o+10+macLen]
		unsigned := append([]byte{}, msg[:t.start]...)
		binary.BigEndian.PutUint16(unsigned[10:], 0)
		key, _ := packName("virtomir.")
		algName, _ := packName("hmac-sha256.")
		vars := append(append(append([]byte{}, key...), 0, 255, 0, 0, 0, 0), algName...)
		vars = append(append(vars, timeFudge...), 0, 0, 0, 0)
		h := hmac.New(sha256.New, f.secret)
		h.Write(unsigned)
		h.Write(vars)
		if !hmac.Equal(h.Sum(nil), mac) {
			return 9
		}
		recs = recs[:len(recs)-1]
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zones = append(f.zones, zone)
	for _, r := range recs {
		key := r.name + " " + map[uint16]string{typeA: "A", typeAAAA: "AAAA", typePTR: "PTR"}[r.typ]
		switch r.class {
		case classANY:
			delete(f.recs, key)
		case classIN:
			switch r.typ {
			case typeA, typeAAAA:
				ip, _ := netip.AddrFromSlice(r.data)
				f.recs[key] = ip.String()
			case typePTR:
				n, _ := readName(r.data, 0)
				f.recs[key] = n
			}
		}
	}
	return 0
}

func TestDynamicDNS(t *testing.T) {
	secret := []byte("a shared secret for the tests!!")
	srv := startDNS(t, secret)
	c := DNSConfig{Enabled: true, Server: srv.addr, Zone: "prod.example", ReverseZones: []string{"20.10.in-addr.arpa"}, KeyName: "virtomir",
		Secret: base64.StdEncoding.EncodeToString(secret)}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ips := []netip.Addr{netip.MustParseAddr("10.20.0.50"), netip.MustParseAddr("fd00:20::50")}
	if err := Register(ctx, c, "Web-01", ips); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	got := map[string]string{}
	for k, v := range srv.recs {
		got[k] = v
	}
	zones := strings.Join(srv.zones, ",")
	srv.mu.Unlock()
	if got["web-01.prod.example. A"] != "10.20.0.50" || got["web-01.prod.example. AAAA"] != "fd00:20::50" || got["50.0.20.10.in-addr.arpa. PTR"] != "web-01.prod.example." {
		t.Errorf("records %v", got)
	}
	if zones != "prod.example.,20.10.in-addr.arpa." {
		t.Errorf("zones %s (no reverse zone for IPv6: no PTR)", zones)
	}
	if err := Unregister(ctx, c, "web-01", ips); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	n := len(srv.recs)
	srv.mu.Unlock()
	if n != 0 {
		t.Errorf("left %v", srv.recs)
	}

	wrong := c
	wrong.Secret = base64.StdEncoding.EncodeToString([]byte("another secret"))
	if err := Register(ctx, wrong, "web-02", ips[:1]); err == nil || !strings.Contains(err.Error(), "NOTAUTH") {
		t.Errorf("wrong key: %v", err)
	}
	if err := Register(ctx, DNSConfig{}, "web-03", ips); err != nil {
		t.Errorf("disabled must do nothing: %v", err)
	}
	if err := Register(ctx, c, "web_01", ips); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad host: %v", err)
	}
}
