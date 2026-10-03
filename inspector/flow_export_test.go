package inspector_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ariefsam/esb/inspector"
	"gopkg.in/yaml.v3"
)

func TestFlowExport_YAMLAndJSONAgree(t *testing.T) {
	m := inspector.ProjectModel{}
	exp := inspector.BuildFlowExport(m, "/p", "")

	var j, y bytes.Buffer
	if err := inspector.WriteFlowExport(&j, exp, "json"); err != nil {
		t.Fatal(err)
	}
	if err := inspector.WriteFlowExport(&y, exp, "yaml"); err != nil {
		t.Fatal(err)
	}

	var fromJSON, fromYAML inspector.FlowExport
	if err := json.Unmarshal(j.Bytes(), &fromJSON); err != nil {
		t.Fatalf("json: %v", err)
	}
	if err := yaml.Unmarshal(y.Bytes(), &fromYAML); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	if fromJSON.Version != 1 || fromYAML.Version != 1 || fromJSON.Project != fromYAML.Project {
		t.Errorf("formats disagree: %+v vs %+v", fromJSON, fromYAML)
	}
	// Empty lists must be [] not null so consumers can iterate safely.
	if strings.Contains(j.String(), "null") {
		t.Errorf("json contains null: %s", j.String())
	}
}

func TestWriteFlowExport_UnknownFormat(t *testing.T) {
	if err := inspector.WriteFlowExport(&bytes.Buffer{}, inspector.FlowExport{}, "xml"); err == nil {
		t.Fatal("want error for unknown format")
	}
}
