package generator

import (
	"os/exec"
	"testing"
)

// TestAddStateMachine_GeneratedProjectBuildsAndTests generates a state-machine
// recipe and asserts it compiles and passes its generated scenario tests
// (valid + illegal transitions).
func TestAddStateMachine_GeneratedProjectBuildsAndTests(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	if err := AddStateMachine("order",
		"placed,paid,shipped,delivered,cancelled",
		"placed->paid,paid->shipped,shipped->delivered,placed->cancelled,paid->cancelled",
	); err != nil {
		t.Fatalf("AddStateMachine() error = %v", err)
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("generated state-machine project does not compile: %v\n%s", err, out)
	}

	test := exec.Command("go", "test", "./service/...")
	test.Dir = dir
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("generated state-machine scenario tests failed: %v\n%s", err, out)
	}
}

func TestBuildStateMachineData_RejectsBadTransitions(t *testing.T) {
	cases := []struct{ states, transitions string }{
		{"", ""},                          // no states
		{"placed,paid", "placed->nope"},   // unknown target
		{"placed,paid", "nope->paid"},     // unknown source
		{"placed,paid", "placed_paid"},    // malformed (no ->)
		{"placed,placed", ""},             // duplicate state
		{"Placed", ""},                    // not snake_case
	}
	for _, c := range cases {
		if _, err := buildStateMachineData("m", "order", c.states, c.transitions); err == nil {
			t.Errorf("buildStateMachineData(states=%q, transitions=%q) = nil error, want error", c.states, c.transitions)
		}
	}
}
