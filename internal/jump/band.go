package jump

import (
	"time"

	lipgloss "charm.land/lipgloss/v2"
)

// Band is a pane's prompt-cache state, the same classes and colors as the
// P5 desk LED (desktop-displays p5/renderer/render_p5.py): blocked on an
// AskUserQuestion, working, then idle by minutes since last activity until
// the ~1h prompt cache expires. Order is the LED's left-to-right order.
type Band int

const (
	BandBlocked Band = iota
	BandWorking
	BandIdle10 // idle < 10m
	BandIdle30 // idle < 30m
	BandIdle40 // idle < 40m
	BandIdle60 // idle < 60m, cache about to expire
	BandExpired
	BandNone // plain pane, or an agent with no activity clock
	bandCount

	// AnyBand is Filter's no-band-filter value.
	AnyBand Band = -1
)

// cacheTTL is the prompt-cache lifetime the idle bands count down to.
const cacheTTL = time.Hour

var bandLimits = [...]time.Duration{10 * time.Minute, 30 * time.Minute, 40 * time.Minute, cacheTTL}

// bandMeta: LED RGB, except expired — the LED's dark grey (80,80,80) is
// lifted to a legible neutral grey for terminal text. Red is reserved for
// the last 20 minutes before the one-hour cache threshold.
var bandMeta = [bandCount]struct {
	name  string
	color string
}{
	BandBlocked: {"AUQ", "#00DCFF"},
	BandWorking: {"working", "#00C83C"},
	BandIdle10:  {"<10m", "#9B5AE6"},
	BandIdle30:  {"<30m", "#00B89C"},
	BandIdle40:  {"<40m", "#E6C84A"},
	BandIdle60:  {"<60m", "#FF0028"},
	BandExpired: {"expired", "#888888"},
	BandNone:    {"other", "#808080"},
}

func (b Band) Name() string { return bandMeta[b].name }

func (b Band) Style() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(bandMeta[b].color))
}

// classify mirrors render_p5.band: blocked beats working beats age.
func classify(blocked, working bool, age time.Duration, known bool) Band {
	switch {
	case blocked:
		return BandBlocked
	case working:
		return BandWorking
	case !known:
		return BandNone
	}
	for i, limit := range bandLimits {
		if age < limit {
			return BandIdle10 + Band(i)
		}
	}
	return BandExpired
}
