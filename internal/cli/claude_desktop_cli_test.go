package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/proc"
)

// desktopFake stands in for the Mac and Claude Desktop: the processes
// listed (or the error listing them gives) and every command started.
type desktopFake struct {
	ps       []proc.Process
	listErr  error
	launched [][]string
}

// fakeDesktop puts a fake in place of claudeDesktop for the test, on goos,
// with magpie's own root under the test's home.
func fakeDesktop(t *testing.T, goos string) *desktopFake {
	t.Helper()
	f := &desktopFake{}
	old := claudeDesktop
	t.Cleanup(func() { claudeDesktop = old })
	claudeDesktop.goos = goos
	claudeDesktop.launch = func(name string, args ...string) ([]byte, error) {
		f.launched = append(f.launched, append([]string{name}, args...))
		return nil, nil
	}
	claudeDesktop.list = func(context.Context) ([]proc.Process, error) { return f.ps, f.listErr }
	return f
}

func desktopRoot() string { return filepath.Join(appdir.Config(), "claude-desktop") }

// `magpie claude-desktop run work` (#991) starts the official app through
// LaunchServices as a new instance on a folder of its own under magpie's,
// made empty and private; nothing is written in Desktop's usual folders,
// and the usual Desktop and another instance running don't stop it.
func TestClaudeDesktopRunStartsAnInstanceOfItsOwn(t *testing.T) {
	home := helpHome(t)
	f := fakeDesktop(t, "darwin")
	dir := filepath.Join(desktopRoot(), "work")
	f.ps = []proc.Process{
		{PID: 100, Args: "/Applications/Claude.app/Contents/MacOS/Claude"},
		{PID: 200, Args: "/Applications/Claude.app/Contents/MacOS/Claude --user-data-dir=" + dir + "2"},
	}
	out, err := printed(t, func() error { return run([]string{"claude-desktop", "run", "work"}) })
	if err != nil {
		t.Fatalf("magpie claude-desktop run work: %v\n%s", err, out)
	}
	want := [][]string{{"open", "-n", "-b", "com.anthropic.claudefordesktop", "--args", "--user-data-dir=" + dir}}
	if !reflect.DeepEqual(f.launched, want) {
		t.Fatalf("started %q, want %q", f.launched, want)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("the instance's folder %s wasn't made: %v", dir, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("the instance's folder is %o, want 700: it holds the instance's sign-in", perm)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("magpie wrote into the instance's folder: %v", ents)
	}
	d := desktopdir.Find("darwin", home, os.Getenv)
	for _, p := range d.All() {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("Desktop's own folder %s was made", p)
		}
	}
	if !strings.Contains(out, "started Claude Desktop") || !strings.Contains(out, dir) {
		t.Errorf("printed %q", out)
	}
}

// A second Desktop started on a running instance's folder made both quit
// (3.1623.0), so run leaves a running instance as it is.
func TestClaudeDesktopRunLeavesARunningInstance(t *testing.T) {
	helpHome(t)
	f := fakeDesktop(t, "darwin")
	dir := filepath.Join(desktopRoot(), "work")
	f.ps = []proc.Process{
		{PID: 4243, Args: "/Applications/Claude.app/Contents/Frameworks/Claude Helper.app/Contents/MacOS/Claude Helper --type=gpu-process --user-data-dir=" + dir + " --shared-files"},
		{PID: 4242, Args: "/Applications/Claude.app/Contents/MacOS/Claude --user-data-dir=" + dir},
	}
	out, err := printed(t, func() error { return run([]string{"claude-desktop", "run", "work"}) })
	if err != nil {
		t.Fatalf("magpie claude-desktop run work: %v", err)
	}
	if len(f.launched) != 0 {
		t.Fatalf("started %q beside the running instance", f.launched)
	}
	if !strings.Contains(out, "running already") || !strings.Contains(out, "4242") {
		t.Errorf("printed %q, want it running as pid 4242", out)
	}
}

// Processes that can't be listed are unknown, not none: run starts
// nothing and makes no folder.
func TestClaudeDesktopRunWhenProcessesAreUnknown(t *testing.T) {
	helpHome(t)
	f := fakeDesktop(t, "darwin")
	f.listErr = errors.New("ps: not permitted")
	_, err := printed(t, func() error { return run([]string{"claude-desktop", "run", "work"}) })
	if err == nil || !strings.Contains(err.Error(), "can't tell") {
		t.Fatalf("err = %v, want it unable to tell", err)
	}
	if len(f.launched) != 0 {
		t.Fatalf("started %q", f.launched)
	}
	if _, err := os.Stat(filepath.Join(desktopRoot(), "work")); err == nil {
		t.Fatal("the folder was made")
	}
}

// A name is only a folder's name under magpie's claude-desktop folder: one
// that would reach outside it, a flag, an upper case letter (a Mac's
// folders don't tell Work from work) or Desktop's own -3p sibling is
// refused by run and rm, nothing started, made or removed.
func TestClaudeDesktopRefusesNamesOutsideItsFolder(t *testing.T) {
	home := helpHome(t)
	f := fakeDesktop(t, "darwin")
	// a folder beside the root, which ../ would reach
	outside := filepath.Join(filepath.Dir(desktopRoot()), "keep")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(desktopRoot(), "x-3p"), 0o700); err != nil {
		t.Fatal(err)
	}
	// magpie's own folders and files, which any first run makes
	if _, err := printed(t, func() error { return run([]string{"claude-desktop", "list"}) }); err != nil {
		t.Fatal(err)
	}
	before := tree(t, home)
	for _, name := range []string{"", ".", "..", "../keep", "a/b", `a\b`, "/tmp/x", "~", "Work", "-x", "--user-data-dir=/tmp", "work.x", "a b", strings.Repeat("a", 33), "x-3p"} {
		for _, cmd := range []string{"run", "rm"} {
			_, err := printed(t, func() error { return run([]string{"claude-desktop", cmd, name}) })
			if err == nil {
				t.Errorf("magpie claude-desktop %s %q was taken", cmd, name)
			}
		}
	}
	if len(f.launched) != 0 {
		t.Fatalf("started %q", f.launched)
	}
	if got := tree(t, home); !reflect.DeepEqual(got, before) {
		t.Fatalf("a refused name changed the home:\nbefore %v\nafter  %v", before, got)
	}
	if err := desktopName(strings.Repeat("a", 32)); err != nil {
		t.Errorf("32 characters: %v", err)
	}
	if err := desktopName("work_2-b"); err != nil {
		t.Errorf("work_2-b: %v", err)
	}
}

// rm deletes the instance's folder and the -3p one Desktop makes beside it
// for the instance's 3p mode, and nothing else.
func TestClaudeDesktopRmRemovesItsFolders(t *testing.T) {
	helpHome(t)
	f := fakeDesktop(t, "darwin")
	root := desktopRoot()
	for _, d := range []string{"work/Local Storage", "work-3p/configLibrary", "work2", "workx-3p"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.ps = []proc.Process{{PID: 7, Args: "/Applications/Claude.app/Contents/MacOS/Claude --user-data-dir=" + filepath.Join(root, "work2")}}
	if _, err := printed(t, func() error { return run([]string{"claude-desktop", "rm", "work"}) }); err != nil {
		t.Fatalf("magpie claude-desktop rm work: %v", err)
	}
	for _, d := range []string{"work", "work-3p"} {
		if _, err := os.Lstat(filepath.Join(root, d)); err == nil {
			t.Errorf("%s is still there", d)
		}
	}
	for _, d := range []string{"work2", "workx-3p"} {
		if _, err := os.Lstat(filepath.Join(root, d)); err != nil {
			t.Errorf("%s went: %v", d, err)
		}
	}
	if _, err := printed(t, func() error { return run([]string{"claude-desktop", "rm", "work"}) }); err == nil {
		t.Error("removing it again said nothing")
	}
}

// rm never deletes a running instance's folder, nor one it can't tell is
// running.
func TestClaudeDesktopRmRefusesWhileRunning(t *testing.T) {
	helpHome(t)
	f := fakeDesktop(t, "darwin")
	dir := filepath.Join(desktopRoot(), "work")
	if err := os.MkdirAll(filepath.Join(dir, "IndexedDB"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.ps = []proc.Process{{PID: 4242, Args: "/Applications/Claude.app/Contents/MacOS/Claude --user-data-dir=" + dir}}
	_, err := printed(t, func() error { return run([]string{"claude-desktop", "rm", "work"}) })
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("err = %v, want it refused as running", err)
	}
	f.ps, f.listErr = nil, errors.New("ps: not permitted")
	if _, err := printed(t, func() error { return run([]string{"claude-desktop", "rm", "work"}) }); err == nil {
		t.Fatal("removed with the processes unknown")
	}
	if _, err := os.Stat(filepath.Join(dir, "IndexedDB")); err != nil {
		t.Fatalf("the running instance's folder went: %v", err)
	}
}

// list shows each instance and whether it runs, and leaves out Desktop's
// -3p folders and anything that isn't an instance's.
func TestClaudeDesktopList(t *testing.T) {
	helpHome(t)
	f := fakeDesktop(t, "darwin")
	out, err := printed(t, func() error { return run([]string{"claude-desktop", "list"}) })
	if err != nil || !strings.Contains(out, "no Claude Desktop instances") {
		t.Fatalf("no folder: %v %q", err, out)
	}
	root := desktopRoot()
	for _, d := range []string{"work", "home", "work-3p", "Notes"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "stray"), nil, 0o600)
	f.ps = []proc.Process{{PID: 4242, Args: "/Applications/Claude.app/Contents/MacOS/Claude --user-data-dir=" + filepath.Join(root, "work")}}
	out, err = printed(t, func() error { return run([]string{"claude-desktop", "list"}) })
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "home") || strings.Contains(lines[0], "running") ||
		!strings.Contains(lines[1], "work") || !strings.Contains(lines[1], "running (pid 4242)") {
		t.Fatalf("list printed %q", out)
	}
	f.ps, f.listErr = nil, errors.New("ps: not permitted")
	out, _ = printed(t, func() error { return run([]string{"claude-desktop", "list"}) })
	if strings.Count(out, "unknown") != 2 {
		t.Fatalf("with the processes unknown, list printed %q", out)
	}
}

// On Windows Desktop's 3p folder is %LOCALAPPDATA%\Claude-3p whatever
// folder it starts with, so an instance wouldn't be apart; Linux is
// untried. Neither starts anything nor makes a folder.
func TestClaudeDesktopMacOnly(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		t.Run(goos, func(t *testing.T) {
			helpHome(t)
			f := fakeDesktop(t, goos)
			_, err := printed(t, func() error { return run([]string{"claude-desktop", "run", "work"}) })
			if err == nil || !strings.Contains(err.Error(), "macOS only") {
				t.Fatalf("err = %v", err)
			}
			if len(f.launched) != 0 {
				t.Fatalf("started %q", f.launched)
			}
			if _, err := os.Stat(desktopRoot()); err == nil {
				t.Fatal("the folder was made")
			}
		})
	}
}

// Only run, list and rm after claude-desktop (or claude-app) are the
// instances'; every other word is still Desktop's provider or a field,
// and its help gives both.
func TestClaudeDesktopAgentFormsStay(t *testing.T) {
	for args, want := range map[string]bool{
		"claude-desktop run x": true, "claude-app list": true, "claude-desktop rm x": true,
		"claude-desktop": false, "claude-desktop provider": false, "claude-desktop default": false,
		"claude-desktop sonnet run": false, "codex run": false, "claude run x": false,
	} {
		if got := desktopInstanceCmd(strings.Fields(args)); got != want {
			t.Errorf("magpie %s: instance command %v, want %v", args, got, want)
		}
	}
	help := helpFor([]string{"claude-desktop"})
	if !strings.Contains(help, "magpie claude-desktop <model>") || !strings.Contains(help, "magpie claude-desktop run <name>") {
		t.Errorf("magpie claude-desktop --help: %q", help)
	}
}
