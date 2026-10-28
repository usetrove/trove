package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jaclynsunardi/trove/internal/pricing"
)

// Set at build time with -ldflags "-X github.com/jaclynsunardi/trove/internal/cli.Version=...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

type versionInfo struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	BuildDate   string `json:"build_date"`
	PricingData string `json:"pricing_data_version"`
}

func newVersion(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit, build date and pricing data version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			v := versionInfo{Version, Commit, Date, pricing.DataVersion}
			return a.emit(v, fmt.Sprintf("trove %s\ncommit: %s\nbuilt: %s\npricing data: %s", v.Version, v.Commit, v.BuildDate, v.PricingData))
		},
	}
}
