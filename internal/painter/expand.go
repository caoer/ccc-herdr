package painter

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUndefinedVar is the fail-loud closed-set violation.
var ErrUndefinedVar = errors.New("undefined var")

// Expand interpolates {{VAR}} tokens in a single pass. Ported from statusd's
// registry engine, vars-only. The load-bearing invariant: SUBSTITUTE, DON'T
// RE-SCAN THE SUBSTITUTED TEXT — a var value containing "{{…}}" is written
// verbatim, never expanded a second time.
func Expand(body string, vars map[string]string) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(body) {
		open := strings.Index(body[i:], "{{")
		if open < 0 {
			b.WriteString(body[i:])
			return b.String(), nil
		}
		open += i
		b.WriteString(body[i:open])
		rel := strings.Index(body[open+2:], "}}")
		if rel < 0 {
			// Unterminated {{ — emit the remainder verbatim, never expand.
			b.WriteString(body[open:])
			return b.String(), nil
		}
		end := open + 2 + rel
		tok := body[open+2 : end]
		i = end + 2 // advance past }} in the SOURCE — substitutions are never re-scanned

		val, ok := vars[tok]
		if !ok {
			return "", fmt.Errorf("%w: %q", ErrUndefinedVar, tok)
		}
		b.WriteString(val)
	}
	return b.String(), nil
}
