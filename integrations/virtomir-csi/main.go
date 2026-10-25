// Command virtomir-csi is the VirtoMir CSI driver: run with -controller once per cluster
// (with the VirtoMir URL and an API token) and with -node on every node.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/onurbaydas/virtomir/integrations/virtomir-csi/driver"
)

func main() {
	endpoint := flag.String("endpoint", "unix:///csi/csi.sock", "the CSI socket")
	controller := flag.Bool("controller", false, "serve the controller (volumes, attaching)")
	node := flag.Bool("node", false, "serve the node plugin (formatting, mounting)")
	base := flag.String("url", os.Getenv("VIRTOMIR_URL"), "VirtoMir, e.g. https://hv1:8420 (controller)")
	tokenFile := flag.String("token-file", "/etc/virtomir/token", "file holding a VirtoMir API token of an admin (controller)")
	insecure := flag.Bool("insecure", os.Getenv("VIRTOMIR_INSECURE") == "true", "accept a self-signed VirtoMir certificate")
	nodeID := flag.String("node-id", "", "this node's machine UUID (default: the SMBIOS system UUID libvirt gives the machine)")
	version := flag.Bool("version", false, "print the version")
	flag.Parse()
	if *version {
		fmt.Println(driver.Version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if !*controller && !*node {
		log.Error("say -controller, -node or both")
		os.Exit(2)
	}
	d := &driver.Driver{Log: log, Mounter: driver.ExecMounter{}, DevDir: "/dev/disk/by-id"}
	if *controller {
		tok, err := os.ReadFile(*tokenFile)
		if err != nil {
			log.Error("API token", "err", err)
			os.Exit(1)
		}
		if d.API, err = driver.NewAPI(*base, strings.TrimSpace(string(tok)), *insecure); err != nil {
			log.Error("VirtoMir", "err", err)
			os.Exit(1)
		}
	}
	if *node {
		d.NodeID = *nodeID
		if d.NodeID == "" {
			raw, err := os.ReadFile("/sys/class/dmi/id/product_uuid")
			if err != nil {
				log.Error("node ID", "err", err)
				os.Exit(1)
			}
			d.NodeID = strings.ToLower(strings.TrimSpace(string(raw)))
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Serve(ctx, *endpoint); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
