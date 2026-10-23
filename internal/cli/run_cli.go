package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
)

const runUsage = "usage: magpie run <claude|codex> --home <folder> [<model>] [--cwd <folder>] [-- <the agent's arguments>…]"

// runCmd: `magpie run <claude|codex> --home <folder> [<model>] [--cwd
// <folder>] [-- <arguments>…]` starts Claude Code or Codex with <folder>
// as its config folder (CLAUDE_CONFIG_DIR, CODEX_HOME), wired there to
// magpie's gateway on <model>, one of magpie's models or routing groups,
// beside the agent run as itself, whose own folder is never written
// (#992; agent.Instance). Several folders can run side by side. A relative
// folder is taken from where magpie runs, not --cwd. Like `magpie <agent>
// <model>`, it neither starts nor needs the gateway to write the folder:
// the app, `magpie serve` or the service serves it, and magpie says when
// none answers. The agent gets the terminal, Ctrl+C is its own, and magpie
// ends with its exit code. What follows -- is the agent's, as it came.
func runCmd(args []string) error {
	var pass []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, pass = args[:i], args[i+1:]
	}
	var home, cwd string
	hasHome, hasCwd := false, false
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--home" || a == "--cwd":
			if i+1 == len(args) {
				return fmt.Errorf("%s needs a folder\n%s", a, runUsage)
			}
			if a == "--home" {
				home, hasHome = args[i+1], true
			} else {
				cwd, hasCwd = args[i+1], true
			}
			i++
		case strings.HasPrefix(a, "--home="):
			home, hasHome = strings.TrimPrefix(a, "--home="), true
		case strings.HasPrefix(a, "--cwd="):
			cwd, hasCwd = strings.TrimPrefix(a, "--cwd="), true
		case strings.HasPrefix(a, "-"):
			// never taken for a model: the agent's own go after --
			return fmt.Errorf("unknown %s · the agent's own arguments go after --\n%s", a, runUsage)
		default:
			words = append(words, a)
		}
	}
	if len(words) < 1 || len(words) > 2 {
		return fmt.Errorf("%s", runUsage)
	}
	var kind string
	switch strings.ToLower(words[0]) {
	case "claude", "cc", "claude-code":
		kind = "claude"
	case "codex":
		kind = "codex"
	default:
		return fmt.Errorf("%q: magpie run starts Claude Code (claude) or Codex (codex)\n%s", words[0], runUsage)
	}
	model := ""
	if len(words) == 2 {
		model = words[1]
	}
	if !hasHome || home == "" {
		return fmt.Errorf("--home needs the folder the agent keeps its settings in\n%s", runUsage)
	}
	dir, err := runFolder(home)
	if err != nil {
		return err
	}
	if hasCwd {
		st, err := os.Stat(cwd)
		switch {
		case cwd == "":
			return fmt.Errorf("--cwd needs a folder\n%s", runUsage)
		case err != nil:
			return fmt.Errorf("--cwd: %w", err)
		case !st.IsDir():
			return fmt.Errorf("--cwd: %s is not a folder", cwd)
		}
	}
	in, err := agent.NewInstance(kind, dir)
	if err != nil {
		return err
	}
	bin := proc.FindTool(in.Bin)
	if bin == "" {
		return fmt.Errorf("%s, the `%s` command line, isn't installed here: nothing was written", in.Name, in.Bin)
	}
	m, err := in.Use(model)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, green.Render("✓"), bold.Render(in.Name), muted.Render("model"), m, muted.Render("· "+in.Var+"="+in.Dir))
	if !gateway.Running() {
		fmt.Fprintln(os.Stderr, muted.Render("  magpie's gateway doesn't answer at "+gateway.URL()+": open magpie, or run magpie serve, for "+in.Name+" to reach it"))
	}
	// Ctrl+C is the agent's, which shares the terminal: magpie waits on it
	interrupts.Add(1)
	defer interrupts.Add(-1)
	cmd := proc.Command(bin, pass...)
	cmd.Env = in.Env(os.Environ())
	cmd.Dir = cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return ranExit(cmd.Run())
}

// runFolder is the folder --home names, as an absolute path: ~ is the
// home folder, and a relative one is under the folder magpie runs in.
func runFolder(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(h, p[1:])
	}
	return filepath.Abs(p)
}

// ranExit is err from an agent magpie ran on the terminal, which said why
// it ended itself: its exit code to end with (128 + the signal's number
// for one a signal ended), with nothing more to say; any other error as
// it is.
func ranExit(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
		if code <= 0 {
			code = 1
		}
		return exitError{code: code}
	}
	return err
}
