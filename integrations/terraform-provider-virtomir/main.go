// Terraform provider for VirtoMir (ADR-0049). MPL-2.0, like most Terraform providers; see LICENSE.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/onurbaydas/virtomir/integrations/terraform-provider-virtomir/internal/provider"
)

var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/onurbaydas/virtomir",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
