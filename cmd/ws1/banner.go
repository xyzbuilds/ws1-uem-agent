package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/xyzbuilds/ws1-uem-agent/internal/auth"
	"github.com/xyzbuilds/ws1-uem-agent/internal/version"
)

// Brand color: teal #5dd3c8. 24-bit truecolor escape — supported by
// every modern terminal we care about (Terminal.app, iTerm2, kitty,
// alacritty, Windows Terminal, VS Code, etc.). Terminals without
// truecolor will render a close approximation; the layout still works.
const colorTeal = "\x1b[38;2;93;211;200m"

const (
	colorReset = "\x1b[0m"
	colorBold  = "\x1b[1m"
	colorDim   = "\x1b[2m"
	colorWarn  = "\x1b[33m" // yellow
	colorGreen = "\x1b[32m"
	colorRed   = "\x1b[31m"
)

// mascot rendered top-to-bottom. Four lines, ~7 cols wide. The
// trailing single-space on each line keeps consistent column width.
var mascotUTF8 = []string{
	"▄▀▀▀▀▀▄",
	"█ ●_● █",
	"▀█▄▄▄█▀",
	" ▘   ▝ ",
}

// ASCII fallback when the locale isn't UTF-8. Approximates the same
// face shape using only 7-bit characters.
var mascotASCII = []string{
	" /---\\ ",
	"| o_o |",
	" \\___/ ",
	"  ' '  ",
}

// stderrIsTTY reports whether os.Stderr is a real terminal. Used to
// gate color and animation. Tests with go test typically don't have a
// TTY on stderr, so this returns false there.
func stderrIsTTY() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// teal wraps s in the brand truecolor when stderr is a TTY; plain
// text otherwise.
func teal(s string) string {
	if !stderrIsTTY() {
		return s
	}
	return colorTeal + s + colorReset
}

func bold(s string) string {
	if !stderrIsTTY() {
		return s
	}
	return colorBold + s + colorReset
}

func dim(s string) string {
	if !stderrIsTTY() {
		return s
	}
	return colorDim + s + colorReset
}

func warn(s string) string {
	if !stderrIsTTY() {
		return s
	}
	return colorWarn + s + colorReset
}

func green(s string) string {
	if !stderrIsTTY() {
		return s
	}
	return colorGreen + s + colorReset
}

func red(s string) string {
	if !stderrIsTTY() {
		return s
	}
	return colorRed + s + colorReset
}

// info is the brand "blue" — used for the write class. Matches the
// mockup's --info (#79c0ff). 24-bit truecolor; degrades on terminals
// without truecolor support like the other helpers.
func info(s string) string {
	if !stderrIsTTY() {
		return s
	}
	return "\x1b[38;2;121;192;255m" + s + colorReset
}

// code wraps s in info-blue — used for inline command names and
// flag examples in helper text. Visually distinct from prose so the
// reader's eye lands on what to actually type.
func code(s string) string {
	return info(s)
}

// example wraps s in info-blue (same as code) — semantic alias for
// concrete-value examples like "as1784.awmdm.com" inside helper
// text. Keeping the alias lets us re-skin examples without touching
// every call site.
func example(s string) string {
	return info(s)
}

// colorByClass returns s wrapped in the canonical class color:
// read → green, write → info-blue, destructive → red. Anything else
// passes through unchanged. Use this everywhere the class is shown
// so the visual mapping stays consistent across ops list, doctor,
// pre-flight summaries, and errors.
func colorByClass(class, s string) string {
	switch class {
	case "read":
		return green(s)
	case "write":
		return info(s)
	case "destructive":
		return red(s)
	default:
		return s
	}
}

// printBanner renders the two-column mascot + info layout to stderr.
// The mascot column is fixed-width (~7 cols + 2-space gutter); each
// info line follows on the corresponding row. If info has fewer rows
// than the mascot, blank tails appear; if more, they continue under
// the mascot as plain unindented lines.
func printBanner(infoLines []string) {
	mascot := mascotUTF8
	if !isUTF8Locale() {
		mascot = mascotASCII
	}
	fmt.Fprintln(stderrWriter)
	rows := len(mascot)
	if len(infoLines) > rows {
		rows = len(infoLines)
	}
	for i := 0; i < rows; i++ {
		var mLine, iLine string
		if i < len(mascot) {
			mLine = teal(mascot[i])
		} else {
			mLine = strings.Repeat(" ", 7)
		}
		if i < len(infoLines) {
			iLine = infoLines[i]
		}
		if iLine != "" {
			fmt.Fprintf(stderrWriter, "  %s  %s\n", mLine, iLine)
		} else {
			fmt.Fprintf(stderrWriter, "  %s\n", mLine)
		}
	}
}

// showBareWS1Greeter renders the banner with state-aware info + a
// heads-up line. Fires when the user runs `ws1` with no subcommand.
// Pure UX surface — no JSON envelope, no exit code drama.
func showBareWS1Greeter() {
	info := []string{
		bold("ws1") + " " + dim("v"+version.Version),
	}

	profiles, _ := auth.LoadProfiles()
	if len(profiles) == 0 {
		// Pre-config greeter.
		info = append(info, dim("Workspace ONE UEM agent · agent-first CLI"))
		printBanner(info)
		fmt.Fprintln(stderrWriter)
		fmt.Fprintf(stderrWriter, "%s  No configuration found. Run %s to set up your tenant.\n",
			warn("⚠"), bold("ws1 setup"))
		return
	}

	active, _ := auth.Active()
	og, _ := auth.CurrentOG()
	tenant := ""
	for _, p := range profiles {
		if p.Name == active {
			tenant = p.Tenant
			break
		}
	}

	info = append(info, dim(fmt.Sprintf("Workspace ONE UEM agent · %d profile(s) configured", len(profiles))))
	if tenant != "" {
		profileColor := green(active)
		if active == "operator" || active == "admin" {
			profileColor = warn(active)
		}
		ogStr := og
		if og == "" {
			ogStr = "(none)"
		}
		info = append(info, dim(tenant+" · profile ")+profileColor+dim(" · OG "+ogStr))
	}
	printBanner(info)

	fmt.Fprintln(stderrWriter)
	switch active {
	case "ro":
		fmt.Fprintf(stderrWriter, "%s  Profile is %s. Switch with %s.\n",
			warn("⚠"), bold("read-only"), bold("ws1 profile use operator"))
	case "operator", "admin":
		fmt.Fprintf(stderrWriter, "%s  Profile is %s (write-capable). Switch back with %s when done.\n",
			warn("⚠"), bold(active), bold("ws1 profile use ro"))
	}
}

// printSetupBanner renders the banner with a setup-flow tagline.
// Reused by RunSetup at the start of an interactive run.
func printSetupBanner() {
	if !auth.IsInteractive() {
		return
	}
	printBanner([]string{
		bold("ws1") + " " + dim("v"+version.Version),
		dim("Welcome — let's set up your tenant."),
	})
}
