package cluster

import "testing"

// A join code is pasted by a person: whatever it holds, parsing never panics, and a code
// that parses is printed back to one that parses to the same thing.
func FuzzParseCode(f *testing.F) {
	f.Add(Code{Address: "https://10.0.0.1:8420", Fingerprint: "ab", Secret: "cd"}.String())
	f.Add("vmj1-")
	f.Add("vmj1-e30")
	f.Add("  not a code ")
	f.Fuzz(func(t *testing.T, s string) {
		c, err := ParseCode(s)
		if err != nil {
			return
		}
		again, err := ParseCode(c.String())
		if err != nil || again != c {
			t.Fatalf("round trip of %q: %+v, %v", s, again, err)
		}
	})
}
