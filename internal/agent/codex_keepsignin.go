package agent

// Codex's sign-in "magpie API + ChatGPT sign-in" (#994): magpie is Codex's
// provider as with magpie API, under the same fixed id, and its
// [model_providers.magpie] also says requires_openai_auth = true. Codex then
// keeps its ChatGPT sign-in: its app-server's getAuthStatus answers with the
// ChatGPT sign-in (the Codex app's signed-in state) where without the key it
// answers none. Every model request still carries the table's
// experimental_bearer_token, magpie's key, never the ChatGPT token, so none
// is spent on the account's allowance. Codex 0.163.0-alpha.11.1 checked so,
// against a fake gateway: it then offers its image tool only while it is
// signed in to ChatGPT, where without the key it offers it always.

import "github.com/yetone/magpie/internal/edit"

// codexAPIKeepSignIn is the sign-in field's value for magpie API with the
// ChatGPT sign-in kept.
const codexAPIKeepSignIn = "api-chatgpt"

// codexKeepSignInNote is that option's note, its cost included.
const codexKeepSignInNote = "as magpie API, but Codex keeps its ChatGPT sign-in: the Codex app shows it signed in, and every request still goes to magpie on magpie's key, none on the account's allowance. Codex then offers its image tool only while signed in to ChatGPT: signed out, it can't draw through magpie"

// codexAPILogin reports whether a sign-in value makes magpie Codex's
// provider whatever Codex is signed in to: magpie API, with the ChatGPT
// sign-in kept or not.
func codexAPILogin(v string) bool { return v == "api" || v == codexAPIKeepSignIn }

// codexOpenAIAuthSync puts requires_openai_auth = true into magpie's
// provider table at path while on, and takes the one magpie wrote out again
// when off. key is the stash's mark of magpie's own. A true the user wrote
// is theirs: it never counts as magpie's, so it stays when off. A false the
// user wrote is Codex's default, and on writes true over it as magpie's. One
// changed or taken out since magpie wrote it is no longer marked: off leaves
// it as it is, on writes true again, as the field asks. A config that
// doesn't parse is told by Check, and written to by nothing.
func codexOpenAIAuthSync(path, key string, on bool) error {
	const table = "model_providers." + magpieID
	t, err := edit.GetTOMLTable(path, table)
	if err != nil {
		return nil
	}
	mine := stashLoad()[key] == "1"
	if t == nil {
		if mine {
			forget(key)
		}
		return nil
	}
	v := t["requires_openai_auth"]
	if mine && v != "true" {
		forget(key)
		mine = false
	}
	switch {
	case on && v != "true":
		if err := edit.SetTOMLKey(path, table, "requires_openai_auth", true); err != nil {
			return err
		}
		stash(map[string]string{key: "1"})
	case !on && mine:
		if err := edit.DelTOMLKey(path, table, "requires_openai_auth"); err != nil {
			return err
		}
		forget(key)
	}
	return nil
}
