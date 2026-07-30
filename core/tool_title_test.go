package core

import "testing"

func TestAppendToolStep_MergesConsecutiveSameName(t *testing.T) {
	e := &Engine{toolTitleTransient: true}
	var steps []ToolStep
	steps = e.appendToolStep(steps, "Bash", "ls")
	steps = e.appendToolStep(steps, "Bash", "pwd")
	steps = e.appendToolStep(steps, "Read", "a.go")

	if len(steps) != 2 {
		t.Fatalf("expected 2 rows (Bash merged, Read), got %d: %+v", len(steps), steps)
	}
	if steps[0].Name != "Bash" || steps[0].Count != 2 {
		t.Errorf("row0 = {Name:%q Count:%d}, want {Bash 2}", steps[0].Name, steps[0].Count)
	}
	if steps[0].Summary != "pwd" {
		t.Errorf("merged row keeps latest summary; got %q want %q", steps[0].Summary, "pwd")
	}
	if steps[1].Name != "Read" || steps[1].Count != 1 {
		t.Errorf("row1 = {Name:%q Count:%d}, want {Read 1}", steps[1].Name, steps[1].Count)
	}
}

func TestAppendToolStep_NoMergeWhenDisabled(t *testing.T) {
	e := &Engine{toolTitleTransient: false}
	var steps []ToolStep
	steps = e.appendToolStep(steps, "Bash", "ls")
	steps = e.appendToolStep(steps, "Bash", "pwd")
	if len(steps) != 2 {
		t.Fatalf("expected 2 separate rows when transient disabled, got %d", len(steps))
	}
}

func TestFinalToolSteps(t *testing.T) {
	steps := []ToolStep{{Kind: ToolStepKindTool, Name: "Bash", Count: 3}}

	on := &Engine{toolTitleTransient: true}
	if got := on.finalToolSteps(steps); got != nil {
		t.Errorf("transient on: final steps = %+v, want nil (no trace)", got)
	}

	off := &Engine{toolTitleTransient: false}
	if got := off.finalToolSteps(steps); len(got) != 1 {
		t.Errorf("transient off: final steps len = %d, want 1 (preserved)", len(got))
	}
}
