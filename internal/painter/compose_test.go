package painter

import (
	"strings"
	"testing"
)

func identityVars() map[string]string {
	return map[string]string{
		"SESSION_ID_SHORT":   "ad3009b4",
		"CCC_SESSION":        "02-00-adhoc",
		"ROLE":               "worker",
		"NAME":               "reader",
		"TITLE":              "reader",
		"PROFILE":            "models-turnip",
		"PROFILE_IF_UNNAMED": "",
		"HERDR_TITLE":        "",
	}
}

func TestComposeIdentityDefaults(t *testing.T) {
	report, ok := ComposeIdentity("w1:p1", DefaultConfig(), identityVars(), nil)
	if !ok {
		t.Fatal("default config must compose")
	}
	if report.Source != SourceIdentity {
		t.Fatalf("source: %s", report.Source)
	}
	if report.Tokens["id"] != "ad3009b4" || report.Tokens["role"] != "worker" {
		t.Fatalf("tokens: %v", report.Tokens)
	}
	if report.DisplayAgent != "models-turnip" {
		t.Fatalf("display_agent: %s", report.DisplayAgent)
	}
}

func TestComposeIdentityEmptyVarClearsToken(t *testing.T) {
	vars := identityVars()
	vars["ROLE"] = ""
	report, _ := ComposeIdentity("w1:p1", DefaultConfig(), vars, nil)
	if v, present := report.Tokens["role"]; !present || v != nil {
		t.Fatalf("empty var must clear the token (nil), got %v present=%v", v, present)
	}
}

func TestComposeIdentityEmptyTitleOmitsWholeTemplate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Params = map[string]any{"title": "{{NAME}}|{{HERDR_TITLE}}"}
	report, _ := ComposeIdentity("w1:p1", cfg, identityVars(), nil)
	if _, present := report.Extra["title"]; present {
		t.Fatal("param referencing empty HERDR_TITLE must be omitted whole")
	}

	vars := identityVars()
	vars["HERDR_TITLE"] = "doing things"
	report, _ = ComposeIdentity("w1:p1", cfg, vars, nil)
	if report.Extra["title"] != "reader|doing things" {
		t.Fatalf("param: %v", report.Extra)
	}
}

func TestComposeIdentityHashStableAcrossSeq(t *testing.T) {
	a, _ := ComposeIdentity("w1:p1", DefaultConfig(), identityVars(), nil)
	b, _ := ComposeIdentity("w1:p1", DefaultConfig(), identityVars(), nil)
	if a.ContentHash() != b.ContentHash() {
		t.Fatal("two composes of identical content must hash equal (seq excluded)")
	}
}

func TestComposeIdentityBadTemplateDropsValueOnly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tokens = map[string]string{"id": "{{SESSION_ID_SHORT}}", "bad": "{{NOPE}}"}
	var diags []string
	report, ok := ComposeIdentity("w1:p1", cfg, identityVars(), func(format string, args ...any) {
		diags = append(diags, format)
	})
	if !ok || report.Tokens["id"] != "ad3009b4" {
		t.Fatal("good tokens must survive a bad sibling")
	}
	if _, present := report.Tokens["bad"]; present {
		t.Fatal("bad template must drop its token")
	}
	if len(diags) != 1 || !strings.Contains(diags[0], "token dropped") {
		t.Fatalf("diags: %v", diags)
	}
}

func TestComposeAUQ(t *testing.T) {
	cfg := DefaultConfig()
	pendingReport := ComposeAUQ("w1:p1", cfg, 2)
	if pendingReport.StateLabels["blocked"] != "AUQ" || pendingReport.TTLms == 0 {
		t.Fatalf("pending: %+v", pendingReport)
	}
	clear := ComposeAUQ("w1:p1", cfg, 0)
	if !clear.ClearStateLabels || clear.StateLabels != nil {
		t.Fatalf("clear: %+v", clear)
	}
}
