package cluster

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T, name string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cluster.json"), name, "fp-"+name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIdentityPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.json")
	a, err := Open(path, "a", "fp")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(path, "a", "fp")
	if err != nil {
		t.Fatal(err)
	}
	if a.SelfID() != b.SelfID() || a.Key() != b.Key() || a.Clustered() {
		t.Errorf("identity not kept or clustered from the start")
	}
}

func TestCodeRoundTrip(t *testing.T) {
	c := Code{Address: "https://10.0.0.1:8420", Fingerprint: "ab", Secret: "s"}
	got, err := ParseCode("  " + c.String() + "\n")
	if err != nil || got != c {
		t.Fatalf("ParseCode = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "hello", "vmj1-!!!", codePrefix + "e30"} {
		if _, err := ParseCode(bad); err == nil {
			t.Errorf("ParseCode(%q) accepted", bad)
		}
	}
}

func TestAdmitIsOneTimeAndExpires(t *testing.T) {
	a, b := open(t, "a"), open(t, "b")
	secret := a.Invite()
	members, v, err := a.Admit(secret, b.Self("https://b:8420"), "https://a:8420")
	if err != nil || len(members) != 2 || v != 1 {
		t.Fatalf("Admit = %v, %d, %v", members, v, err)
	}
	if members[0].Address != "https://a:8420" || !a.Clustered() {
		t.Errorf("inviter address/state wrong: %+v", members[0])
	}
	if _, _, err := a.Admit(secret, open(t, "c").Self("https://c:8420"), ""); err == nil {
		t.Error("secret used twice")
	}
	secret = a.Invite()
	a.now = func() time.Time { return time.Now().Add(InviteTTL + time.Minute) }
	if _, _, err := a.Admit(secret, open(t, "d").Self("https://d:8420"), ""); err == nil {
		t.Error("expired secret accepted")
	}
}

func TestAdmitRefusesDuplicateName(t *testing.T) {
	a := open(t, "a")
	if _, _, err := a.Admit(a.Invite(), open(t, "b").Self("https://b:1"), "https://a:1"); err != nil {
		t.Fatal(err)
	}
	_, _, err := a.Admit(a.Invite(), open(t, "b").Self("https://b2:1"), "")
	if err == nil || !strings.Contains(err.Error(), "already a member") {
		t.Errorf("second host named b: %v", err)
	}
}

func TestVerifyPeer(t *testing.T) {
	a, b := open(t, "a"), open(t, "b")
	if _, _, err := a.Admit(a.Invite(), b.Self("https://b:1"), "https://a:1"); err != nil {
		t.Fatal(err)
	}
	if n, ok := a.VerifyPeer(b.SelfID() + "." + b.Key()); !ok || n.Name != "b" {
		t.Errorf("valid peer refused")
	}
	for _, bad := range []string{b.SelfID() + ".wrong", b.SelfID(), "nobody." + b.Key(), a.SelfID() + "." + a.Key()} {
		if _, ok := a.VerifyPeer(bad); ok {
			t.Errorf("VerifyPeer(%q) accepted", bad)
		}
	}
}

func TestReplaceAndRemove(t *testing.T) {
	a, b, c := open(t, "a"), open(t, "b"), open(t, "c")
	a.Admit(a.Invite(), b.Self("https://b:1"), "https://a:1")
	members, v, _ := a.Admit(a.Invite(), c.Self("https://c:1"), "")
	if err := b.Replace(members, v); err != nil || !b.Clustered() {
		t.Fatalf("b did not take the list: %v", err)
	}
	if err := b.Replace(members[:2], v-1); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Members(); len(got) != 3 {
		t.Errorf("older list replaced newer one: %d members", len(got))
	}
	// a removes c: b gets the list without c, c gets the same list and is standalone.
	left, v, err := a.Remove(c.SelfID())
	if err != nil || len(left) != 2 {
		t.Fatalf("Remove = %v, %v", left, err)
	}
	b.Replace(left, v)
	c.Replace(left, v)
	if got, _ := b.Members(); len(got) != 2 || c.Clustered() {
		t.Errorf("after removal b has %d members, c clustered=%v", len(got), c.Clustered())
	}
	// Removing the last peer makes both standalone.
	left, v, _ = a.Remove(b.SelfID())
	if a.Clustered() || len(left) != 0 {
		t.Errorf("a should be standalone")
	}
	b.Replace([]Node{a.Self("https://a:1")}, v)
	if b.Clustered() {
		t.Errorf("b should be standalone")
	}
}
