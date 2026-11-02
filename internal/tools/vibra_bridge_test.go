package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVibraRecipe_WithVibeEngine(t *testing.T) {
	vibraBin := findVibraBinary()
	if vibraBin == "" {
		t.Skip("vibra.exe not installed, skipping installed test")
	}

	dir := t.TempDir()
	oldRoot := workspaceRoot
	workspaceRoot = dir
	defer func() { workspaceRoot = oldRoot }()

	recipePath := filepath.Join(dir, "test_recipe.json")
	recipeContent := `{"workflow_id": "test_wf", "tasks": [{"id": "t1", "trigger": "manual", "action_type": "notify", "target": "stdout", "payload": {"message": "hello from bridge"}}]}`
	if err := os.WriteFile(recipePath, []byte(recipeContent), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	res := r.Dispatch(ToolCall{
		Name: "run_vibra_recipe",
		Arguments: map[string]any{
			"path": recipePath,
		},
	})

	if !res.Success {
		t.Fatalf("expected run_vibra_recipe to succeed with Vibe Engine installed, got error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "test_wf") {
		t.Errorf("expected output to contain workflow id, got: %s", res.Output)
	}
}

func TestRunVibraRecipe_WithoutVibeEngine(t *testing.T) {
	// Temporarily override PATH and LOCALAPPDATA to simulate Vibe Engine missing
	origPath := os.Getenv("PATH")
	origLocalAppData := os.Getenv("LOCALAPPDATA")
	defer func() {
		os.Setenv("PATH", origPath)
		os.Setenv("LOCALAPPDATA", origLocalAppData)
	}()

	os.Setenv("PATH", "")
	os.Setenv("LOCALAPPDATA", t.TempDir())

	r := NewRegistry()
	res := r.Dispatch(ToolCall{
		Name: "run_vibra_recipe",
		Arguments: map[string]any{
			"path": "dummy.json",
		},
	})

	if res.Success {
		t.Fatal("expected run_vibra_recipe to fail when Vibe Engine is missing")
	}

	want := "Running recipes needs Vibe Engine (adeptuscamini.com). Terminus itself parses documents on its own."
	if res.Error != want {
		t.Fatalf("got error %q, want %q", res.Error, want)
	}
}
