package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/pricing"
)

// Set at build time with -ldflags "-X github.com/usetrove/trove/internal/cli.Version=...".
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

// runVersion prints the version, commit, build date and pricing data version.
// The first line of the text form is exactly "trove <version>"; the plugin's
// launcher compares it to the version it pins.
func runVersion(a *App) error {
	v := versionInfo{Version, Commit, Date, pricing.DataVersion}
	return a.emit(v, fmt.Sprintf("trove %s\ncommit: %s\nbuilt: %s\npricing data: %s", v.Version, v.Commit, v.BuildDate, v.PricingData))
}

// newVersion is the old `trove version` spelling. It is hidden and kept for one
// release so scripts and older plugin launchers keep working; `trove --version`
// is the documented form.
func newVersion(a *App) *cobra.Command {
	return &cobra.Command{
		Use:    "version",
		Short:  "Same as `trove --version`",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE:   func(*cobra.Command, []string) error { return runVersion(a) },
	}
}
