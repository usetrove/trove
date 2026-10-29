package cli

import (
	"os"
	"strconv"

	"golang.org/x/term"
)

// termWidth is the width of the terminal output goes to. COLUMNS wins if set;
// otherwise the real terminal is asked. Output that is not a terminal (a pipe, a
// file, a test) gets 80 columns so saved reports look the same everywhere.
func (a *App) termWidth() int {
	if n, err := strconv.Atoi(a.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	if f, ok := a.Out.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 { //nolint:gosec // file descriptors fit in int
			return w
		}
	}
	return 80
}
