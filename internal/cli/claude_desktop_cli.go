package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// Claude Desktop instances (#991): the official Claude Desktop started with
// a userData folder of its own, given by Chromium's --user-data-dir, one
// folder per name under magpie's. The user signs in inside it; magpie never
// reads, copies or writes anything in that folder or in Desktop's usual one,
// and an instance runs beside the usual Desktop and the other instances.
// magpie's gateway wiring (3p mode, internal/agent/claudedesktop.go) stays
// on the usual Desktop only.

const claudeDesktopUsage = "usage: magpie claude-desktop run <name>   start Claude Desktop with a folder of its own for <name> (made when missing), beside your usual one: sign in there\n" +
	"       magpie claude-desktop list         the instances, and whether each is running\n" +
	"       magpie claude-desktop rm <name>    delete an instance's folder, its sign-in and data with it (not while it runs)\n" +
	"  macOS only. magpie never reads or writes what is in an instance's folder; connecting to magpie's gateway is for your usual Claude Desktop"

// desktopBundleID is Claude.app's CFBundleIdentifier (Info.plist of
// Claude Desktop 3.1623.0), which open -b finds the app by wherever it is
// installed.
const desktopBundleID = "com.anthropic.claudefordesktop"

// claudeDesktop is what the command reads of the machine and starts; tests
// stand in for it.
var claudeDesktop = struct {
	goos   string
	root   func() string
	launch func(name string, args ...string) ([]byte, error)
	list   func(ctx context.Context) ([]proc.Process, error)
}{
	goos: runtime.GOOS,
	root: func() string { return filepath.Join(appdir.Config(), "claude-desktop") },
	launch: func(name string, args ...string) ([]byte, error) {
		return proc.Command(name, args...).CombinedOutput()
	},
	list: func(ctx context.Context) ([]proc.Process, error) { return proc.List(ctx, "") },
}

// desktopInstanceCmd says whether `magpie <args…>` is an instance command:
// claude-desktop (or its alias) followed by run, list or rm. Any other word
// after it is still Desktop's provider or a field, as for every agent.
func desktopInstanceCmd(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "run", "list", "rm":
	default:
		return false
	}
	a, err := agent.Find(args[0])
	return err == nil && a.ID == "claude-desktop"
}

// claudeDesktopCmd: magpie claude-desktop run <name> | list | rm <name>.
func claudeDesktopCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(claudeDesktopUsage)
	}
	if err := desktopSupported(claudeDesktop.goos); err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) > 1 {
			return fmt.Errorf("magpie claude-desktop list takes nothing more\n%s", claudeDesktopUsage)
		}
		return desktopList()
	case "run", "rm":
		if len(args) != 2 {
			return fmt.Errorf("magpie claude-desktop %s takes one name\n%s", args[0], claudeDesktopUsage)
		}
		if err := desktopName(args[1]); err != nil {
			return err
		}
		if args[0] == "run" {
			return desktopRun(args[1])
		}
		return desktopRemove(args[1])
	}
	return fmt.Errorf("magpie claude-desktop: unknown %q\n%s", args[0], claudeDesktopUsage)
}

// desktopSupported: macOS only. On Windows Desktop keeps its 3p mode's
// folder at %LOCALAPPDATA%\Claude-3p whatever folder it is started with
// (UQ() in its app.asar, 3.1623.0), so an instance there would share it
// with the usual Desktop; and no Claude Desktop for Linux has been tried.
func desktopSupported(goos string) error {
	switch goos {
	case "darwin":
		return nil
	case "windows":
		return errors.New(`magpie claude-desktop runs Claude Desktop instances on macOS only: on Windows Desktop keeps its 3p mode's folder at %LOCALAPPDATA%\Claude-3p whatever folder it is started with, so an instance wouldn't be kept apart from your usual Desktop`)
	}
	return fmt.Errorf("magpie claude-desktop runs Claude Desktop instances on macOS only: it hasn't been tried on %s", goos)
}

var desktopNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// desktopName checks an instance's name, which is its folder's name under
// the root: no path separator, dot or other character that could name a
// folder outside it. Lowercase only, as a Mac's folders don't tell Work
// from work while a running instance is known by its folder as written.
// Not <x>-3p: Desktop keeps an instance's 3p mode in <folder>-3p beside it
// (UQ() appends -3p to userData).
func desktopName(name string) error {
	if !desktopNameRe.MatchString(name) {
		return fmt.Errorf("%q can't name a Claude Desktop instance: use up to 32 lowercase letters, digits, - and _, starting with a letter or digit", name)
	}
	if strings.HasSuffix(name, "-3p") {
		return fmt.Errorf("%q can't name a Claude Desktop instance: Desktop keeps an instance's 3p mode in a folder named <name>-3p beside it", name)
	}
	return nil
}

// desktopRunning is the id of a process started on dir (an argument
// --user-data-dir=dir, the folder exactly), its main process's when it is
// among them, 0 for none.
func desktopRunning(ps []proc.Process, dir string) int {
	flag := "--user-data-dir=" + dir
	pid := 0
	for _, p := range ps {
		on := false
		for rest := p.Args; ; {
			i := strings.Index(rest, flag)
			if i < 0 {
				break
			}
			rest = rest[i+len(flag):]
			if rest == "" || rest[0] == ' ' {
				on = true
				break
			}
		}
		if !on {
			continue
		}
		// Chromium's helpers carry --type=…; the app itself doesn't
		if !strings.Contains(p.Args, " --type=") {
			return p.PID
		}
		if pid == 0 {
			pid = p.PID
		}
	}
	return pid
}

// desktopProcesses lists the running processes, an error meaning they
// couldn't be asked, not that none runs.
func desktopProcesses() ([]proc.Process, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return claudeDesktop.list(ctx)
}

// desktopRun starts Desktop on the instance's folder, made when missing.
// A second Desktop started on the folder of a running one made both quit
// (3.1623.0), so a running instance is left as it is, and one that can't
// be told running or not isn't started.
func desktopRun(name string) error {
	dir := filepath.Join(claudeDesktop.root(), name)
	ps, err := desktopProcesses()
	if err != nil {
		return fmt.Errorf("can't tell whether Claude Desktop %q is running, so it wasn't started: %w", name, err)
	}
	if pid := desktopRunning(ps, dir); pid != 0 {
		fmt.Println(green.Render("●"), "Claude Desktop", bold.Render(name), "is running already", muted.Render("(pid "+strconv.Itoa(pid)+")"))
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// open -n: a new instance, not the usual Desktop brought forward, and
	// started by launchd rather than as this terminal's child, which a
	// closed terminal would end
	out, err := claudeDesktop.launch("open", "-n", "-b", desktopBundleID, "--args", "--user-data-dir="+dir)
	if err != nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return fmt.Errorf("starting Claude Desktop: %s", s)
		}
		return fmt.Errorf("starting Claude Desktop: %w", err)
	}
	fmt.Println(green.Render("✓"), "started Claude Desktop", bold.Render(name), muted.Render(dir))
	fmt.Println(muted.Render("  sign in there: it keeps its own sign-in and data, apart from your usual Claude Desktop"))
	return nil
}

// desktopList prints the instances under the root, each with whether it
// runs.
func desktopList() error {
	root := claudeDesktop.root()
	ents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Println(muted.Render("no Claude Desktop instances · magpie claude-desktop run <name> starts one"))
		return nil
	}
	if err != nil {
		return err
	}
	ps, perr := desktopProcesses()
	n := 0
	for _, e := range ents {
		if !e.IsDir() || desktopName(e.Name()) != nil {
			continue
		}
		n++
		dir := filepath.Join(root, e.Name())
		switch pid := desktopRunning(ps, dir); {
		case perr != nil:
			fmt.Println(amber.Render("?"), bold.Render(e.Name()), muted.Render(dir), muted.Render("· running or not is unknown"))
		case pid != 0:
			fmt.Println(green.Render("●"), bold.Render(e.Name()), muted.Render(dir), muted.Render("· running (pid "+strconv.Itoa(pid)+")"))
		default:
			fmt.Println(faint.Render("○"), bold.Render(e.Name()), muted.Render(dir))
		}
	}
	if n == 0 {
		fmt.Println(muted.Render("no Claude Desktop instances · magpie claude-desktop run <name> starts one"))
	}
	return nil
}

// desktopRemove deletes the instance's folder, and the <folder>-3p Desktop
// makes beside it for the instance's 3p mode, never while it runs or when
// that can't be told.
func desktopRemove(name string) error {
	dir := filepath.Join(claudeDesktop.root(), name)
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("there is no Claude Desktop instance %q", name)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s isn't a folder magpie made: nothing was removed", dir)
	}
	ps, err := desktopProcesses()
	if err != nil {
		return fmt.Errorf("can't tell whether Claude Desktop %q is running, so nothing was removed: %w", name, err)
	}
	if pid := desktopRunning(ps, dir); pid != 0 {
		return fmt.Errorf("Claude Desktop %q is running (pid %d): quit it first; nothing was removed", name, pid)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if fi, err := os.Lstat(dir + "-3p"); err == nil && fi.IsDir() {
		if err := os.RemoveAll(dir + "-3p"); err != nil {
			return err
		}
	}
	fmt.Println(green.Render("✓"), "removed Claude Desktop", bold.Render(name), muted.Render(dir))
	return nil
}
