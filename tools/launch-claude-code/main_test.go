package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchScriptPreservesArguments(t *testing.T) {
	project := t.TempDir()
	stubs := t.TempDir()
	stub := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(stubs, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	task := `fix the "login" bug; it's $HOME's fault`
	file := filepath.Join(t.TempDir(), "launch.sh")
	if err := os.WriteFile(file, []byte(launchScript(project, "sonnet", "--permission-mode plan", task)), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", file)
	cmd.Env = append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := strings.Join([]string{project, "--model", "sonnet", "--permission-mode", "plan", task}, "\n") + "\n"
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("script was not deleted: %v", err)
	}
}

func TestHasModelFlag(t *testing.T) {
	for args, want := range map[string]bool{
		"--model opus": true, "--continue --model=haiku": true,
		"--continue": false, "--models": false, "": false,
	} {
		if got := hasModelFlag(args); got != want {
			t.Errorf("hasModelFlag(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestLaunchScriptOmitsModelByDefault(t *testing.T) {
	if s := launchScript("/p", "", "--continue", ""); strings.Contains(s, "--model") {
		t.Errorf("unexpected --model in script:\n%s", s)
	}
}
