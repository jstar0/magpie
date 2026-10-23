package cli

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/testenv"
)

// fakeCodex and fakeClaudeCode stand in for the agents `magpie run` starts:
// each writes down the folder it was told, the variables that would take
// it elsewhere, the folder it runs in, its arguments and the settings file
// magpie wired in its folder, and exits 7. Only the shell's own commands,
// with PATH holding nothing else.
const fakeCodex = `#!/bin/sh
{
  printf 'home=%s\n' "${CODEX_HOME-unset}"
  printf 'sqlite=%s\n' "${CODEX_SQLITE_HOME-unset}"
  printf 'cwd=%s\n' "$(pwd -P)"
  for a in "$@"; do printf 'arg=%s\n' "$a"; done
  printf '%s\n' '--- file'
  while IFS= read -r l || [ -n "$l" ]; do printf '%s\n' "$l"; done < "$CODEX_HOME/config.toml"
} > "$FAKE_RECORD"
exit 7
`

const fakeClaudeCode = `#!/bin/sh
{
  printf 'home=%s\n' "${CLAUDE_CONFIG_DIR-unset}"
  printf 'key=%s\n' "${ANTHROPIC_API_KEY-unset}"
  printf 'base=%s\n' "${ANTHROPIC_BASE_URL-unset}"
  printf 'bedrock=%s\n' "${CLAUDE_CODE_USE_BEDROCK-unset}"
  printf 'model=%s\n' "${ANTHROPIC_MODEL-unset}"
  printf 'cwd=%s\n' "$(pwd -P)"
  for a in "$@"; do printf 'arg=%s\n' "$a"; done
  printf '%s\n' '--- file'
  while IFS= read -r l || [ -n "$l" ]; do printf '%s\n' "$l"; done < "$CLAUDE_CONFIG_DIR/settings.json"
} > "$FAKE_RECORD"
exit 7
`

// runHome is groupsHome with the stand-ins on PATH and a gateway that
// answers as magpie's, so nothing asks the machine's own: it gives the
// home, and the gateway's address.
func runHome(t *testing.T) (home, gw string) {
	t.Helper()
	groupsHome(t)
	home = os.Getenv("HOME")
	for _, k := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "CODEX_SQLITE_HOME"} {
		t.Setenv(k, "")
	}
	bin := filepath.Join(home, "fakebin")
	testenv.Program(t, filepath.Join(bin, "codex"), fakeCodex)
	testenv.Program(t, filepath.Join(bin, "claude"), fakeClaudeCode)
	t.Setenv("PATH", bin)
	t.Setenv("FAKE_RECORD", filepath.Join(home, "record"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"name":"magpie"}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	return home, srv.URL
}

// runIn runs magpie with args and gives what it said on stderr, and the
// exit code it would end with.
func runIn(t *testing.T, args ...string) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	read := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		read <- string(b)
	}()
	done := make(chan error, 1)
	go func() { done <- run(args) }()
	select {
	case err = <-done:
	case <-time.After(60 * time.Second):
		os.Stderr = old
		t.Fatalf("magpie %s didn't come back", strings.Join(args, " "))
	}
	w.Close()
	os.Stderr = old
	said := <-read
	r.Close()
	var e exitError
	switch {
	case errors.As(err, &e):
		return said, e.code
	case err != nil:
		return said + "magpie: " + err.Error(), 1
	}
	return said, 0
}

// record is what the stand-in wrote down: its lines before the file, and
// the file magpie wired.
func record(t *testing.T, home string) (env []string, file string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "record"))
	if err != nil {
		t.Fatalf("the agent wasn't run: %v", err)
	}
	os.Remove(filepath.Join(home, "record"))
	head, file, _ := strings.Cut(string(b), "--- file\n")
	return strings.Split(strings.TrimSuffix(head, "\n"), "\n"), file
}

func has(lines []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, l := range lines {
			found = found || l == w
		}
		if !found {
			return false
		}
	}
	return true
}

// `magpie run <agent> --home <folder> <model>` (#992) starts Codex or
// Claude Code with <folder> as its config folder, wired there to magpie's
// gateway on <model>, with what follows -- as it came, in --cwd, and ends
// with the agent's exit code. The user's own ~/.codex, ~/.claude and
// ~/.claude.json are never written, and nothing takes the agent elsewhere.
func TestRunStartsTheAgentInItsFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-ins are shell scripts")
	}
	t.Run("codex", func(t *testing.T) {
		home, gw := runHome(t)
		t.Setenv("CODEX_SQLITE_HOME", filepath.Join(home, "shared-state"))
		dir := filepath.Join(t.TempDir(), "work", "codex-a")
		work := t.TempDir()
		said, code := runIn(t, "run", "codex", "--home", dir, "a/m", "--cwd", work, "--", "exec", "two words", "--help")
		if code != 7 {
			t.Fatalf("exit %d, want Codex's 7: %s", code, said)
		}
		env, cfg := record(t, home)
		realDir, _ := filepath.EvalSymlinks(dir)
		realWork, _ := filepath.EvalSymlinks(work)
		if !has(env, "home="+realDir, "sqlite=unset", "cwd="+realWork, "arg=exec", "arg=two words", "arg=--help") {
			t.Fatalf("Codex was run with %q (magpie said %s)", env, said)
		}
		for _, want := range []string{`model = "a/m"`, `model_provider = "magpie"`, `base_url = "` + gw + `/v1"`} {
			if !strings.Contains(cfg, want) {
				t.Fatalf("the folder's config.toml has no %s:\n%s", want, cfg)
			}
		}
		if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("the folder: %v, %v; want made 0700", st, err)
		}
		for _, own := range []string{".codex", ".claude", ".claude.json"} {
			if _, err := os.Stat(filepath.Join(home, own)); !os.IsNotExist(err) {
				t.Fatalf("~/%s was written: %v", own, err)
			}
		}
	})
	t.Run("claude", func(t *testing.T) {
		home, gw := runHome(t)
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-not-this")
		t.Setenv("ANTHROPIC_BASE_URL", "https://relay.example.com")
		t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
		t.Setenv("ANTHROPIC_MODEL", "opus")
		dir := filepath.Join(t.TempDir(), "claude-a")
		said, code := runIn(t, "run", "claude", "--home="+dir, "a/m", "--", "-p", "hi")
		if code != 7 {
			t.Fatalf("exit %d, want Claude Code's 7: %s", code, said)
		}
		env, settings := record(t, home)
		realDir, _ := filepath.EvalSymlinks(dir)
		if !has(env, "home="+realDir, "key=unset", "base=unset", "bedrock=unset", "model=unset", "arg=-p", "arg=hi") {
			t.Fatalf("Claude Code was run with %q (magpie said %s)", env, said)
		}
		path := filepath.Join(dir, "settings.json")
		if v, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); v != gw {
			t.Fatalf("the folder's settings.json doesn't point at the gateway:\n%s", settings)
		}
		if v, _ := edit.GetJSON(path, "model"); v != "a/m" {
			t.Fatalf("the folder's settings.json isn't on a/m:\n%s", settings)
		}
		for _, own := range []string{".codex", ".claude", ".claude.json"} {
			if _, err := os.Stat(filepath.Join(home, own)); !os.IsNotExist(err) {
				t.Fatalf("~/%s was written: %v", own, err)
			}
		}
	})
}

// Several folders run side by side, the user's own agent beside them: each
// keeps its own model and wiring, and what magpie remembers of one never
// moves another. Claude Code's tiers follow its main model by what magpie
// last wrote as that folder's main model: kept under one id for every
// folder, a folder's run moved the others' (and the user's own), whose
// tiers then stayed behind on the next model.
func TestRunFoldersSideBySide(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-ins are shell scripts")
	}
	home, _ := runHome(t)
	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	tier := func(dir string) string {
		v, _ := edit.GetJSON(filepath.Join(dir, "settings.json"), "env.ANTHROPIC_DEFAULT_SONNET_MODEL")
		return v
	}
	model := func(dir string) string {
		v, _ := edit.GetJSON(filepath.Join(dir, "settings.json"), "model")
		return v
	}
	// the user's own Claude Code, on magpie
	if err := run([]string{"claude", "a/m"}); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(home, ".claude")
	for _, step := range []struct{ dir, model string }{{a, "a/m"}, {b, "a/only-a"}} {
		if said, code := runIn(t, "run", "claude", "--home", step.dir, step.model); code != 7 {
			t.Fatalf("%s: exit %d: %s", step.dir, code, said)
		}
	}
	// the next model for a and for the user's own: their tiers follow it
	if said, code := runIn(t, "run", "claude", "--home", a, "b/gpt-5.5"); code != 7 {
		t.Fatalf("exit %d: %s", code, said)
	}
	if err := run([]string{"claude", "a/claude-opus-5-5"}); err != nil {
		t.Fatal(err)
	}
	if model(a) != "b/gpt-5.5" || tier(a) != "b/gpt-5.5" {
		t.Errorf("folder a: model %q, sonnet tier %q; want both on b/gpt-5.5", model(a), tier(a))
	}
	if model(b) != "a/only-a" || tier(b) != "a/only-a" {
		t.Errorf("folder b: model %q, sonnet tier %q; want both on a/only-a still", model(b), tier(b))
	}
	if model(own) != "a/claude-opus-5-5" || tier(own) != "a/claude-opus-5-5" {
		t.Errorf("your own Claude Code: model %q, sonnet tier %q; want both on a/claude-opus-5-5", model(own), tier(own))
	}
	// with no model, a folder runs on the one it was on
	if said, code := runIn(t, "run", "claude", "--home", b); code != 7 || model(b) != "a/only-a" {
		t.Fatalf("exit %d, folder b on %q: %s", code, model(b), said)
	}
	// and so do two Codex folders, the user's own Codex never made
	x, y := filepath.Join(t.TempDir(), "x"), filepath.Join(t.TempDir(), "y")
	for _, step := range []struct{ dir, model string }{{x, "a/m"}, {y, "b/gpt-5.5"}, {x, ""}} {
		args := []string{"run", "codex", "--home", step.dir}
		if step.model != "" {
			args = append(args, step.model)
		}
		if said, code := runIn(t, args...); code != 7 {
			t.Fatalf("%v: exit %d: %s", args, code, said)
		}
	}
	for dir, want := range map[string]string{x: "a/m", y: "b/gpt-5.5"} {
		if v, _ := edit.GetTOMLTop(filepath.Join(dir, "config.toml"), "model"); v != want {
			t.Errorf("%s's Codex on %q, want %q", filepath.Base(dir), v, want)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("~/.codex was written: %v", err)
	}
}

// What the user set in the folder stays: magpie lays its wiring over the
// files as it does in the agent's own (code standards: magpie's default
// never overrides a value the user set).
func TestRunKeepsWhatTheFolderHas(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-ins are shell scripts")
	}
	home, _ := runHome(t)
	cx := filepath.Join(t.TempDir(), "codex")
	cc := filepath.Join(t.TempDir(), "claude")
	for p, s := range map[string]string{
		filepath.Join(cx, "config.toml"):   "# mine\napproval_policy = \"never\"\n\n[projects.\"/work\"]\ntrust_level = \"trusted\"\n",
		filepath.Join(cc, "settings.json"): "{\n  // mine\n  \"theme\": \"dark\",\n  \"env\": {\"CLAUDE_CODE_MAX_OUTPUT_TOKENS\": \"12345\", \"FOO\": \"bar\"}\n}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if said, code := runIn(t, "run", "codex", "--home", cx, "a/m"); code != 7 {
		t.Fatalf("exit %d: %s", code, said)
	}
	_, cfg := record(t, home)
	for _, want := range []string{"# mine", `approval_policy = "never"`, `trust_level = "trusted"`, `model = "a/m"`} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("config.toml lost %s:\n%s", want, cfg)
		}
	}
	if said, code := runIn(t, "run", "claude", "--home", cc, "a/m"); code != 7 {
		t.Fatalf("exit %d: %s", code, said)
	}
	_, settings := record(t, home)
	path := filepath.Join(cc, "settings.json")
	for k, want := range map[string]string{"theme": "dark", "env.CLAUDE_CODE_MAX_OUTPUT_TOKENS": "12345", "env.FOO": "bar", "model": "a/m"} {
		if v, _ := edit.GetJSON(path, k); v != want {
			t.Fatalf("settings.json's %s is %q, want %q:\n%s", k, v, want, settings)
		}
	}
	if !strings.Contains(settings, "// mine") {
		t.Fatalf("settings.json lost the user's comment:\n%s", settings)
	}
}

// Refused, with nothing run and nothing written: a model that isn't one of
// magpie's, the user's own folders, a file, magpie's folder, a folder
// holding a sign-in, and what the command doesn't take.
func TestRunRefuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-ins are shell scripts")
	}
	home, _ := runHome(t)
	file := filepath.Join(home, "a-file")
	os.WriteFile(file, []byte("x"), 0o600)
	signed := filepath.Join(home, "signed")
	os.MkdirAll(signed, 0o700)
	os.WriteFile(filepath.Join(signed, "auth.json"), []byte(`{"tokens":{"access_token":"x"}}`), 0o600)
	signedCC := filepath.Join(home, "signed-cc")
	os.MkdirAll(signedCC, 0o700)
	os.WriteFile(filepath.Join(signedCC, ".credentials.json"), []byte(`{}`), 0o600)
	linked := filepath.Join(home, "linked")
	os.MkdirAll(filepath.Join(home, ".codex"), 0o700)
	os.Symlink(filepath.Join(home, ".codex"), linked)
	elsewhere := filepath.Join(home, "elsewhere-codex")
	fresh := filepath.Join(home, "fresh")
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"codex", "--home", fresh, "nope/x"}, "isn't one of magpie's models"},
		{[]string{"codex", "--home", fresh, "magpie/a/nope"}, "isn't a model in magpie's catalog"},
		{[]string{"codex", "--home", fresh, "gpt-5.5"}, "isn't one of magpie's models"},
		{[]string{"claude", "--home", fresh, "opus"}, "isn't one of magpie's models"},
		{[]string{"codex", "--home", file, "a/m"}, "is a file"},
		{[]string{"codex", "--home", filepath.Join(home, ".codex"), "a/m"}, "own folder"},
		{[]string{"codex", "--home", filepath.Join(home, ".codex", "sub"), "a/m"}, "own folder"},
		{[]string{"codex", "--home", linked, "a/m"}, "own folder"},
		{[]string{"claude", "--home", filepath.Join(home, ".claude"), "a/m"}, "own folder"},
		{[]string{"claude", "--home", home, "a/m"}, "home folder"},
		{[]string{"claude", "--home", filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie", "claude-accounts", "0123456789abcdef"), "a/m"}, "magpie's own folder"},
		{[]string{"codex", "--home", signed, "a/m"}, "holds a sign-in"},
		{[]string{"claude", "--home", signedCC, "a/m"}, "holds a sign-in"},
		{[]string{"gemini", "--home", fresh, "a/m"}, "Claude Code (claude) or Codex (codex)"},
		{[]string{"codex", "a/m"}, "--home"},
		{[]string{"codex", "--home", fresh, "--model", "a/m"}, "unknown --model"},
		{[]string{"codex", "--home", fresh, "a/m", "extra"}, "usage"},
		{[]string{"codex", "--home", fresh, "a/m", "--cwd", file}, "not a folder"},
	} {
		said, code := runIn(t, append([]string{"run"}, c.args...)...)
		if code != 1 || !strings.Contains(said, c.says) {
			t.Errorf("magpie run %s: exit %d, %q; want 1 and %q", strings.Join(c.args, " "), code, said, c.says)
		}
		if _, err := os.Stat(filepath.Join(home, "record")); err == nil {
			t.Fatalf("magpie run %s ran the agent", strings.Join(c.args, " "))
		}
		for _, p := range []string{fresh, filepath.Join(home, ".claude"), filepath.Join(home, ".codex", "config.toml"), filepath.Join(home, ".claude.json"), filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie", "claude-accounts"), filepath.Join(signed, "config.toml"), filepath.Join(signedCC, "settings.json")} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("magpie run %s wrote %s", strings.Join(c.args, " "), p)
			}
		}
	}
	// CODEX_HOME in magpie's environment is the user's own Codex's folder
	t.Setenv("CODEX_HOME", elsewhere)
	if said, code := runIn(t, "run", "codex", "--home", elsewhere, "a/m"); code != 1 || !strings.Contains(said, "own folder") {
		t.Errorf("CODEX_HOME's folder: exit %d, %q", code, said)
	}
	t.Setenv("CODEX_HOME", "")
	// no Codex on this machine: nothing written either
	t.Setenv("PATH", t.TempDir())
	if said, code := runIn(t, "run", "codex", "--home", fresh, "a/m"); code != 1 || !strings.Contains(said, "isn't installed") {
		t.Errorf("no codex: exit %d, %q", code, said)
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatalf("no codex, and %s was made", fresh)
	}
}

// A relative folder is taken from where magpie runs, and the gateway not
// answering is said, the folder wired all the same, as `magpie <agent>
// <model>` wires an agent while nothing serves.
func TestRunRelativeFolderAndNoGateway(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-ins are shell scripts")
	}
	home, _ := runHome(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:1")
	base := t.TempDir()
	t.Chdir(base)
	said, code := runIn(t, "run", "codex", "--home", "inst", "a/m")
	if code != 7 {
		t.Fatalf("exit %d: %s", code, said)
	}
	env, cfg := record(t, home)
	realBase, _ := filepath.EvalSymlinks(base)
	if !has(env, "home="+filepath.Join(realBase, "inst"), "cwd="+realBase) {
		t.Fatalf("Codex was run with %q", env)
	}
	if !strings.Contains(cfg, `base_url = "http://127.0.0.1:1/v1"`) {
		t.Fatalf("not wired:\n%s", cfg)
	}
	if !strings.Contains(said, "gateway doesn't answer at http://127.0.0.1:1") {
		t.Fatalf("magpie didn't say the gateway doesn't answer: %s", said)
	}
}

// What follows -- is the agent's: its --help is passed on, never magpie's.
func TestRunHelpIsTheAgents(t *testing.T) {
	if helpAsked([]string{"run", "codex", "--home", "/x", "--", "--help"}) {
		t.Fatal("magpie took the agent's --help for its own")
	}
	if !helpAsked([]string{"run", "--help"}) || !helpAsked([]string{"run", "codex", "-h", "--", "x"}) {
		t.Fatal("magpie run --help isn't magpie's help")
	}
}
