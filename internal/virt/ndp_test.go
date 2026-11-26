package virt

import (
	"reflect"
	"testing"
)

func TestParseNeighbors6(t *testing.T) {
	out := `2001:db8::5 dev vnet0 lladdr 52:54:00:AA:BB:CC REACHABLE
fe80::5054:ff:feaa:bbcc dev vnet0 lladdr 52:54:00:aa:bb:cc STALE
fd00:1::20 dev virbr1 lladdr 52:54:00:aa:bb:cc DELAY
2001:db8::5 dev br0 lladdr 52:54:00:aa:bb:cc STALE
2001:db8::9 dev br0 lladdr 52:54:00:11:22:33 router REACHABLE
2001:db8::66 dev br0  FAILED
2001:db8::67 dev br0 lladdr 52:54:00:44:55:66 INCOMPLETE
ff02::1 dev br0 lladdr 33:33:00:00:00:01 NOARP
::ffff:10.0.0.1 dev br0 lladdr 52:54:00:77:88:99 REACHABLE
not-an-address dev br0 lladdr 52:54:00:77:88:99 REACHABLE

`
	want := map[string][]string{
		"52:54:00:aa:bb:cc": {"2001:db8::5", "fd00:1::20"},
		"52:54:00:11:22:33": {"2001:db8::9"},
	}
	if got := parseNeighbors6(out); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestMergeNeighborsKeepsLeasesAndPrefixes(t *testing.T) {
	leases := map[string][]string{"52:54:00:aa:bb:cc": {"192.168.122.10/24", "2001:db8::5/64"}}
	nb := map[string][]string{
		"52:54:00:aa:bb:cc": {"2001:db8::5", "2001:db8::77"},
		"52:54:00:11:22:33": {"fd00::1"},
	}
	want := map[string][]string{
		"52:54:00:aa:bb:cc": {"192.168.122.10/24", "2001:db8::5/64", "2001:db8::77"},
		"52:54:00:11:22:33": {"fd00::1"},
	}
	if got := mergeNeighbors(leases, nb); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if got := mergeNeighbors(map[string][]string{}, nil); len(got) != 0 {
		t.Errorf("nothing to merge gave %v", got)
	}
}
