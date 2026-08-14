package painter

// Starlark config: ccc-herdr.star replaces the TOML table with a small
// program. Static settings stay declarative ([painter]/[auq] become module
// dicts, resolved by the SAME resolver as TOML so diagnostics never fork);
// the per-pane values move from {{VAR}} templates into one render(v)
// function, which is where conditionals live (per-role tokens, title
// omission). TOML files keep loading unchanged — the format is chosen by
// file extension, so fleet hosts migrate one at a time.

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"

	"github.com/caoer/ccc-herdr/internal/herdr"
)

const (
	// execMaxSteps bounds module init; renderMaxSteps bounds one render call.
	// The painter is a resident daemon — a pathological config must degrade
	// loudly, never wedge the paint loop. Real configs use a few hundred steps.
	execMaxSteps   = 4_000_000
	renderMaxSteps = 1_000_000
)

// StarProgram is a loaded ccc-herdr.star: the frozen render function. Frozen
// values are safe to call from concurrent repaint goroutines.
type StarProgram struct {
	path   string
	render starlark.Callable
}

// loadStarConfig evaluates the module and resolves statics. Any failure that
// leaves no usable render degrades to DefaultConfig LOUDLY — same contract as
// an unparsable TOML: an unreadable config must never dark a fleet's labels.
func loadStarConfig(path string, data []byte, diag Diag) Config {
	thread := &starlark.Thread{Name: "ccc-herdr.star"}
	thread.SetMaxExecutionSteps(execMaxSteps)
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, path, data, nil)
	if err != nil {
		diag("config %s: %v — using defaults", path, starErr(err))
		return DefaultConfig()
	}
	globals.Freeze() // render is called from concurrent repaints

	renderVal, ok := globals["render"]
	if !ok {
		diag("config %s: no render(v) function defined — using defaults", path)
		return DefaultConfig()
	}
	render, ok := renderVal.(starlark.Callable)
	if !ok {
		diag("config %s: render must be a function, got %s — using defaults", path, renderVal.Type())
		return DefaultConfig()
	}

	// Statics ride the TOML resolver: identical keys, identical diagnostics.
	raw := map[string]any{}
	for _, table := range []string{"painter", "auq"} {
		v, present := globals[table]
		if !present {
			continue
		}
		raw[table] = goValue(v, diag)
	}
	// The per-pane surfaces belong to render(v) in a .star config; a static
	// leftover would silently shadow it, so it is stripped with a diagnostic.
	if pm, isTable := raw["painter"].(map[string]any); isTable {
		for _, key := range []string{"tokens", "params", "display_agent"} {
			if _, has := pm[key]; has {
				diag("[painter] %s: owned by render(v) in a .star config — ignored", key)
				delete(pm, key)
			}
		}
	}
	cfg := resolveConfig(raw, diag)
	cfg.Tokens, cfg.Params, cfg.DisplayAgent = nil, nil, ""
	cfg.Star = &StarProgram{path: path, render: render}
	return cfg
}

// Render computes one pane's identity surfaces. ok=false (with a diag) means
// the render call itself failed — the identity report is skipped for this
// paint; AUQ and other sessions are unaffected.
func (sp *StarProgram) Render(vars map[string]string, diag Diag) (displayAgent string, tokens map[string]any, extra map[string]any, ok bool) {
	if diag == nil {
		diag = func(string, ...any) {}
	}
	dict := starlark.StringDict{}
	for k, v := range vars {
		dict[k] = starlark.String(v)
	}
	thread := &starlark.Thread{Name: "render"}
	thread.SetMaxExecutionSteps(renderMaxSteps)
	res, err := starlark.Call(thread, sp.render, starlark.Tuple{starlarkstruct.FromStringDict(starlark.String("vars"), dict)}, nil)
	if err != nil {
		diag("[render]: %v — identity report skipped", starErr(err))
		return "", nil, nil, false
	}
	result, isDict := res.(*starlark.Dict)
	if !isDict {
		diag("[render]: must return a dict, got %s — identity report skipped", res.Type())
		return "", nil, nil, false
	}

	for _, item := range result.Items() {
		key, isStr := starlark.AsString(item[0])
		if !isStr {
			diag("[render]: result keys must be strings, got %s — entry dropped", item[0].Type())
			continue
		}
		switch key {
		case "display_agent":
			if s, isString := starlark.AsString(item[1]); isString {
				displayAgent = s
			} else if item[1] != starlark.None {
				diag("[render] display_agent: must be a string, got %s — field dropped", item[1].Type())
			}
		case "tokens":
			tokens = renderTokens(item[1], diag)
		case "params":
			extra = renderParams(item[1], diag)
		default:
			diag("[render] %s: unknown key — ignored (known: display_agent, tokens, params)", key)
		}
	}
	return displayAgent, tokens, extra, true
}

// renderTokens maps the tokens dict onto herdr's clear convention: "" or
// None → JSON null (explicit clear); an OMITTED key keeps herdr's stored
// value until the lease expires — clearing is a statement, not a default.
func renderTokens(v starlark.Value, diag Diag) map[string]any {
	d, isDict := v.(*starlark.Dict)
	if !isDict {
		diag("[render] tokens: must be a dict, got %s — dropped", v.Type())
		return nil
	}
	tokens := map[string]any{}
	for _, item := range d.Items() {
		name, isStr := starlark.AsString(item[0])
		if !isStr {
			diag("[render] tokens: keys must be strings, got %s — token dropped", item[0].Type())
			continue
		}
		if item[1] == starlark.None {
			tokens[name] = nil
			continue
		}
		s, isString := starlark.AsString(item[1])
		if !isString {
			diag("[render] tokens.%s: value must be a string or None, got %s — token dropped", name, item[1].Type())
			continue
		}
		tokens[name] = tokenValue(s)
	}
	if len(tokens) == 0 {
		return nil
	}
	return tokens
}

// renderParams mirrors the TOML params convention: an empty or None value is
// an omission (herdr keeps its previous value), non-string scalars ride
// verbatim.
func renderParams(v starlark.Value, diag Diag) map[string]any {
	d, isDict := v.(*starlark.Dict)
	if !isDict {
		diag("[render] params: must be a dict, got %s — dropped", v.Type())
		return nil
	}
	extra := map[string]any{}
	for _, item := range d.Items() {
		key, isStr := starlark.AsString(item[0])
		if !isStr {
			diag("[render] params: keys must be strings, got %s — param dropped", item[0].Type())
			continue
		}
		if item[1] == starlark.None {
			continue
		}
		if s, isString := starlark.AsString(item[1]); isString {
			if strings.TrimSpace(s) == "" {
				continue
			}
			extra[key] = herdr.ClampValue(s)
			continue
		}
		extra[key] = goValue(item[1], diag)
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

// goValue converts a Starlark value into the map[string]any vocabulary the
// TOML resolver already speaks. Unconvertible values become nil with a diag.
func goValue(v starlark.Value, diag Diag) any {
	switch val := v.(type) {
	case starlark.String:
		return string(val)
	case starlark.Bool:
		return bool(val)
	case starlark.Int:
		if i, exact := val.Int64(); exact {
			return i
		}
		return val.String()
	case starlark.Float:
		return float64(val)
	case starlark.NoneType:
		return nil
	case *starlark.Dict:
		m := map[string]any{}
		for _, item := range val.Items() {
			key, isStr := starlark.AsString(item[0])
			if !isStr {
				diag("config: dict keys must be strings, got %s — entry dropped", item[0].Type())
				continue
			}
			m[key] = goValue(item[1], diag)
		}
		return m
	case *starlark.List:
		out := make([]any, 0, val.Len())
		for i := 0; i < val.Len(); i++ {
			out = append(out, goValue(val.Index(i), diag))
		}
		return out
	case starlark.Tuple:
		out := make([]any, 0, len(val))
		for _, item := range val {
			out = append(out, goValue(item, diag))
		}
		return out
	default:
		diag("config: unsupported value type %s — treated as unset", v.Type())
		return nil
	}
}

// starErr flattens a Starlark error to its call-stack summary — the multiline
// backtrace form would spread one diagnostic over many log lines.
func starErr(err error) string {
	if evalErr, isEval := err.(*starlark.EvalError); isEval {
		return fmt.Sprintf("%s (%s)", evalErr.Error(), strings.TrimSpace(lastLine(evalErr.Backtrace())))
	}
	return err.Error()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
