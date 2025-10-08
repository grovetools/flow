package cmd

import (
	"github.com/mattsolo1/grove-core/starship"
	"github.com/spf13/cobra"
)

func init() {
	// Register flow's status provider
	starship.RegisterProvider(FlowStatusProvider)
}

func NewStarshipCmd() *cobra.Command {
	return starship.NewStarshipCmd("flow")
}
