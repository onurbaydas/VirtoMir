// Package config holds the operator's manifests; the test checks that they parse.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestManifestsParse(t *testing.T) {
	files, _ := filepath.Glob("*/*.yaml")
	if len(files) < 3 {
		t.Fatalf("found only %v", files)
	}
	kinds := map[string]int{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, doc := range strings.Split(string(raw), "\n---\n") {
			var obj map[string]any
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
				t.Errorf("%s document %d: %v", f, i+1, err)
				continue
			}
			kind, _ := obj["kind"].(string)
			kinds[kind]++
		}
	}
	if kinds["CustomResourceDefinition"] != 2 || kinds["VirtoMirMachine"] < 1 || kinds["Deployment"] != 1 {
		t.Errorf("kinds %v", kinds)
	}
}
