package cli

import (
	"context"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/feedback"
	"github.com/usetrove/trove/internal/pricing"
)

type feedbackOutput struct {
	Diagnostics feedback.Diagnostics `json:"diagnostics"`
	Body        string               `json:"issue_body"`
	URL         string               `json:"url"`
	Truncated   bool                 `json:"url_truncated"`
}

func newFeedback(a *App) *cobra.Command {
	var note string
	var noOpen bool
	c := &cobra.Command{
		Use:   "feedback",
		Short: "Prepare a GitHub issue with diagnostics, shown to you first",
		Long: "Builds a prefilled GitHub issue with versions, the agents Trove found, counts and error classes.\n" +
			"It never includes prompts, code, file paths, tool output or keys. You see the full text first and\n" +
			"nothing is opened or sent unless you say yes. Use --no-open to just print the link.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, _, err := loadConfig(a)
			if err != nil {
				return err
			}
			d := feedback.Diagnostics{
				TroveVersion: Version, Commit: Commit, GoVersion: runtime.Version(),
				OS: runtime.GOOS, Arch: runtime.GOARCH, PricingData: pricing.DataVersion,
			}
			for _, s := range supportedAgents {
				if _, err := a.LookPath(s.binary); err == nil {
					d.AgentsDetected = append(d.AgentsDetected, s.name)
				}
			}
			db, err := openExisting(dir)
			if err != nil {
				return err
			}
			if db != nil {
				defer func() { _ = db.Close() }()
				feedback.Collect(context.Background(), db.DB, &d)
			} else {
				feedback.Collect(context.Background(), nil, &d)
			}
			body := feedback.Body(d, note)
			link, trunc := feedback.IssueURL("Beta feedback: ", body)
			out := feedbackOutput{d, body, link, trunc}
			if a.JSON {
				return a.emit(out, "")
			}
			text := "This is exactly what would go into the issue:\n\n" + body + "\nLink: " + link
			if err := a.emit(out, text); err != nil {
				return err
			}
			if noOpen {
				return nil
			}
			if !a.confirm("Open this prefilled issue in your browser? You can still edit it there before submitting.") {
				_, _ = fmt.Fprintln(a.Out, "\nNothing was opened. Copy the link above when you are ready.")
				return nil
			}
			if err := openBrowser(a, link); err != nil {
				_, _ = fmt.Fprintf(a.Err, "Could not open a browser (%v). Copy the link above instead.\n", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&note, "note", "", "a short description to put at the top of the issue")
	c.Flags().BoolVar(&noOpen, "no-open", false, "print the link and do not offer to open it")
	return c
}

func openBrowser(a *App, link string) error {
	if a.Run == nil {
		return fmt.Errorf("no way to start a browser here")
	}
	switch runtime.GOOS {
	case "darwin":
		return a.Run("open", link)
	case "windows":
		return a.Run("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		return a.Run("xdg-open", link)
	}
}
