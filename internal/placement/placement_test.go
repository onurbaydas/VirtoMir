package placement

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTripAndLegacyImport(t *testing.T) {
	tmpDir := t.TempDir()
	drsPath := filepath.Join(tmpDir, "drs.json")
	placementPath := filepath.Join(tmpDir, "placement.json")

	// 1. Seed legacy drs.json
	type legacyRule struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		Kind       string   `json:"kind"`
		Enabled    bool     `json:"enabled"`
		Mandatory  bool     `json:"mandatory"`
		Domains    []string `json:"domains"`
		TargetHost string   `json:"targetHost,omitempty"`
	}
	legacyConfig := struct {
		Enabled bool         `json:"enabled"`
		Rules   []legacyRule `json:"rules"`
	}{
		Enabled: true,
		Rules: []legacyRule{
			{
				ID:        "1111222233334444",
				Name:      "Legacy anti-affinity",
				Kind:      "vm_anti_affinity",
				Enabled:   true,
				Mandatory: true,
				Domains:   []string{"vm-1", "vm-2"},
			},
			{
				ID:         "5555666677778888",
				Name:       "Legacy host affinity",
				Kind:       "vm_host_affinity",
				Enabled:    true,
				Mandatory:  false,
				Domains:    []string{"vm-3"},
				TargetHost: "host-1",
			},
		},
	}
	legacyBytes, err := json.Marshal(legacyConfig)
	if err != nil {
		t.Fatalf("marshal legacy config: %v", err)
	}
	if err := os.WriteFile(drsPath, legacyBytes, 0o600); err != nil {
		t.Fatalf("write legacy drs.json: %v", err)
	}

	// 2. Open store with absent placement.json -> imports drs.json once
	s, err := Open(placementPath)
	if err != nil {
		t.Fatalf("Open placement store: %v", err)
	}

	rules := s.Rules()
	if len(rules) != 2 {
		t.Fatalf("expected 2 imported rules, got %d", len(rules))
	}

	r1 := rules[0]
	if r1.ID != "1111222233334444" || r1.Kind != VMAntiAffinity || r1.Strength != Must || !r1.Enabled {
		t.Errorf("unexpected rule 1: %+v", r1)
	}

	r2 := rules[1]
	if r2.ID != "5555666677778888" || r2.Kind != VMHost || r2.Strength != Should || len(r2.Hosts) != 1 || r2.Hosts[0] != "host-1" {
		t.Errorf("unexpected rule 2: %+v", r2)
	}

	// Verify placement.json was written to disk
	if _, err := os.Stat(placementPath); err != nil {
		t.Fatalf("expected placement.json to exist on disk: %v", err)
	}

	// 3. Mutate drs.json on disk to ensure it is ignored afterwards
	if err := os.WriteFile(drsPath, []byte(`{"rules": []}`), 0o600); err != nil {
		t.Fatalf("overwrite drs.json: %v", err)
	}

	s2, err := Open(placementPath)
	if err != nil {
		t.Fatalf("re-open placement store: %v", err)
	}
	if len(s2.Rules()) != 2 {
		t.Fatalf("expected drs.json to be ignored after first import, got %d rules", len(s2.Rules()))
	}

	// 4. Test AddRule with ID-replace semantics
	added, err := s2.AddRule(Rule{
		Name:     "New Affinity Rule",
		Kind:     VMAffinity,
		Enabled:  true,
		Strength: Should,
		Domains:  []string{"vm-4", "vm-5"},
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if added.ID == "" || len(added.ID) != 16 {
		t.Errorf("expected 16-hex ID, got %q", added.ID)
	}
	if !added.Enabled {
		t.Errorf("expected added rule to be enabled")
	}
	if len(s2.Rules()) != 3 {
		t.Fatalf("expected 3 rules after add, got %d", len(s2.Rules()))
	}

	// Replace rule with same ID
	added.Name = "Updated Affinity Rule"
	replaced, err := s2.AddRule(added)
	if err != nil {
		t.Fatalf("AddRule (replace): %v", err)
	}
	if replaced.Name != "Updated Affinity Rule" {
		t.Errorf("expected replaced name, got %q", replaced.Name)
	}
	if len(s2.Rules()) != 3 {
		t.Fatalf("expected 3 rules after replace, got %d", len(s2.Rules()))
	}

	// 5. Test DeleteRule
	if err := s2.DeleteRule(added.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if len(s2.Rules()) != 2 {
		t.Fatalf("expected 2 rules after delete, got %d", len(s2.Rules()))
	}

	// 6. Test SetLabels and Labels
	labelsH1 := map[string]string{"rack": "r1", "zone": "us-east-1"}
	if err := s2.SetLabels("host-1", labelsH1); err != nil {
		t.Fatalf("SetLabels host-1: %v", err)
	}
	labelsH2 := map[string]string{"rack": "r2"}
	if err := s2.SetLabels("host-2", labelsH2); err != nil {
		t.Fatalf("SetLabels host-2: %v", err)
	}

	allLabels := s2.Labels()
	if len(allLabels) != 2 || allLabels["host-1"]["rack"] != "r1" || allLabels["host-2"]["rack"] != "r2" {
		t.Fatalf("unexpected Labels result: %+v", allLabels)
	}

	// 7. Verify persistence across store re-open
	s3, err := Open(placementPath)
	if err != nil {
		t.Fatalf("Open re-check: %v", err)
	}
	if len(s3.Rules()) != 2 {
		t.Errorf("expected 2 persisted rules, got %d", len(s3.Rules()))
	}
	persistedLabels := s3.Labels()
	if persistedLabels["host-1"]["zone"] != "us-east-1" {
		t.Errorf("expected persisted host-1 zone us-east-1, got %q", persistedLabels["host-1"]["zone"])
	}

	// View snapshot test
	view := s3.View()
	if len(view.Rules) != 2 || len(view.Labels) != 2 {
		t.Errorf("View() mismatch: %+v", view)
	}
}

func TestValidationErrorsAreClassed(t *testing.T) {
	s, err := Open("") // in-memory store
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "empty name",
			call: func() error {
				_, err := s.AddRule(Rule{Name: "", Kind: VMAffinity, Domains: []string{"a"}})
				return err
			},
		},
		{
			name: "whitespace name",
			call: func() error {
				_, err := s.AddRule(Rule{Name: "   ", Kind: VMAffinity, Domains: []string{"a"}})
				return err
			},
		},
		{
			name: "unknown kind",
			call: func() error {
				_, err := s.AddRule(Rule{Name: "r", Kind: "invalid_kind", Domains: []string{"a"}})
				return err
			},
		},
		{
			name: "empty domains",
			call: func() error {
				_, err := s.AddRule(Rule{Name: "r", Kind: VMAffinity, Domains: []string{}})
				return err
			},
		},
		{
			name: "anti-affinity under 2 domains",
			call: func() error {
				_, err := s.AddRule(Rule{Name: "r", Kind: VMAntiAffinity, Domains: []string{"a"}})
				return err
			},
		},
		{
			name: "vm_host without hosts",
			call: func() error {
				_, err := s.AddRule(Rule{Name: "r", Kind: VMHost, Domains: []string{"a"}, Hosts: []string{}})
				return err
			},
		},
		{
			name: "invalid strength",
			call: func() error {
				_, err := s.AddRule(Rule{Name: "r", Kind: VMAffinity, Domains: []string{"a"}, Strength: "supermust"})
				return err
			},
		},
		{
			name: "invalid rule ID format",
			call: func() error {
				_, err := s.AddRule(Rule{ID: "invalid-id", Name: "r", Kind: VMAffinity, Domains: []string{"a"}})
				return err
			},
		},
		{
			name: "DeleteRule invalid ID format",
			call: func() error {
				return s.DeleteRule("not-16-hex")
			},
		},
		{
			name: "SetLabels empty hostID",
			call: func() error {
				return s.SetLabels("", map[string]string{"rack": "r1"})
			},
		},
		{
			name: "SetLabels invalid key regex",
			call: func() error {
				return s.SetLabels("host-1", map[string]string{"INVALID_KEY!": "val"})
			},
		},
		{
			name: "SetLabels invalid value regex",
			call: func() error {
				return s.SetLabels("host-1", map[string]string{"rack": "val with spaces"})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected ErrInvalid (422), got: %v", err)
			}
		})
	}

	// Test DeleteRule 404 (ErrNotFound)
	err = s.DeleteRule("0123456789abcdef")
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound (404), got: %v", err)
	}
}

func TestAddRuleRefusesConflictingRule(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Add Must Affinity rule
	_, err = s.AddRule(Rule{
		Name:     "Affinity Pair",
		Kind:     VMAffinity,
		Enabled:  true,
		Strength: Must,
		Domains:  []string{"vm-1", "vm-2"},
	})
	if err != nil {
		t.Fatalf("AddRule affinity: %v", err)
	}

	// Attempting to add contradictory Must AntiAffinity rule sharing both domains must be rejected (422)
	_, err = s.AddRule(Rule{
		Name:     "AntiAffinity Pair",
		Kind:     VMAntiAffinity,
		Enabled:  true,
		Strength: Must,
		Domains:  []string{"vm-1", "vm-2"},
	})
	if err == nil {
		t.Fatalf("expected AddRule to reject conflicting anti-affinity rule")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid (422), got: %v", err)
	}
}

func TestLegacyImportSanitizesInvalidID(t *testing.T) {
	tmpDir := t.TempDir()
	drsPath := filepath.Join(tmpDir, "drs.json")
	placementPath := filepath.Join(tmpDir, "placement.json")

	// Legacy rule with invalid (non-16-hex) ID
	legacyConfig := struct {
		Rules []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Kind      string   `json:"kind"`
			Enabled   bool     `json:"enabled"`
			Mandatory bool     `json:"mandatory"`
			Domains   []string `json:"domains"`
		} `json:"rules"`
	}{
		Rules: []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Kind      string   `json:"kind"`
			Enabled   bool     `json:"enabled"`
			Mandatory bool     `json:"mandatory"`
			Domains   []string `json:"domains"`
		}{
			{
				ID:        "legacy-bad-uuid-format",
				Name:      "Legacy bad ID rule",
				Kind:      "vm_anti_affinity",
				Enabled:   true,
				Mandatory: true,
				Domains:   []string{"vm-1", "vm-2"},
			},
		},
	}
	data, err := json.Marshal(legacyConfig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(drsPath, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s, err := Open(placementPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	rules := s.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 imported rule, got %d", len(rules))
	}
	r := rules[0]
	if r.ID == "legacy-bad-uuid-format" {
		t.Errorf("expected legacy invalid ID to be replaced, got %q", r.ID)
	}
	if !ruleIDRE.MatchString(r.ID) {
		t.Errorf("expected valid 16-hex ID, got %q", r.ID)
	}

	// Verify that the sanitized rule can be deleted without ErrInvalid
	if err := s.DeleteRule(r.ID); err != nil {
		t.Fatalf("expected DeleteRule to succeed on sanitized ID, got: %v", err)
	}
	if len(s.Rules()) != 0 {
		t.Errorf("expected 0 rules after delete, got %d", len(s.Rules()))
	}
}

func TestAddRuleDisabledRule(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Adding a rule with Enabled=false must NOT be silently forced to Enabled=true
	rule, err := s.AddRule(Rule{
		Name:     "Disabled Affinity Rule",
		Kind:     VMAffinity,
		Enabled:  false,
		Strength: Should,
		Domains:  []string{"vm-1", "vm-2"},
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if rule.Enabled {
		t.Errorf("expected added rule to remain disabled (Enabled=false), got true")
	}

	rules := s.Rules()
	if len(rules) != 1 || rules[0].Enabled {
		t.Errorf("expected persisted rule to be disabled: %+v", rules)
	}
}

func TestAddRuleDoesNotPigeonholeFromLabelsCount(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Set labels on only 2 hosts in a cluster
	if err := s.SetLabels("host-1", map[string]string{"rack": "r1"}); err != nil {
		t.Fatalf("SetLabels host-1: %v", err)
	}
	if err := s.SetLabels("host-2", map[string]string{"rack": "r2"}); err != nil {
		t.Fatalf("SetLabels host-2: %v", err)
	}

	if len(s.Labels()) != 2 {
		t.Fatalf("expected 2 labeled hosts, got %d", len(s.Labels()))
	}

	// Adding a 3-VM anti-affinity rule must NOT be falsely rejected with "exceeds host count (2)"
	rule, err := s.AddRule(Rule{
		Name:     "Separate 3 VMs",
		Kind:     VMAntiAffinity,
		Enabled:  true,
		Strength: Must,
		Domains:  []string{"vm-1", "vm-2", "vm-3"},
	})
	if err != nil {
		t.Fatalf("AddRule falsely rejected 3-VM anti-affinity rule based on len(labels)=2: %v", err)
	}
	if rule.ID == "" {
		t.Errorf("expected generated rule ID")
	}
}

// #37: a member that was down when the rules changed catches up from the others.
func TestSharedVersionAdopt(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(filepath.Join(dir, "a.json"))
	b, _ := Open(filepath.Join(dir, "b.json"))
	if _, err := a.AddRule(Rule{Name: "apart", Kind: VMAntiAffinity, Domains: []string{"web1", "web2"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetLabels("h1", map[string]string{"rack": "r1"}); err != nil {
		t.Fatal(err)
	}
	sh := a.Shared()
	if sh.Version != 2 {
		t.Fatalf("version after two changes = %d", sh.Version)
	}
	if ok, err := b.Adopt(sh); !ok || err != nil {
		t.Fatalf("adopt newer: %v %v", ok, err)
	}
	if ok, _ := b.Adopt(sh); ok {
		t.Error("the same version adopted twice")
	}
	reopened, _ := Open(filepath.Join(dir, "b.json"))
	if got := reopened.Shared(); got.Version != 2 || len(got.Rules) != 1 || got.Labels["h1"]["rack"] != "r1" {
		t.Errorf("adopted state not saved: %+v", got)
	}
	if ok, _ := a.Adopt(Shared{Version: 1}); ok {
		t.Error("an older version replaced newer rules")
	}
}
