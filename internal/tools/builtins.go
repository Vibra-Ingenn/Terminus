package tools

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// workspaceRoot is the directory AI tools may read/write. Resolved once at startup
// from TERMINUS_WORKSPACE (or legacy JANUS_WORKSPACE).
var workspaceRoot string

func init() {
	ws := os.Getenv("TERMINUS_WORKSPACE")
	if ws == "" {
		ws = os.Getenv("JANUS_WORKSPACE") // legacy name; prefer TERMINUS_WORKSPACE
	}
	if ws != "" {
		abs, err := filepath.Abs(ws)
		if err == nil {
			workspaceRoot = abs
			return
		}
	}
	cwd, _ := os.Getwd()
	workspaceRoot = filepath.Join(cwd, "workspace")
}

// safePath ensures p is inside workspaceRoot. Returns an error for traversal attempts.
func safePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	target := p
	if !filepath.IsAbs(p) {
		target = filepath.Join(workspaceRoot, p)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	// filepath.Rel will succeed only if abs is inside workspaceRoot
	rel, err := filepath.Rel(workspaceRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q is outside the workspace (allowed: %s)", p, workspaceRoot)
	}
	return abs, nil
}

// registerBuiltins adds the default set of local tools.
func (r *Registry) registerBuiltins() {
	r.Register(ToolDef{
		Name:        "read_file",
		Description: "Read the contents of a local file",
		Parameters:  map[string]string{"path": "File path to read"},
		Category:    "filesystem",
	}, toolReadFile)

	r.Register(ToolDef{
		Name:        "write_file",
		Description: "Write content to a local file (creates or overwrites)",
		Parameters:  map[string]string{"path": "File path to write", "content": "Content to write"},
		Category:    "filesystem",
	}, toolWriteFile)

	r.Register(ToolDef{
		Name:        "list_dir",
		Description: "List files and directories in a path",
		Parameters:  map[string]string{"path": "Directory path to list"},
		Category:    "filesystem",
	}, toolListDir)

	r.Register(ToolDef{
		Name:        "calculate",
		Description: "Evaluate a simple math expression (add, subtract, multiply, divide, power)",
		Parameters:  map[string]string{"expression": "Math expression like '2 + 3' or '10 * 5'"},
		Category:    "utility",
	}, toolCalculate)

	r.Register(ToolDef{
		Name:       "get_time",
		Description: "Get the current date and time",
		Parameters: map[string]string{},
		Category:   "utility",
	}, toolGetTime)

	r.Register(ToolDef{
		Name:        "search_files",
		Description: "Search for files matching a glob pattern in a directory",
		Parameters:  map[string]string{"directory": "Base directory to search", "pattern": "Glob pattern (e.g. *.go, *.py)"},
		Category:    "filesystem",
	}, toolSearchFiles)

	r.Register(ToolDef{
		Name:        "run_command",
		Description: "Execute a shell command and return combined stdout+stderr. Use to run scripts, compile code, install packages, test programs, etc. Commands run relative to working_dir if provided.",
		Parameters: map[string]string{
			"command":     "Shell command to execute (e.g. 'python script.py', 'go build .', 'pip install requests')",
			"working_dir": "Directory to run the command in (optional)",
		},
		Category: "shell",
	}, toolRunCommand)

	r.Register(ToolDef{
		Name:        "create_dir",
		Description: "Create a directory (and any parent directories) at the given path",
		Parameters:  map[string]string{"path": "Directory path to create"},
		Category:    "filesystem",
	}, toolCreateDir)

	r.Register(ToolDef{
		Name:        "delete",
		Description: "Delete a file or empty directory at the given path",
		Parameters:  map[string]string{"path": "Path to delete"},
		Category:    "filesystem",
	}, toolDelete)

	r.Register(ToolDef{
		Name:        "done",
		Description: "Signal that the task is complete. Provide a summary of what was accomplished.",
		Parameters:  map[string]string{"summary": "Brief summary of what was done"},
		Category:    "utility",
	}, toolDone)

	r.Register(ToolDef{
		Name:        "run_vibra_recipe",
		Description: "Execute a Vibra Ingenn workflow recipe. The recipe must be a valid JSON file inside the workspace containing workflow_id and tasks.",
		Parameters:  map[string]string{"path": "Path to the JSON workflow file inside the workspace (e.g. 'my_workflow.json')"},
		Category:    "utility",
	}, r.toolRunVibraRecipe)
}

func toolReadFile(args map[string]any) ToolResult {
	p, _ := args["path"].(string)
	safe, err := safePath(p)
	if err != nil {
		return ToolResult{ToolName: "read_file", Success: false, Error: err.Error()}
	}
	data, err := os.ReadFile(safe)
	if err != nil {
		return ToolResult{ToolName: "read_file", Success: false, Error: err.Error()}
	}
	content := string(data)
	if len(content) > 4096 {
		content = content[:4096] + "\n... (truncated)"
	}
	return ToolResult{ToolName: "read_file", Success: true, Output: content}
}

func toolWriteFile(args map[string]any) ToolResult {
	p, _ := args["path"].(string)
	content, _ := args["content"].(string)
	safe, err := safePath(p)
	if err != nil {
		return ToolResult{ToolName: "write_file", Success: false, Error: err.Error()}
	}
	dir := filepath.Dir(safe)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ToolResult{ToolName: "write_file", Success: false, Error: err.Error()}
	}
	if err := os.WriteFile(safe, []byte(content), 0644); err != nil {
		return ToolResult{ToolName: "write_file", Success: false, Error: err.Error()}
	}
	return ToolResult{ToolName: "write_file", Success: true, Output: fmt.Sprintf("wrote %d bytes to %s", len(content), safe)}
}

func toolListDir(args map[string]any) ToolResult {
	p, _ := args["path"].(string)
	if p == "" {
		p = "."
	}
	safe, err := safePath(p)
	if err != nil {
		return ToolResult{ToolName: "list_dir", Success: false, Error: err.Error()}
	}
	entries, err := os.ReadDir(safe)
	if err != nil {
		return ToolResult{ToolName: "list_dir", Success: false, Error: err.Error()}
	}
	var sb strings.Builder
	for _, e := range entries {
		prefix := "  "
		if e.IsDir() {
			prefix = "D "
		}
		sb.WriteString(fmt.Sprintf("%s%s\n", prefix, e.Name()))
	}
	return ToolResult{ToolName: "list_dir", Success: true, Output: sb.String()}
}

func toolCalculate(args map[string]any) ToolResult {
	expr, _ := args["expression"].(string)
	if expr == "" {
		return ToolResult{ToolName: "calculate", Success: false, Error: "expression is required"}
	}
	// Simple two-operand calculator
	expr = strings.TrimSpace(expr)
	var a, b float64
	var op string
	for _, candidate := range []string{" + ", " - ", " * ", " / ", " ^ ", " ** "} {
		if idx := strings.Index(expr, candidate); idx >= 0 {
			op = strings.TrimSpace(candidate)
			fmt.Sscanf(expr[:idx], "%f", &a)
			fmt.Sscanf(expr[idx+len(candidate):], "%f", &b)
			break
		}
	}
	if op == "" {
		return ToolResult{ToolName: "calculate", Success: false, Error: "unsupported expression format"}
	}
	var result float64
	switch op {
	case "+":
		result = a + b
	case "-":
		result = a - b
	case "*":
		result = a * b
	case "/":
		if b == 0 {
			return ToolResult{ToolName: "calculate", Success: false, Error: "division by zero"}
		}
		result = a / b
	case "^", "**":
		result = math.Pow(a, b)
	}
	return ToolResult{ToolName: "calculate", Success: true, Output: fmt.Sprintf("%g", result)}
}

func toolGetTime(args map[string]any) ToolResult {
	_ = args
	return ToolResult{ToolName: "get_time", Success: true, Output: time.Now().Format(time.RFC3339)}
}

func toolSearchFiles(args map[string]any) ToolResult {
	d, _ := args["directory"].(string)
	pattern, _ := args["pattern"].(string)
	if d == "" {
		d = "."
	}
	if pattern == "" {
		return ToolResult{ToolName: "search_files", Success: false, Error: "pattern is required"}
	}
	safeDir, err := safePath(d)
	if err != nil {
		return ToolResult{ToolName: "search_files", Success: false, Error: err.Error()}
	}
	var matches []string
	_ = filepath.Walk(safeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if matched, _ := filepath.Match(pattern, info.Name()); matched {
			matches = append(matches, path)
		}
		if len(matches) >= 50 {
			return filepath.SkipAll
		}
		return nil
	})
	if len(matches) == 0 {
		return ToolResult{ToolName: "search_files", Success: true, Output: "no matches found"}
	}
	return ToolResult{ToolName: "search_files", Success: true, Output: strings.Join(matches, "\n")}
}

// commandRisk classifies a command's risk level.
// "safe"     — read-only, local (go build, python script.py, dir, type)
// "install"  — installs packages (pip install, npm install, go get)
// "network"  — makes external requests (curl, wget, Invoke-WebRequest)
// "destruct" — deletes or modifies system state (rm, del, format, reg)
func commandRisk(cmd string) string {
	lower := strings.ToLower(cmd)
	// Destructive
	for _, kw := range []string{"rm -rf", "del /", "rmdir /s", "format ", "reg delete", "reg add"} {
		if strings.Contains(lower, kw) {
			return "destruct"
		}
	}
	// Package installs (network + disk writes)
	for _, kw := range []string{"pip install", "pip3 install", "npm install", "npm i ", "go get ", "go install ", "cargo install", "choco install", "winget install", "scoop install"} {
		if strings.Contains(lower, kw) {
			return "install"
		}
	}
	// Network
	for _, kw := range []string{"curl ", "wget ", "invoke-webrequest", "invoke-restmethod", "iwr ", "irm "} {
		if strings.Contains(lower, kw) {
			return "network"
		}
	}
	return "safe"
}

func toolRunCommand(args map[string]any) ToolResult {
	command, _ := args["command"].(string)
	if command == "" {
		return ToolResult{ToolName: "run_command", Success: false, Error: "command is required"}
	}

	// On Windows, cmd /C interprets metacharacters as shell operators.
	// Block them to prevent command injection via the tool interface.
	if runtime.GOOS == "windows" {
		for _, ch := range []byte{'&', '|', '<', '>', '^', '%', '(', ')'} {
			if strings.ContainsRune(command, rune(ch)) {
				return ToolResult{ToolName: "run_command", Success: false,
					Error: fmt.Sprintf("command contains Windows shell metacharacter %q — use WSL shell tool for complex pipelines", string(ch))}
			}
		}
	}

	workDir, _ := args["working_dir"].(string)

	// Safety check
	risk := commandRisk(command)
	safeMode := envBool("TERMINUS_SAFE_MODE", "JANUS_SAFE_MODE")
	medicalStrict := envBool("TERMINUS_MEDICAL_STRICT", "TERMINUS_HIPAA_STRICT", "JANUS_HIPAA_STRICT")

	if risk != "safe" {
		log.Printf("run_command: [%s] %q", strings.ToUpper(risk), command)
	}

	// Medical strict mode blocks outbound network commands — PHI must not leave this PC.
	if medicalStrict && risk == "network" {
		return ToolResult{ToolName: "run_command", Success: false,
			Error: fmt.Sprintf("BLOCKED by medical strict mode: network command %q would send data outside the local system. Set TERMINUS_MEDICAL_STRICT=false to allow outbound commands.", command)}
	}

	if safeMode {
		if risk == "destruct" {
			return ToolResult{ToolName: "run_command", Success: false,
				Error: fmt.Sprintf("BLOCKED by safe mode: destructive command %q — set TERMINUS_SAFE_MODE=false to allow", command)}
		}
		if risk == "install" {
			return ToolResult{ToolName: "run_command", Success: false,
				Error: fmt.Sprintf("BLOCKED by safe mode: package install %q — set TERMINUS_SAFE_MODE=false to allow", command)}
		}
		if risk == "network" {
			return ToolResult{ToolName: "run_command", Success: false,
				Error: fmt.Sprintf("BLOCKED by safe mode: network command %q — set TERMINUS_SAFE_MODE=false to allow", command)}
		}
	}

	// Resolve working_dir to absolute path so relative paths work correctly.
	if workDir != "" {
		if abs, err := filepath.Abs(workDir); err == nil {
			workDir = abs
		}
	}

	// Prefer 'uv pip' over 'pip' when uv is available (faster, more reliable).
	if strings.Contains(strings.ToLower(command), "pip install") {
		if _, err := exec.LookPath("uv"); err == nil {
			command = strings.Replace(command, "pip3 install", "uv pip install", 1)
			command = strings.Replace(command, "pip install", "uv pip install", 1)
			log.Printf("run_command: upgraded to uv pip: %q", command)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	if workDir != "" {
		cmd.Dir = workDir
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	output := strings.TrimSpace(buf.String())
	if len(output) > 3000 {
		output = output[:3000] + "\n... [output truncated]"
	}

	if err != nil {
		msg := err.Error()
		if output != "" {
			msg = output
		}
		return ToolResult{ToolName: "run_command", Success: false, Error: msg, Output: output}
	}
	if output == "" {
		output = "(command completed with no output)"
	}
	return ToolResult{ToolName: "run_command", Success: true, Output: output}
}

func toolDelete(args map[string]any) ToolResult {
	p, _ := args["path"].(string)
	safe, err := safePath(p)
	if err != nil {
		return ToolResult{ToolName: "delete", Success: false, Error: err.Error()}
	}
	if err := os.Remove(safe); err != nil {
		return ToolResult{ToolName: "delete", Success: false, Error: err.Error()}
	}
	return ToolResult{ToolName: "delete", Success: true, Output: "deleted: " + safe}
}

func toolDone(args map[string]any) ToolResult {
	summary, _ := args["summary"].(string)
	if summary == "" {
		summary = "task completed"
	}
	return ToolResult{ToolName: "done", Success: true, Output: summary}
}

func toolCreateDir(args map[string]any) ToolResult {
	p, _ := args["path"].(string)
	safe, err := safePath(p)
	if err != nil {
		return ToolResult{ToolName: "create_dir", Success: false, Error: err.Error()}
	}
	if err := os.MkdirAll(safe, 0755); err != nil {
		return ToolResult{ToolName: "create_dir", Success: false, Error: err.Error()}
	}
	return ToolResult{ToolName: "create_dir", Success: true, Output: "created: " + safe}
}

func findVibraBinary() string {
	if p, err := exec.LookPath("vibra"); err == nil {
		return p
	}
	if p, err := exec.LookPath("vibra.exe"); err == nil {
		return p
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		cand := filepath.Join(localAppData, "Vibra-Ingenn", "vibra.exe")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

func (r *Registry) toolRunVibraRecipe(args map[string]any) ToolResult {
	vibraBin := findVibraBinary()
	if vibraBin == "" {
		return ToolResult{
			ToolName: "run_vibra_recipe",
			Success:  false,
			Error:    "Running recipes needs Vibe Engine (adeptuscamini.com). Terminus itself parses documents on its own.",
		}
	}

	p, _ := args["path"].(string)
	safe, err := safePath(p)
	if err != nil {
		return ToolResult{ToolName: "run_vibra_recipe", Success: false, Error: err.Error()}
	}

	cmd := exec.Command(vibraBin, "run", safe)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return ToolResult{
			ToolName: "run_vibra_recipe",
			Success:  false,
			Output:   stdout.String(),
			Error:    errMsg,
		}
	}

	return ToolResult{
		ToolName: "run_vibra_recipe",
		Success:  true,
		Output:   stdout.String(),
	}
}
