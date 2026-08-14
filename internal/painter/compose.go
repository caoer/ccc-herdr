package painter

import (
	"strings"
	"time"

	"github.com/caoer/ccc-herdr/internal/herdr"
)

// Sources: identity owns tokens + display_agent; AUQ owns
// state_labels{blocked} alone. herdr metadata is per-source, so the two
// writers cannot stomp each other. Same source names statusd used — herdr-
// side ownership transfers without any herdr change.
const (
	SourceIdentity = "ccc:identity"
	SourceAUQ      = "ccc:auq"
)

// ComposeIdentity renders the config into one wire report. Interpolation
// failures degrade per value — a bad template drops that token/param with a
// diag, the rest still paints (display-only, never load-bearing). ok is false
// when nothing remains to say (herdr rejects an empty report).
func ComposeIdentity(paneID string, cfg Config, vars map[string]string, diag Diag) (herdr.Report, bool) {
	if diag == nil {
		diag = func(string, ...any) {}
	}
	report := herdr.Report{
		PaneID: paneID,
		Source: SourceIdentity,
		Agent:  cfg.Agent,
		Method: cfg.Method,
		TTLms:  cfg.TTL.Milliseconds(),
		// Seq at DECISION time, not send time: a send stalled in dial must not
		// deliver older content with a newer seq. ContentHash excludes it.
		Seq: time.Now().UnixNano(),
	}

	// Star config: render(v) owns every per-pane surface, including the
	// empty-HERDR_TITLE omission the template path special-cases below.
	if cfg.Star != nil {
		displayAgent, tokens, extra, renderOK := cfg.Star.Render(vars, diag)
		if !renderOK {
			return herdr.Report{}, false
		}
		report.Tokens = tokens
		report.Extra = extra
		if displayAgent != "" {
			report.DisplayAgent = herdr.ClampValue(displayAgent)
		}
		if report.Tokens == nil && report.Extra == nil && report.DisplayAgent == "" {
			return herdr.Report{}, false
		}
		return report, true
	}

	// A template referencing {{HERDR_TITLE}} while the var is empty is omitted
	// WHOLE — expanding it would paint a dangling "name · " or null the token;
	// omission keeps herdr's previous value (metadata patch semantics) until a
	// title exists.
	emptyTitle := strings.TrimSpace(vars["HERDR_TITLE"]) == ""
	skipForEmptyTitle := func(tmpl string) bool {
		return emptyTitle && strings.Contains(tmpl, "{{HERDR_TITLE}}")
	}

	tokens := map[string]any{}
	for name, tmpl := range cfg.Tokens {
		if skipForEmptyTitle(tmpl) {
			continue
		}
		val, err := Expand(tmpl, vars)
		if err != nil {
			diag("[painter] tokens.%s: %v — token dropped", name, err)
			continue
		}
		tokens[name] = tokenValue(val)
	}
	if len(tokens) > 0 {
		report.Tokens = tokens
	}

	if cfg.DisplayAgent != "" && !skipForEmptyTitle(cfg.DisplayAgent) {
		val, err := Expand(cfg.DisplayAgent, vars)
		if err != nil {
			diag("[painter] display_agent: %v — field dropped", err)
		} else {
			report.DisplayAgent = herdr.ClampValue(val)
		}
	}

	if len(cfg.Params) > 0 {
		extra := map[string]any{}
		for key, v := range cfg.Params {
			s, isStr := v.(string)
			if !isStr {
				extra[key] = v // non-string leaves ride verbatim
				continue
			}
			if skipForEmptyTitle(s) {
				continue
			}
			val, err := Expand(s, vars)
			if err != nil {
				diag("[painter] params.%s: %v — param dropped", key, err)
				continue
			}
			if strings.TrimSpace(val) == "" {
				continue // an empty presentation field is an omission, not a clear
			}
			extra[key] = herdr.ClampValue(val)
		}
		if len(extra) > 0 {
			report.Extra = extra
		}
	}

	if report.Tokens == nil && report.Extra == nil && report.DisplayAgent == "" {
		return herdr.Report{}, false
	}
	return report, true
}

// ComposeAUQ renders the blocked-label report: a LEASE while questions pend
// (the sweep renews it, so a canceled question TTLs out without a clear
// path), an explicit clear on the pending→0 edge.
func ComposeAUQ(paneID string, cfg Config, pending int) herdr.Report {
	report := herdr.Report{
		PaneID: paneID,
		Source: SourceAUQ,
		Agent:  cfg.Agent,
		Method: cfg.Method,
		Seq:    time.Now().UnixNano(),
	}
	if pending > 0 {
		report.StateLabels = map[string]string{"blocked": herdr.ClampValue(cfg.AUQLabel)}
		report.TTLms = cfg.AUQLease.Milliseconds()
	} else {
		report.ClearStateLabels = true
	}
	return report
}

// tokenValue maps an interpolated value onto herdr's clear convention:
// empty → JSON null (explicit clear — the row shortens by itself).
func tokenValue(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return herdr.ClampValue(v)
}
