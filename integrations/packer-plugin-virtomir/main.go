// Command packer-plugin-virtomir is the Packer plugin that builds VirtoMir templates.
package main

import (
	"fmt"
	"os"

	"github.com/hashicorp/packer-plugin-sdk/plugin"
	"github.com/hashicorp/packer-plugin-sdk/version"

	"github.com/onurbaydas/virtomir/integrations/packer-plugin-virtomir/builder/virtomir"
)

// Version is set at build time (-ldflags "-X main.Version=0.1.0").
var Version = "0.1.0"

func main() {
	pps := plugin.NewSet()
	pps.RegisterBuilder(plugin.DEFAULT_NAME, new(virtomir.Builder))
	pps.SetVersion(version.InitializePluginVersion(Version, ""))
	if err := pps.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
