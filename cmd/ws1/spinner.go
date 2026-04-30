package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// realSpinner is the in-place progress indicator. Writes a tick line,
// overwritten on Done with a sigil + result. Goroutine cleans up
// deterministically when Done is called.
type realSpinner struct {
	label string
	out   io.Writer
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
}

func newSpinner(out io.Writer, label string) *realSpinner {
	s := &realSpinner{
		label: label,
		out:   out,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go s.run()
	return s
}

// run paints the spinner glyph + label until stop is closed. Uses
// `\r` to overwrite the same line every tick.
func (s *realSpinner) run() {
	defer close(s.done)
	glyphs := spinnerGlyphs()
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			fmt.Fprintf(s.out, "\r\x1b[K  %s %s", string(glyphs[i%len(glyphs)]), s.label)
			i++
		}
	}
}

// Done stops the spinner and writes the final state in place. Calling
// Done more than once is a no-op (sync.Once).
func (s *realSpinner) Done(ok bool, result string) {
	s.once.Do(func() {
		close(s.stop)
		<-s.done
		sigil := "✓"
		if !ok {
			sigil = "✗"
		}
		fmt.Fprintf(s.out, "\r\x1b[K  %s %s\n", sigil, result)
	})
}

// spinnerGlyphs picks the Braille frames when the locale supports
// UTF-8, otherwise the ASCII fallback. Sub-decision pinned in the
// plan: check LC_ALL, LC_CTYPE, LANG (in that order) for "UTF-8".
func spinnerGlyphs() []rune {
	if isUTF8Locale() {
		return []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	}
	return []rune(`|/-\`)
}

// isUTF8Locale reports whether the user's locale advertises UTF-8.
// Case-insensitive match for "UTF-8" or "utf-8" in any of the three
// locale env vars; first non-empty value wins.
func isUTF8Locale() bool {
	for _, env := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := os.Getenv(env)
		if v == "" {
			continue
		}
		up := strings.ToUpper(v)
		return strings.Contains(up, "UTF-8") || strings.Contains(up, "UTF8")
	}
	return false
}
