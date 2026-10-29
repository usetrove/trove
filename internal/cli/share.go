package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/report/share"
)

type shareOutput struct {
	Card  *share.Card `json:"card,omitempty"`
	Saved bool        `json:"saved"`
	SVG   string      `json:"svg,omitempty"`
	PNG   string      `json:"png,omitempty"`
}

func newShare(a *App) *cobra.Command {
	var pf periodFlags
	var theme, out string
	var yes bool
	c := &cobra.Command{
		Use:   "share",
		Short: "Create a shareable summary image (SVG and PNG)",
		Long: "Draws a card with your headline numbers: spend, waste found, savings, the biggest leak and the period.\n" +
			"It shows exactly what the card says and asks before saving. The card has no repository, file, prompt,\n" +
			"agent or model names, and Trove never uploads it: you post it yourself if you want to.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runShare(a, pf, theme, out, yes) },
	}
	pf.add(c)
	c.Flags().StringVar(&theme, "theme", string(share.Dark), "dark or light (the card paints its own background either way)")
	c.Flags().StringVar(&out, "out", "", "where to save: a folder, or a file name without extension (default: ./trove-share)")
	c.Flags().BoolVar(&yes, "yes", false, "save without asking")
	return c
}

func runShare(a *App, pf periodFlags, theme, out string, yes bool) error {
	th := share.Theme(theme)
	if th != share.Dark && th != share.Light {
		return fmt.Errorf("--theme is %q; use dark or light", theme)
	}
	r, err := buildReport(a, pf)
	if err != nil {
		return err
	}
	card, ok := share.FromReport(r)
	if !ok {
		return a.emit(shareOutput{}, "Nothing to share yet: no usage has been recorded "+r.Period.Label+". Use your agent through `trove gateway`, then try again.")
	}
	base, err := shareBase(out)
	if err != nil {
		return err
	}
	svgPath, pngPath := base+".svg", base+".png"

	var b strings.Builder
	fmt.Fprintf(&b, "Share card preview (%s theme, %d x %d). This is everything the card says:\n\n", th, share.Width, share.Height)
	for _, l := range card.Lines() {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString("\nOnly these numbers and labels appear: no repository, file, prompt, agent or model names. Trove uploads nothing.\n")
	if !a.JSON {
		_, _ = fmt.Fprint(a.Out, b.String()+"\n")
	}

	consent := yes
	if !consent && !a.JSON {
		q := fmt.Sprintf("Save %s and %s?", svgPath, filepath.Base(pngPath))
		if exists(svgPath) || exists(pngPath) {
			q = fmt.Sprintf("Save %s and %s? This replaces files that already exist.", svgPath, filepath.Base(pngPath))
		}
		consent = a.confirm(q)
	}
	if !consent {
		msg := "Nothing saved. Run again and answer y, or pass --yes."
		if !a.JSON {
			return a.emit(shareOutput{Card: &card}, "\n"+msg)
		}
		return a.emit(shareOutput{Card: &card}, msg)
	}
	svgPath, pngPath, err = share.Save(base, card, th)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("Saved %s and %s. Open them to check, then post whichever you like.", svgPath, pngPath)
	if !a.JSON {
		text = "\n" + text
	}
	return a.emit(shareOutput{Card: &card, Saved: true, SVG: svgPath, PNG: pngPath}, text)
}

// shareBase turns --out into the path without extension that the card files
// share: an existing folder gets trove-share inside it, a name ending in .svg
// or .png is trimmed, anything else is used as is.
func shareBase(out string) (string, error) {
	if out == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot find the current folder: %v. Choose a place with --out", err)
		}
		return filepath.Join(wd, "trove-share"), nil
	}
	if fi, err := os.Stat(out); err == nil && fi.IsDir() {
		return filepath.Join(out, "trove-share"), nil
	}
	return strings.TrimSuffix(strings.TrimSuffix(out, ".svg"), ".png"), nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
