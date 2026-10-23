package agent

// An instance (#992) is Claude Code or Codex started by `magpie run` with
// a config folder the user names, as the agent itself takes one:
// CLAUDE_CONFIG_DIR for Claude Code, CODEX_HOME for Codex. magpie wires
// that folder as it wires the agent's own (claudeIn and codexIn at a place
// whose folder is the one named), so the same agent can run from several
// such folders side by side, beside the user's own, which is never
// touched. Each instance's state in magpie's stash and applied.json is
// kept under an id of its own, made from its folder (<kind>#<hash>), so
// two folders never take each other's. For now an instance holds no
// subscription sign-in: it runs on magpie's models with magpie's key, and
// a folder holding a sign-in, or one of magpie's own, is refused (#992,
// option 1).

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/provider"
)

// Instance is Claude Code or Codex with a config folder of the user's.
type Instance struct {
	*Agent
	// Dir is its config folder, as the agent is told it (Var)
	Dir string
	// Var is the variable that tells the agent its folder
	Var string
}

// instanceVar is the variable each agent that can run as an instance
// takes its config folder from.
var instanceVar = map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME"}

// instanceSignIn is the file each keeps a sign-in in, in its folder.
var instanceSignIn = map[string]string{"claude": ".credentials.json", "codex": "auth.json"}

// NewInstance is the agent kind ("claude" or "codex") with dir, an
// absolute path, as its config folder. Nothing is written. Refused: a
// path that is a file; the user's own folder of the agent (where the
// agent goes without the variable, or where the variable in magpie's
// environment points) and a folder in it; the home folder, where Claude
// Code's .claude.json would be the user's own ~/.claude.json; a folder in
// magpie's own (a saved Claude account's is there); and a folder holding
// a sign-in.
func NewInstance(kind, dir string) (*Instance, error) {
	v, ok := instanceVar[kind]
	if !ok {
		return nil, fmt.Errorf("%q: only Claude Code (claude) and Codex (codex) run in a folder of their own", kind)
	}
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("%s is not an absolute path", dir)
	}
	dir = realPath(filepath.Clean(dir))
	switch st, err := os.Stat(dir); {
	case err == nil && !st.IsDir():
		return nil, fmt.Errorf("%s is a file, not a folder", dir)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	// its records under an id of its own, as a WSL twin's are: the same
	// agent from another folder never reads or writes this one's
	sum := sha256.Sum256([]byte(dir))
	at := place{home: home, dir: dir, id: kind + "#" + hex.EncodeToString(sum[:])[:16]}
	var a *Agent
	var own []string
	switch kind {
	case "claude":
		a = claudeIn(at)
		own = []string{filepath.Join(home, ".claude"), appdir.ClaudeDirIn(home)}
	case "codex":
		a = codexIn(at)
		own = []string{filepath.Join(home, ".codex"), appdir.CodexHomeIn(home)}
	}
	a.ID = at.id
	for _, o := range own {
		if inFolder(dir, realPath(o)) {
			return nil, fmt.Errorf("%s is %s's own folder, or in it: magpie run starts %s in a folder of its own beside it (magpie %s <model> sets the one you run as %s)", dir, a.Name, a.Name, kind, kind)
		}
	}
	if h := realPath(home); inFolder(dir, h) && inFolder(h, dir) {
		return nil, fmt.Errorf("%s is your home folder: give %s a folder of its own", dir, a.Name)
	}
	if inFolder(dir, realPath(appdir.Config())) {
		return nil, fmt.Errorf("%s is in magpie's own folder: give %s a folder of its own", dir, a.Name)
	}
	switch _, err := os.Lstat(filepath.Join(dir, instanceSignIn[kind])); {
	case err == nil:
		return nil, fmt.Errorf("%s holds a sign-in (%s): a folder magpie runs %s in holds none for now, it runs on magpie's models and key", dir, instanceSignIn[kind], a.Name)
	case !errors.Is(err, fs.ErrNotExist):
		// can't tell whether one is there: not taken for none
		return nil, err
	}
	return &Instance{Agent: a, Dir: dir, Var: v}, nil
}

// Use wires the instance's folder to magpie on model, one of magpie's
// models or routing groups the agent is shown, as `magpie <agent> <model>`
// sets the agent's own, and answers the model it is on. A model that isn't
// one of magpie's is refused, with nothing written. With none given, a
// folder magpie wired before is wired again on its model (the gateway's
// address may have moved since), and a new one is connected as the Agents
// page's switch connects the agent (ConnectHow). The folder is made
// (0700) only once the model is known.
func (in *Instance) Use(model string) (string, error) {
	a := in.Agent
	f := a.Field("model")
	switch {
	case model != "":
		v, err := a.Spell(f.Key, model)
		if err != nil {
			return "", err
		}
		vals := a.Values()
		vals[f.Key] = v
		if !magpieValue(a, *f, v, vals) {
			return "", fmt.Errorf("%s isn't one of magpie's models %s is shown: magpie models %s lists them", model, a.Name, a.ListsFor())
		}
		if err := os.MkdirAll(in.Dir, 0o700); err != nil {
			return "", err
		}
		if err := a.Pick(f.Key, v); err != nil {
			return "", err
		}
	case a.Wired():
		if err := os.MkdirAll(in.Dir, 0o700); err != nil {
			return "", err
		}
		if err := a.Pick(f.Key, f.Get()); err != nil {
			return "", err
		}
	default:
		if err := a.noModelLeft(); err != nil {
			return "", err
		}
		if err := os.MkdirAll(in.Dir, 0o700); err != nil {
			return "", err
		}
		if _, err := a.ConnectHow(); err != nil {
			return "", err
		}
	}
	if !a.Wired() {
		return "", fmt.Errorf("%s in %s isn't on magpie after wiring it: nothing was started", a.Name, in.Dir)
	}
	return f.Get(), nil
}

// Env is env for the instance: its folder in Var, and none of what would
// take it elsewhere. For Claude Code, the endpoint, key, sign-in and
// folder variables (provider.ClaudeRunEnv, as for a saved account's run),
// the model one, which outranks its settings, and the switches that send
// it to a cloud past magpie (agentenv.ClaudeProviders). For Codex, the
// folder its state database is kept in, which would be another's.
func (in *Instance) Env(env []string) []string {
	drop := []string{in.Var, "CODEX_SQLITE_HOME"}
	if in.Var == "CLAUDE_CONFIG_DIR" {
		drop = append(slices.Clone(agentenv.ClaudeProviders), "ANTHROPIC_MODEL")
	}
	out := slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(drop, k)
	})
	if in.Var == "CLAUDE_CONFIG_DIR" {
		return provider.ClaudeRunEnv(out, in.Dir)
	}
	return append(out, in.Var+"="+in.Dir)
}

// realPath is p with the links in it followed, as far as it exists: a
// folder not made yet is where it would be made.
func realPath(p string) string {
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// inFolder reports whether p is root or a folder in it, letters' case
// aside where the system's folders take either (macOS, Windows).
func inFolder(p, root string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		p, root = strings.ToLower(p), strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
