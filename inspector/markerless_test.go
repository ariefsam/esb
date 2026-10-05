package inspector_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ariefsam/esb/generator"
	"github.com/ariefsam/esb/inspector"
)

// TestScan_SameWiringWithoutMarkers runs one add flow twice, once as
// generated and once with every `// esb:inject:` marker deleted before each
// step, and requires the scanner to report the same wiring for both: the
// generator injects into the markers' constructs when they are gone, so the
// scanner must read those constructs too, without picking up what the
// template itself declares there. Order may differ: with a marker new
// entries go right under it (newest first), without one they go last, and
// the scanner reports file order either way.
func TestScan_SameWiringWithoutMarkers(t *testing.T) {
	withMarkers := scanAfterAddFlow(t, false)
	without := scanAfterAddFlow(t, true)

	if len(withMarkers.Migrate) == 0 || len(withMarkers.RunWorker) == 0 ||
		len(withMarkers.Wire.Fields) == 0 || len(withMarkers.Wire.Nodes) == 0 {
		t.Fatalf("baseline scan is missing wiring: %+v", withMarkers)
	}
	for _, c := range []struct {
		name      string
		want, got any
	}{
		{"Migrate", sorted(withMarkers.Migrate), sorted(without.Migrate)},
		{"RunWorker", sorted(withMarkers.RunWorker), sorted(without.RunWorker)},
		{"Wire.Fields", withMarkers.Wire.Fields, without.Wire.Fields},
		{"Wire.Nodes", withMarkers.Wire.Nodes, without.Wire.Nodes},
	} {
		if !reflect.DeepEqual(c.want, c.got) {
			t.Errorf("%s without markers = %+v, want %+v", c.name, c.got, c.want)
		}
	}
}

func sorted(xs []string) []string {
	return slices.Sorted(slices.Values(xs))
}

func scanAfterAddFlow(t *testing.T, stripMarkers bool) inspector.ProjectModel {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := generator.InitProject("example.com/shop", dir); err != nil {
		t.Fatal(err)
	}
	steps := []func() error{
		func() error { return generator.AddAggregate("order") },
		func() error { return generator.AddAggregate("product") },
		func() error { return generator.AddHandler("place_order", "order") },
		func() error { return generator.AddProjection("sales_report", []string{"order", "product"}) },
		func() error { return generator.AddLedger("account") },
	}
	for _, step := range steps {
		if stripMarkers {
			removeMarkers(t, dir)
		}
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if stripMarkers {
		removeMarkers(t, dir)
	}
	m, err := inspector.Scan(dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	return m
}

func removeMarkers(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var kept []string
		for _, line := range strings.Split(string(src), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "// esb:inject:") {
				kept = append(kept, line)
			}
		}
		return os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
