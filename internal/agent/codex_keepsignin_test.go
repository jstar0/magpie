package agent

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// #994 (jstar0, PennyVibe, MrDanielChen): magpie API with the ChatGPT
// sign-in kept. Codex stays on [model_providers.magpie] under the fixed id
// magpie, whose table also requires OpenAI auth, so Codex keeps its ChatGPT
// sign-in while magpie's key goes with every request. A switch of model, an
// account out of its allowance and magpie API without it keep the id; off
// again, magpie's requires_openai_auth goes.
func TestCodexKeepSignIn(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"chatgpt-token","id_token":"x.e30.x"}}`, "model = \"gpt-5.5\"\n")
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	login := cx.Field("login")
	table := func() map[string]string {
		t.Helper()
		tb, err := edit.GetTOMLTable(cx.Path, "model_providers."+magpieID)
		if err != nil || tb == nil {
			t.Fatalf("no magpie table (%v):\n%s", err, read())
		}
		return tb
	}
	onMagpie := func(when string, auth bool) {
		t.Helper()
		cfg := read()
		if p, _ := edit.GetTOMLTop(cx.Path, "model_provider"); p != magpieID {
			t.Fatalf("%s: model_provider %q, not magpie:\n%s", when, p, cfg)
		}
		tb := table()
		if (tb["requires_openai_auth"] == "true") != auth {
			t.Fatalf("%s: requires_openai_auth %q, want it %v:\n%s", when, tb["requires_openai_auth"], auth, cfg)
		}
		if tok := tb["experimental_bearer_token"]; tok == "" || tok == "chatgpt-token" || !keyHolds(tok, here(home).gwKey()) {
			t.Fatalf("%s: bearer token %q is not magpie's key:\n%s", when, tok, cfg)
		}
		if strings.Count(cfg, "requires_openai_auth") > 1 {
			t.Fatalf("%s: requires_openai_auth written twice:\n%s", when, cfg)
		}
		if c := cx.Check(); c != "" {
			t.Fatalf("%s: %s", when, c)
		}
	}

	if err := login.Set("api-chatgpt"); err != nil {
		t.Fatal(err)
	}
	if got := login.Get(); got != "api-chatgpt" {
		t.Fatalf("login %q", got)
	}
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	onMagpie("kept sign-in", true)
	if cx.Joined() || cx.Beside() {
		t.Fatal("kept sign-in: joined or beside the ChatGPT sign-in, not magpie as the provider")
	}

	if err := cx.Fields[0].Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	onMagpie("another model", true)

	// the ChatGPT account the Codex app holds changes nothing: magpie is
	// the provider already
	codexUsedUp = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	onMagpie("account held", true)
	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	onMagpie("account with room", true)

	// magpie API without it: the same table, the same id, no OpenAI auth
	if err := login.Set("api"); err != nil {
		t.Fatal(err)
	}
	onMagpie("magpie API", false)
	if err := login.Set("api-chatgpt"); err != nil {
		t.Fatal(err)
	}
	onMagpie("kept again", true)

	// back beside the sign-in: the table stays for the threads started on
	// magpie, without magpie's requires_openai_auth
	if err := login.Set(""); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "requires_openai_auth") || !strings.Contains(cfg, "[model_providers.magpie]") {
		t.Fatalf("back to ChatGPT:\n%s", cfg)
	}
	if v := stashLoad()[here(home).key("codex.openai_auth")]; v != "" {
		t.Fatalf("magpie's mark %q outlived the key", v)
	}
}

// requires_openai_auth = true the user wrote in magpie's table is theirs:
// a model switch on magpie API keeps it, and turning the kept sign-in on
// and off again leaves it, where magpie's own goes.
func TestCodexKeepSignInLeavesTheUsersOwn(t *testing.T) {
	const user = "model = \"fake/m1\"\nmodel_provider = \"magpie\"\n\n[model_providers.magpie]\nname = \"magpie\"\nbase_url = \"http://127.0.0.1:1/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = true\n"
	home, read := codexHome(t, `{"tokens":{"access_token":"chatgpt-token","id_token":"x.e30.x"}}`, user)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	login := cx.Field("login")
	theirs := func(when string) {
		t.Helper()
		tb, _ := edit.GetTOMLTable(cx.Path, "model_providers."+magpieID)
		if tb["requires_openai_auth"] != "true" {
			t.Fatalf("%s: the user's requires_openai_auth is gone:\n%s", when, read())
		}
	}
	if err := login.Set("api"); err != nil {
		t.Fatal(err)
	}
	theirs("magpie API")
	if err := cx.Fields[0].Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	theirs("model switch on magpie API")
	if err := login.Set("api-chatgpt"); err != nil {
		t.Fatal(err)
	}
	theirs("kept sign-in on")
	if err := login.Set("api"); err != nil {
		t.Fatal(err)
	}
	theirs("kept sign-in off")
	if err := login.Set(""); err != nil {
		t.Fatal(err)
	}
	theirs("back to ChatGPT")

	// a false the user wrote is Codex's default: on writes true as
	// magpie's, off takes it out again
	if err := edit.SetTOMLKey(cx.Path, "model_providers."+magpieID, "requires_openai_auth", false); err != nil {
		t.Fatal(err)
	}
	if err := login.Set("api-chatgpt"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	theirs("on over the user's false")
	if err := login.Set("api"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), "requires_openai_auth") {
		t.Fatalf("off: magpie's true stayed:\n%s", read())
	}
}

// The kept sign-in's requires_openai_auth taken out of the table by hand
// (or by another tool) is drift, which Check names and Reconnect writes
// back. Turned off while magpie is no longer Codex's provider, the table
// kept for the threads loses magpie's requires_openai_auth too.
func TestCodexKeepSignInDrift(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"chatgpt-token","id_token":"x.e30.x"}}`, "model = \"gpt-5.5\"\n")
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	if err := cx.Apply("login", "api-chatgpt"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := edit.DelTOMLKey(cx.Path, "model_providers."+magpieID, "requires_openai_auth"); err != nil {
		t.Fatal(err)
	}
	if c := cx.Check(); !strings.Contains(c, "requires_openai_auth") {
		t.Fatalf("taken out by hand: Check %q\n%s", c, read())
	}
	if err := cx.Reapply(); err != nil {
		t.Fatal(err)
	}
	if c := cx.Check(); c != "" {
		t.Fatalf("after Reconnect: %s\n%s", c, read())
	}
	if !strings.Contains(read(), "requires_openai_auth = true") {
		t.Fatalf("Reconnect didn't write it back:\n%s", read())
	}

	// Disconnect: magpie's provider goes, the table stays for the threads,
	// and the sign-in Disconnect puts back (ChatGPT) takes magpie's
	// requires_openai_auth out of it
	if err := cx.Disconnect(); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if strings.Contains(cfg, "model_provider =") || !strings.Contains(cfg, "[model_providers.magpie]") {
		t.Fatalf("disconnected:\n%s", cfg)
	}
	if got := cx.Field("login").Get(); got != "" || strings.Contains(cfg, "requires_openai_auth") {
		t.Fatalf("disconnected: sign-in %q, magpie's requires_openai_auth should be gone:\n%s", got, cfg)
	}
}

// A config magpie can't parse is written to by nothing as the field
// changes (a failed parse is unknown, not "no requires_openai_auth").
func TestCodexKeepSignInUnparsedConfig(t *testing.T) {
	const broken = "model = \"fake/m1\"\nmodel_provider = \"magpie\"\n[model_providers.magpie\nname = \"magpie\"\n"
	home, read := codexHome(t, `{"tokens":{"access_token":"chatgpt-token","id_token":"x.e30.x"}}`, broken)
	cx := codex(home)
	for _, v := range []string{"api-chatgpt", "api", ""} {
		cx.Field("login").Set(v)
		if read() != broken {
			t.Fatalf("sign-in %q wrote to an unparsed config:\n%s", v, read())
		}
	}
}
