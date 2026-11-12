package cli

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
)

// errNoAnswer means the question could not be answered at all: there is no input, or it has ended.
// It is different from "no", which is a real answer.
var errNoAnswer = errors.New("no answer could be read")

// confirmUninstall asks once, "Go ahead?", and reports whether the user said yes. It returns errNoAnswer when
// no input can be read at all, such as with stdin at end of file (for example /dev/null).
func confirmUninstall(a *App) (bool, error) {
	if a.In == nil {
		return false, errNoAnswer
	}
	_, _ = fmt.Fprint(a.Out, "Go ahead? [y/N] ")
	if a.inReader == nil || a.inSource != a.In { // one reader per input, as App.confirm does
		a.inReader, a.inSource = bufio.NewReader(a.In), a.In
	}
	line, err := a.inReader.ReadString('\n')
	if err != nil && line == "" {
		_, _ = fmt.Fprintln(a.Out)
		return false, errNoAnswer
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
