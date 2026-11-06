package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/contribute"
	"github.com/usetrove/trove/internal/report"
)

type contributeStatus struct {
	Enabled   bool   `json:"enabled"`
	InstallID string `json:"install_id,omitempty"`
	LastSent  string `json:"last_sent,omitempty"`
	Endpoint  string `json:"endpoint"`
}

func newContribute(a *App) *cobra.Command {
	var pf periodFlags
	var export string
	c := &cobra.Command{
		Use:   "contribute",
		Short: "Show, save or (if you opt in) share an anonymous summary of your results",
		Long: "Builds a small summary of aggregate results (counts, whole-number percents and fixed labels) that helps\n" +
			"check Trove's savings claims. It has no prompts, code, file paths, hashes, model names, dollar amounts or\n" +
			"exact token counts.\n\n" +
			"By itself this command only shows the summary, or writes it to a file with --export. Trove sends it\n" +
			"nowhere unless you run `trove contribute enable`, which shows the exact text first and asks.",
		Example: "  trove contribute\n  trove contribute --export trove-summary.json\n  trove contribute enable",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, _, err := loadConfig(a)
			if err != nil {
				return err
			}
			p, body, err := summary(a, pf, dir)
			if err != nil {
				return err
			}
			if export != "" {
				if err := os.WriteFile(export, body, 0o600); err != nil {
					return fmt.Errorf("cannot write %s: %v. Choose a folder you can write to", export, err)
				}
			}
			text := "This is the whole summary. Share it only if you choose to.\n\n" + string(body)
			if export != "" {
				text += "\nSaved to " + export + "."
			}
			return a.emit(p, text)
		},
	}
	pf.add(c)
	c.Flags().StringVar(&export, "export", "", "write the summary to this file so you can share it yourself")
	c.AddCommand(newContributeEnable(a), newContributeDisable(a), newContributeDelete(a), newContributeReset(a), newContributeStatus(a))
	return c
}

// summary builds the payload and its exact text. It carries the install id only
// when upload is on, so the preview shows what an upload would send.
func summary(a *App, pf periodFlags, dir string) (contribute.Payload, []byte, error) {
	r, err := buildReport(a, pf)
	if err != nil {
		return contribute.Payload{}, nil, err
	}
	st, err := contribute.LoadState(dir)
	if err != nil {
		return contribute.Payload{}, nil, err
	}
	p := payloadOf(r)
	if st.Enabled {
		p.InstallID = st.InstallID
	}
	b, err := contribute.Encode(p)
	return p, b, err
}

func payloadOf(r report.Report) contribute.Payload {
	return contribute.Build(r, contribute.Meta{TroveVersion: Version, OS: runtime.GOOS, Arch: runtime.GOARCH})
}

func newContributeEnable(a *App) *cobra.Command {
	var pf periodFlags
	var yes bool
	c := &cobra.Command{
		Use:   "enable",
		Short: "Share the summary automatically, at most once a week",
		Long: "Shows the exact summary and asks before turning on a weekly upload to the endpoint in config.toml\n" +
			"([contribute] endpoint). Nothing is sent until you answer yes. Undo it any time with `trove contribute disable`.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, cfg, err := loadConfig(a)
			if err != nil {
				return err
			}
			if cfg.Contribute.Endpoint == "" {
				return fmt.Errorf("there is no endpoint to send to. Set endpoint under [contribute] in %s, or use `trove contribute --export` to share a file yourself", dir+string(os.PathSeparator)+"config.toml")
			}
			st, err := contribute.LoadState(dir)
			if err != nil {
				return err
			}
			if st.InstallID == "" {
				if st.InstallID, err = contribute.NewInstallID(); err != nil {
					return err
				}
			}
			r, err := buildReport(a, pf)
			if err != nil {
				return err
			}
			p := payloadOf(r)
			p.InstallID = st.InstallID
			body, err := contribute.Encode(p)
			if err != nil {
				return err
			}
			if !a.JSON {
				_, _ = fmt.Fprintf(a.Out, "If you turn this on, Trove will send exactly this text to %s at most once every 7 days:\n\n%s\n"+
					"It has no prompts, code, paths, model names or dollar amounts. The install id is random and not linked to you.\n"+
					"To have your data deleted, send the install id to the project and it will be removed.\n\n", cfg.Contribute.Endpoint, body)
			}
			if !yes && !a.confirm("Turn on the weekly share?") {
				return a.emit(contributeStatus{Enabled: st.Enabled, Endpoint: cfg.Contribute.Endpoint}, "Nothing changed. Run again and answer y, or pass --yes.")
			}
			st.Enabled = true
			if err := contribute.SaveState(dir, st); err != nil {
				return err
			}
			return a.emit(contributeStatus{Enabled: true, InstallID: st.InstallID, Endpoint: cfg.Contribute.Endpoint},
				"Weekly share is on. The first upload happens the next time you run `trove report`. Turn it off with `trove contribute disable`.")
		},
	}
	pf.add(c)
	c.Flags().BoolVar(&yes, "yes", false, "turn on without asking")
	return c
}

func newContributeDisable(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Stop sharing the summary",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, cfg, err := loadConfig(a)
			if err != nil {
				return err
			}
			st, err := contribute.LoadState(dir)
			if err != nil {
				return err
			}
			st.Enabled = false
			if err := contribute.SaveState(dir, st); err != nil {
				return err
			}
			return a.emit(contributeStatus{Endpoint: cfg.Contribute.Endpoint, InstallID: st.InstallID, LastSent: st.LastSent},
				"Sharing is off. Nothing more will be sent. Data already sent stays until you ask for it to be deleted: run `trove contribute delete`.")
		},
	}
}

func newContributeDelete(a *App) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "delete",
		Short: "Ask the receiver to delete every summary it holds under your install id",
		Long: "Sends one request, only when you run this command: DELETE to the sharing endpoint in config.toml followed by your install id.\n" +
			"It has no body, no credentials and no cookies, and follows no redirects. Trove shows the exact request first and asks.\n" +
			"If the receiver accepts it, sharing is switched off and your install id is removed from this computer. If it cannot be\n" +
			"reached nothing changes, so you can try again. Nothing else ever triggers this request.",
		Example: "  trove contribute delete\n  trove contribute delete --yes",
		Args:    cobra.NoArgs,
		RunE:    func(*cobra.Command, []string) error { return runContributeDelete(a, yes) },
	}
	c.Flags().BoolVar(&yes, "yes", false, "send the request without asking")
	return c
}

func runContributeDelete(a *App, yes bool) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	st, err := contribute.LoadState(dir)
	if err != nil {
		return err
	}
	if st.InstallID == "" {
		return a.emit(contributeStatus{Endpoint: cfg.Contribute.Endpoint}, "Nothing to delete: this computer has no install id, so nothing was ever shared under one.")
	}
	url, err := contribute.DeleteURL(cfg.Contribute.Endpoint, st.InstallID)
	if err != nil {
		return err
	}
	if !yes {
		if !a.JSON {
			_, _ = fmt.Fprintf(a.Out, "This sends one request, and nothing else:\n  DELETE %s\nIt asks the receiver to delete every summary stored under your install id. It has no body, no credentials and no cookies.\n", url)
		}
		if !a.confirm("Send the request?") {
			return a.emit(contributeStatus{Enabled: st.Enabled, InstallID: st.InstallID, Endpoint: cfg.Contribute.Endpoint}, "Nothing was sent. Run again and answer y, or pass --yes.")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), contribute.Timeout)
	defer cancel()
	if err := contribute.Delete(ctx, cfg.Contribute.Endpoint, st.InstallID); err != nil {
		return err
	}
	if err := contribute.SaveState(dir, contribute.State{}); err != nil {
		return err
	}
	return a.emit(contributeStatus{Endpoint: cfg.Contribute.Endpoint},
		"The receiver accepted the request to delete everything stored under your install id. Sharing is now off and the install id is removed from this computer.\n"+
			"Trove cannot check the receiver's database from here. To share again later, run `trove contribute enable`, which makes a new id.")
}

func newContributeReset(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Replace the install id with a new random one",
		Long: "Makes a new random install id, so later uploads are not tied to earlier ones. Rows already sent keep the old id, and once the old id is gone\n" +
			"you can no longer ask for them to be deleted. Run `trove contribute delete` first if you want them removed.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, cfg, err := loadConfig(a)
			if err != nil {
				return err
			}
			st, err := contribute.LoadState(dir)
			if err != nil {
				return err
			}
			if st.InstallID, err = contribute.NewInstallID(); err != nil {
				return err
			}
			st.LastSent = ""
			if err := contribute.SaveState(dir, st); err != nil {
				return err
			}
			return a.emit(contributeStatus{Enabled: st.Enabled, InstallID: st.InstallID, Endpoint: cfg.Contribute.Endpoint}, "Made a new install id: "+st.InstallID)
		},
	}
}

func newContributeStatus(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether sharing is on, and your install id",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, cfg, err := loadConfig(a)
			if err != nil {
				return err
			}
			st, err := contribute.LoadState(dir)
			if err != nil {
				return err
			}
			s := contributeStatus{Enabled: st.Enabled, InstallID: st.InstallID, LastSent: st.LastSent, Endpoint: cfg.Contribute.Endpoint}
			text := "Sharing is off. Trove sends nothing."
			if st.Enabled {
				text = "Sharing is on: a summary goes to " + s.Endpoint + " at most once every 7 days."
				if s.LastSent != "" {
					text += "\nLast sent: " + s.LastSent + " (UTC)."
				}
			}
			if s.InstallID != "" {
				text += "\nInstall id: " + s.InstallID
			}
			return a.emit(s, text)
		},
	}
}

// autoContribute runs after `trove report` prints. It does nothing unless the
// user turned sharing on and an upload is due, never prints, and never changes
// the command's result: a failure just means it tries again next time.
func autoContribute(a *App, r report.Report) {
	dir, cfg, err := loadConfig(a)
	if err != nil || cfg.Contribute.Endpoint == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), contribute.Timeout)
	defer cancel()
	_, _ = contribute.Auto(ctx, dir, cfg.Contribute.Endpoint, payloadOf(r), time.Now())
}
