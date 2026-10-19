package sdn

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
)

// ReconcileVPC makes the VPC's logical router exist.
func ReconcileVPC(ctx context.Context, nb NB, vpc VPC) error {
	lr := routerName(vpc.ID)
	if _, err := nb.Run(ctx, "--may-exist", "lr-add", lr); err != nil {
		return err
	}
	_, err := nb.Run(ctx, "set", "Logical_Router", lr, "external_ids:virtomir-project="+vpc.ProjectID, "external_ids:virtomir-vpc="+vpc.ID)
	return err
}

// DeleteVPCResources removes the VPC's logical router.
func DeleteVPCResources(ctx context.Context, nb NB, vpcID string) error {
	_, err := nb.Run(ctx, "--if-exists", "lr-del", routerName(vpcID))
	return err
}

// dhcpOptions is the OVN DHCP options of a subnet (server_id and router are the gateway).
func dhcpOptions(sub Subnet) []string {
	opts := []string{
		"options:server_id=" + sub.GatewayIP,
		"options:server_mac=" + routerMAC(sub.ID),
		"options:router=" + sub.GatewayIP,
		"options:lease_time=3600",
	}
	if len(sub.DNSServers) > 0 {
		opts = append(opts, "options:dns_server={"+strings.Join(sub.DNSServers, ",")+"}")
	}
	return opts
}

// ReconcileSubnet makes the subnet's logical switch, its DHCP options and its link to the
// VPC router exist, and returns the DHCP options' UUID ("" without DHCP).
func ReconcileSubnet(ctx context.Context, nb NB, vpc VPC, sub Subnet) (string, error) {
	p, err := netip.ParsePrefix(sub.CIDR)
	if err != nil {
		return "", err
	}
	ls, lrp, lsp, mac := switchName(sub.ID), routerPort(sub.ID), switchRouter(sub.ID), routerMAC(sub.ID)
	steps := [][]string{
		{"--may-exist", "ls-add", ls},
		{"set", "Logical_Switch", ls, "other_config:subnet=" + sub.CIDR, "external_ids:virtomir-subnet=" + sub.ID, "external_ids:virtomir-vpc=" + vpc.ID},
		{"--may-exist", "lrp-add", routerName(vpc.ID), lrp, mac, fmt.Sprintf("%s/%d", sub.GatewayIP, p.Bits())},
		{"--may-exist", "lsp-add", ls, lsp},
		{"lsp-set-type", lsp, "router"},
		{"lsp-set-addresses", lsp, "router"},
		{"lsp-set-options", lsp, "router-port=" + lrp},
	}
	for _, s := range steps {
		if _, err := nb.Run(ctx, s...); err != nil {
			return "", err
		}
	}
	if !sub.DHCPEnabled {
		return "", nil
	}
	// One DHCP_Options row per subnet, found again by its external ID.
	out, err := nb.Run(ctx, "--bare", "--columns=_uuid", "find", "DHCP_Options", "external_ids:virtomir-subnet="+sub.ID)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if i := strings.IndexByte(id, '\n'); i >= 0 {
		id = id[:i]
	}
	if id == "" {
		args := append([]string{"create", "DHCP_Options", "cidr=" + sub.CIDR, "external_ids:virtomir-subnet=" + sub.ID}, dhcpOptions(sub)...)
		out, err := nb.Run(ctx, args...)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	args := append([]string{"set", "DHCP_Options", id, "cidr=" + sub.CIDR}, dhcpOptions(sub)...)
	_, err = nb.Run(ctx, args...)
	return id, err
}

// DeleteSubnetResources removes the subnet's switch, its router port and its DHCP options.
func DeleteSubnetResources(ctx context.Context, nb NB, vpcID string, sub Subnet) error {
	steps := [][]string{
		{"--if-exists", "lsp-del", switchRouter(sub.ID)},
		{"--if-exists", "lrp-del", routerPort(sub.ID)},
		{"--if-exists", "ls-del", switchName(sub.ID)},
	}
	if sub.OVNDHCPID != "" {
		steps = append(steps, []string{"--if-exists", "destroy", "DHCP_Options", sub.OVNDHCPID})
	}
	for _, s := range steps {
		if _, err := nb.Run(ctx, s...); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileRoutes sets a router's static routes (the default route belongs to the gateway).
func ReconcileRoutes(ctx context.Context, nb NB, r Router) error {
	lr := routerName(r.VPCID)
	for _, rt := range r.Routes {
		if _, err := nb.Run(ctx, "--may-exist", "lr-route-add", lr, rt.DestinationCIDR, rt.NextHopIP); err != nil {
			return err
		}
	}
	return nil
}
