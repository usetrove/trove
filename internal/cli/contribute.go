package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/contribute"
)

func newContribute(a *App) *cobra.Command {
	var pf periodFlags
	var export string
	c := &cobra.Command{
		Use:   "contribute",
		Short: "Show, or save to a file, the anonymous summary you could share",
		Long: "Builds a small summary of aggregate results (counts, whole-number percents and fixed labels) that you can\n" +
			"choose to share to help check Trove's savings claims. It has no prompts, code, file paths, hashes, model\n" +
			"names, dollar amounts or exact token counts.\n\n" +
			"This command only shows the summary or writes it to a file with --export. Trove sends nothing anywhere.",
		Example: "  trove contribute\n  trove contribute --export trove-summary.json",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			r, err := buildReport(a, pf)
			if err != nil {
				return err
			}
			p := contribute.Build(r, contribute.Meta{TroveVersion: Version, OS: runtime.GOOS, Arch: runtime.GOARCH})
			b, err := json.MarshalIndent(p, "", "  ")
			if err != nil {
				return err
			}
			b = append(b, '\n')
			if export != "" {
				if err := os.WriteFile(export, b, 0o600); err != nil {
					return fmt.Errorf("cannot write %s: %v. Choose a folder you can write to", export, err)
				}
			}
			text := "This is the whole summary. Trove sends nothing; share it only if you choose to.\n\n" + string(b)
			if export != "" {
				text += "\nSaved to " + export + "."
			}
			return a.emit(p, text)
		},
	}
	pf.add(c)
	c.Flags().StringVar(&export, "export", "", "write the summary to this file so you can share it yourself")
	return c
}
