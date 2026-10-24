package physnet

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Dynamic DNS (RFC 2136, signed with TSIG, RFC 8945): when a machine gets an address from
// an IP pool, its name gets an A or AAAA record in the zone and the address a PTR record in
// a reverse zone; both go when the machine does. Works with BIND, Knot, PowerDNS and
// Windows DNS (with a TSIG key or unsigned updates allowed from the hosts).

// DNSConfig is where updates go and how they are signed.
type DNSConfig struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"` // host or host:port of the primary server
	Zone    string `json:"zone"`   // corp.example.
	// ReverseZones get the PTR records: 20.10.in-addr.arpa., …; none = no PTR records.
	ReverseZones []string `json:"reverseZones,omitempty"`
	KeyName      string   `json:"keyName,omitempty"`   // the TSIG key, e.g. virtomir.
	Algorithm    string   `json:"algorithm,omitempty"` // hmac-sha256 (default), hmac-sha512 or hmac-md5
	Secret       string   `json:"secret,omitempty"`    // base64, as in the server's key file
	TTL          uint32   `json:"ttl,omitempty"`
}

var algorithms = map[string]struct {
	wire string
	h    func() hash.Hash
}{
	"hmac-sha256": {"hmac-sha256.", sha256.New},
	"hmac-sha512": {"hmac-sha512.", sha512.New},
	"hmac-md5":    {"hmac-md5.sig-alg.reg.int.", md5.New},
}

func fqdn(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), ".")) + "."
}

// Normalize fills defaults (port 53, TTL 300, hmac-sha256) and checks the rest.
func (c *DNSConfig) Normalize() error {
	if !c.Enabled && c.Server == "" && c.Zone == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(c.Server)
	if err != nil {
		host, port = c.Server, "53"
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 || host == "" || !(domainRE.MatchString(host) || isIP(host)) {
		return invalidf("the DNS server is a host or IP, with an optional port (10.0.0.53:53)")
	}
	c.Server = net.JoinHostPort(host, port)
	if !domainRE.MatchString(strings.TrimSuffix(c.Zone, ".")) {
		return invalidf("%q is not a zone name", c.Zone)
	}
	c.Zone = fqdn(c.Zone)
	for i, z := range c.ReverseZones {
		if !strings.HasSuffix(fqdn(z), ".in-addr.arpa.") && !strings.HasSuffix(fqdn(z), ".ip6.arpa.") {
			return invalidf("%q is not a reverse zone (… .in-addr.arpa. or … .ip6.arpa.)", z)
		}
		c.ReverseZones[i] = fqdn(z)
	}
	if c.TTL == 0 {
		c.TTL = 300
	}
	if c.TTL < 30 || c.TTL > 86400 {
		return invalidf("the TTL is 30 to 86400 seconds")
	}
	if c.KeyName == "" {
		c.Secret, c.Algorithm = "", ""
		return nil
	}
	if !domainRE.MatchString(strings.TrimSuffix(c.KeyName, ".")) {
		return invalidf("%q is not a key name", c.KeyName)
	}
	c.KeyName = fqdn(c.KeyName)
	if c.Algorithm == "" {
		c.Algorithm = "hmac-sha256"
	}
	if _, ok := algorithms[c.Algorithm]; !ok {
		return invalidf("the algorithm is hmac-sha256, hmac-sha512 or hmac-md5")
	}
	if raw, err := base64.StdEncoding.DecodeString(c.Secret); err != nil || len(raw) == 0 {
		return invalidf("the key secret is the base64 string of the server's key file")
	}
	return nil
}

func isIP(s string) bool { _, err := netip.ParseAddr(s); return err == nil }

// DNS wire format values.
const (
	typeA    = 1
	typeSOA  = 6
	typePTR  = 12
	typeAAAA = 28
	typeTSIG = 250
	typeANY  = 255
	classIN  = 1
	classANY = 255
)

var rcodes = map[int]string{1: "FORMERR (malformed update)", 2: "SERVFAIL", 3: "NXDOMAIN", 4: "NOTIMP (no updates here)", 5: "REFUSED (updates not allowed from this host or key)",
	6: "YXDOMAIN", 7: "YXRRSET", 8: "NXRRSET", 9: "NOTAUTH (not authoritative, or the key is wrong)", 10: "NOTZONE (name outside the zone)"}

// packName is a domain name in wire format.
func packName(name string) ([]byte, error) {
	name = fqdn(name)
	if len(name) > 254 {
		return nil, invalidf("the name %s is too long", name)
	}
	var b []byte
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if l == "" || len(l) > 63 {
			return nil, invalidf("%q is not a valid DNS name", name)
		}
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0), nil
}

// rr is one record of the update section.
type rr struct {
	name  string
	typ   uint16
	class uint16
	ttl   uint32
	data  []byte
}

func (r rr) pack() ([]byte, error) {
	b, err := packName(r.name)
	if err != nil {
		return nil, err
	}
	b = binary.BigEndian.AppendUint16(b, r.typ)
	b = binary.BigEndian.AppendUint16(b, r.class)
	b = binary.BigEndian.AppendUint32(b, r.ttl)
	b = binary.BigEndian.AppendUint16(b, uint16(len(r.data)))
	return append(b, r.data...), nil
}

// deleteRRset removes every record of a type at name; addRR adds one.
func deleteRRset(name string, typ uint16) rr { return rr{name: name, typ: typ, class: classANY} }

func addressRR(name string, ip netip.Addr, ttl uint32) rr {
	if ip.Is4() {
		a := ip.As4()
		return rr{name: name, typ: typeA, class: classIN, ttl: ttl, data: a[:]}
	}
	a := ip.As16()
	return rr{name: name, typ: typeAAAA, class: classIN, ttl: ttl, data: a[:]}
}

// updateMessage builds an UPDATE for zone with the records, signed when the config has a
// key; now is the TSIG time.
func updateMessage(c DNSConfig, zone string, records []rr, id uint16, now time.Time) ([]byte, error) {
	msg := binary.BigEndian.AppendUint16(nil, id)
	msg = binary.BigEndian.AppendUint16(msg, 5<<11) // opcode UPDATE
	msg = binary.BigEndian.AppendUint16(msg, 1)     // zone count
	msg = binary.BigEndian.AppendUint16(msg, 0)     // prerequisites
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(records)))
	msg = binary.BigEndian.AppendUint16(msg, 0) // additional
	z, err := packName(zone)
	if err != nil {
		return nil, err
	}
	msg = append(msg, z...)
	msg = binary.BigEndian.AppendUint16(msg, typeSOA)
	msg = binary.BigEndian.AppendUint16(msg, classIN)
	for _, r := range records {
		p, err := r.pack()
		if err != nil {
			return nil, err
		}
		msg = append(msg, p...)
	}
	if c.KeyName == "" {
		return msg, nil
	}
	return signTSIG(msg, c, id, now)
}

// signTSIG appends a TSIG record (RFC 8945 §4.3.3: the MAC covers the message and the TSIG
// variables) and counts it in the additional section.
func signTSIG(msg []byte, c DNSConfig, id uint16, now time.Time) ([]byte, error) {
	alg := algorithms[c.Algorithm]
	secret, err := base64.StdEncoding.DecodeString(c.Secret)
	if err != nil {
		return nil, invalidf("the key secret is not base64")
	}
	key, _ := packName(c.KeyName)
	algName, _ := packName(alg.wire)
	signed := uint64(now.Unix())
	timeFudge := func(b []byte) []byte {
		b = append(b, byte(signed>>40), byte(signed>>32), byte(signed>>24), byte(signed>>16), byte(signed>>8), byte(signed))
		return binary.BigEndian.AppendUint16(b, 300) // fudge
	}
	vars := append(append([]byte{}, key...), 0, classANY, 0, 0, 0, 0)
	vars = append(vars, algName...)
	vars = timeFudge(vars)
	vars = append(vars, 0, 0, 0, 0) // error, other len
	mac := hmac.New(alg.h, secret)
	mac.Write(msg)
	mac.Write(vars)
	sum := mac.Sum(nil)

	rdata := append([]byte{}, algName...)
	rdata = timeFudge(rdata)
	rdata = binary.BigEndian.AppendUint16(rdata, uint16(len(sum)))
	rdata = append(rdata, sum...)
	rdata = binary.BigEndian.AppendUint16(rdata, id)
	rdata = append(rdata, 0, 0, 0, 0) // error, other len
	t, err := rr{name: c.KeyName, typ: typeTSIG, class: classANY, data: rdata}.pack()
	if err != nil {
		return nil, err
	}
	out := append(append([]byte{}, msg...), t...)
	binary.BigEndian.PutUint16(out[10:], binary.BigEndian.Uint16(out[10:])+1)
	return out, nil
}

// send sends one update and checks the answer's RCODE.
func send(ctx context.Context, c DNSConfig, zone string, records []rr) error {
	var idb [2]byte
	_, _ = rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	msg, err := updateMessage(c, zone, records, id, time.Now())
	if err != nil {
		return err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", c.Server)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(msg); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return fmt.Errorf("no answer from %s: %w", c.Server, err)
		}
		if n < 12 || binary.BigEndian.Uint16(buf) != id {
			continue // not ours
		}
		flags := binary.BigEndian.Uint16(buf[2:])
		if flags&0x8000 == 0 || (flags>>11)&0xf != 5 {
			return errors.New("the DNS server sent something that is not an update answer")
		}
		if rc := int(flags & 0xf); rc != 0 {
			why := rcodes[rc]
			if why == "" {
				why = strconv.Itoa(rc)
			}
			return fmt.Errorf("%s refused the update of %s: %s", c.Server, zone, why)
		}
		return nil
	}
}

// ReverseName is the PTR name of an address: 50.0.20.10.in-addr.arpa.
func ReverseName(ip netip.Addr) string {
	if ip.Is4() {
		a := ip.As4()
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa.", a[3], a[2], a[1], a[0])
	}
	a := ip.As16()
	var b strings.Builder
	for i := 15; i >= 0; i-- {
		fmt.Fprintf(&b, "%x.%x.", a[i]&0xf, a[i]>>4)
	}
	return b.String() + "ip6.arpa."
}

// reverseZone is the configured reverse zone that holds name ("" = none).
func (c DNSConfig) reverseZone(name string) string {
	best := ""
	for _, z := range c.ReverseZones {
		if strings.HasSuffix(name, "."+z) && len(z) > len(best) {
			best = z
		}
	}
	return best
}

var hostLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Name is a machine's name in the zone.
func (c DNSConfig) Name(host string) (string, error) {
	h := strings.ToLower(host)
	if !hostLabelRE.MatchString(h) {
		return "", invalidf("%q cannot be a DNS name: letters, digits and dashes", host)
	}
	return h + "." + c.Zone, nil
}

// Register points host's name at ips (replacing what it had) and each address back at
// the name.
func Register(ctx context.Context, c DNSConfig, host string, ips []netip.Addr) error {
	if !c.Enabled {
		return nil
	}
	name, err := c.Name(host)
	if err != nil {
		return err
	}
	records := []rr{deleteRRset(name, typeA), deleteRRset(name, typeAAAA)}
	for _, ip := range ips {
		records = append(records, addressRR(name, ip, c.TTL))
	}
	if err := send(ctx, c, c.Zone, records); err != nil {
		return err
	}
	return reverse(ctx, c, name, ips, true)
}

// Unregister removes host's addresses and the PTR records of ips.
func Unregister(ctx context.Context, c DNSConfig, host string, ips []netip.Addr) error {
	if !c.Enabled {
		return nil
	}
	name, err := c.Name(host)
	if err != nil {
		return err
	}
	if err := send(ctx, c, c.Zone, []rr{deleteRRset(name, typeA), deleteRRset(name, typeAAAA)}); err != nil {
		return err
	}
	return reverse(ctx, c, name, ips, false)
}

func reverse(ctx context.Context, c DNSConfig, name string, ips []netip.Addr, add bool) error {
	target, _ := packName(name)
	byZone := map[string][]rr{}
	for _, ip := range ips {
		ptr := ReverseName(ip)
		z := c.reverseZone(ptr)
		if z == "" {
			continue
		}
		byZone[z] = append(byZone[z], deleteRRset(ptr, typePTR))
		if add {
			byZone[z] = append(byZone[z], rr{name: ptr, typ: typePTR, class: classIN, ttl: c.TTL, data: target})
		}
	}
	zones := make([]string, 0, len(byZone))
	for z := range byZone {
		zones = append(zones, z)
	}
	slices.Sort(zones)
	for _, z := range zones {
		if err := send(ctx, c, z, byZone[z]); err != nil {
			return err
		}
	}
	return nil
}
