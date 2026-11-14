package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This runs the actual entrypoint/exit handling, not a reimplementation in a
// helper process. Non-network commands do not access any credential backend.
func TestCLIExecutableCommands(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goBin += ".exe"
	}
	if _, err := os.Stat(goBin); err != nil {
		t.Fatal("Go toolchain required to build CLI integration executable:", err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "kapten")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	// Linking under a loaded test run can take minutes on a CI runner.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, goBin, "build", "-o", binary, "-ldflags", "-X main.version=integration -X main.commit=fixture -X main.date=synthetic", "./cmd/kapten")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"root", nil, "Usage:"},
		{"root-help", []string{"--help"}, "Get started:"},
		{"version", []string{"--version"}, "kapten integration (commit fixture, built synthetic)"},
		{"help-command", []string{"help"}, "Usage:"},
		{"auth-help-topic", []string{"help", "auth"}, "login"},
		{"auth-login-help-topic", []string{"help", "auth", "login"}, "kapten auth login"},
		{"auth-help-command", []string{"auth", "help", "login"}, "kapten auth login"},
		{"auth-completion-help", []string{"auth", "completion", "--help"}, "kapten auth completion"},
		{"auth-bash-completion", []string{"auth", "completion", "bash"}, "kapten"},
		{"auth-zsh-completion", []string{"auth", "completion", "zsh"}, "kapten"},
		{"auth-fish-completion", []string{"auth", "completion", "fish"}, "kapten"},
		{"auth-powershell-completion", []string{"auth", "completion", "powershell"}, "kapten"},
		{"completion-help", []string{"completion", "--help"}, "bash"},
		{"bash-completion", []string{"completion", "bash"}, "kapten"},
		{"zsh-completion", []string{"completion", "zsh"}, "kapten"},
		{"fish-completion", []string{"completion", "fish"}, "kapten"},
		{"powershell-completion", []string{"completion", "powershell"}, "kapten"},
		{"auth-list-empty", []string{"auth", "list"}, "No Kapten contexts"},
		{"auth-logout-empty", []string{"auth", "logout"}, ""},
		{"auth-logout-all-empty", []string{"auth", "logout", "--all"}, ""},
	}
	for _, prefix := range [][]string{nil, {"auth"}} {
		for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
			args := append(append([]string{}, prefix...), "completion", shell, "--help")
			cases = append(cases, struct {
				name string
				args []string
				want string
			}{strings.Join(args, "-"), args, "Usage:"})
		}
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			childCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			cmd := exec.CommandContext(childCtx, binary, test.args...)
			cmd.Env = isolatedProcessEnv(home)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("kapten %q: %v; stdout=%s stderr=%s", test.args, err, &stdout, &stderr)
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("kapten %q missing %q: %s", test.args, test.want, &stdout)
			}
			if strings.TrimSpace(stderr.String()) != "" {
				t.Fatalf("unexpected diagnostics: %s", &stderr)
			}
		})
	}
	t.Run("argument-error-exit", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, binary, "auth", "use")
		cmd.Env = isolatedProcessEnv(t.TempDir())
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("error exit = %v", err)
		}
		if !strings.Contains(stderr.String(), "kapten:") {
			t.Fatalf("entrypoint omitted error: %s", &stderr)
		}
	})
}

func isolatedProcessEnv(home string) []string {
	// Filter before adding overrides: duplicate environment keys are platform-dependent.
	keys := []string{"HOME", "USERPROFILE", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "KAPTEN_CONFIG_DIR", "KAPTEN_API_URL", "KAPTEN_ISSUER_URL", "KAPTEN_OAUTH_AUDIENCE", "KAPTEN_OAUTH_CLIENT_ID"}
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		remove := strings.HasPrefix(key, "RSH_")
		for _, owned := range keys {
			remove = remove || key == owned
		}
		if !remove {
			env = append(env, value)
		}
	}
	return append(env, "HOME="+home, "USERPROFILE="+home, "XDG_CACHE_HOME="+filepath.Join(home, "cache"), "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "KAPTEN_CONFIG_DIR="+filepath.Join(home, "kapten"))
}
