package sdn

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// VPC is a project's private network: subnets joined by one distributed router.
type VPC struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ProjectID   string `json:"projectId"`
	IPv4CIDR    string `json:"ipv4Cidr"`
	// DefaultDeny drops traffic to the VPC's ports unless a security group allows it.
	DefaultDeny bool      `json:"defaultDeny"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
}

// Subnet is one logical switch of a VPC, with OVN-native DHCP when enabled.
type Subnet struct {
	ID                  string   `json:"id"`
	VPCID               string   `json:"vpcId"`
	Name                string   `json:"name"`
	CIDR                string   `json:"cidr"`
	GatewayIP           string   `json:"gatewayIp"`
	DHCPEnabled         bool     `json:"dhcpEnabled"`
	AllocationPoolStart string   `json:"allocationPoolStart,omitempty"`
	AllocationPoolEnd   string   `json:"allocationPoolEnd,omitempty"`
	DNSServers          []string `json:"dnsServers,omitempty"`
	OVNDHCPID           string   `json:"ovnDhcpId,omitempty"`
	LogicalSwitch       string   `json:"logicalSwitch"`
}

// Route is a static route of a VPC router.
type Route struct {
	DestinationCIDR string `json:"destinationCidr"`
	NextHopIP       string `json:"nextHopIp"`
}

// Router is a VPC's distributed router.
type Router struct {
	ID            string  `json:"id"`
	VPCID         string  `json:"vpcId"`
	Name          string  `json:"name"`
	LogicalRouter string  `json:"logicalRouter"`
	Routes        []Route `json:"routes"`
}

// Port is a machine's adapter on a subnet (an OVN logical switch port).
type Port struct {
	ID             string   `json:"id"` // also the OVS iface-id
	SubnetID       string   `json:"subnetId"`
	VMUUID         string   `json:"vmUuid"`
	MAC            string   `json:"mac"`
	IP             string   `json:"ip,omitempty"`
	SecurityGroups []string `json:"securityGroups,omitempty"`
}

// OVN object names.
func routerName(vpcID string) string      { return "lr-" + vpcID }
func switchName(subnetID string) string   { return "ls-" + subnetID }
func routerPort(subnetID string) string   { return "lrp-" + subnetID }
func switchRouter(subnetID string) string { return "lsp-" + subnetID + "-router" }

// routerMAC is a subnet's router port MAC: locally administered and different for every
// subnet (ports of one router must not share a MAC).
func routerMAC(id string) string {
	h := sha256.Sum256([]byte("virtomir-sdn/" + id))
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", h[0], h[1], h[2], h[3], h[4])
}

// ExternalGateway connects a VPC's router to an external network: outbound traffic is
// SNATed to ExternalIP, and the default route goes to GatewayIP.
type ExternalGateway struct {
	VPCID           string `json:"vpcId"`
	ExternalNetwork string `json:"externalNetwork"` // the provider network (OVN localnet) name
	GatewayIP       string `json:"gatewayIp"`       // the upstream router
	ExternalIP      string `json:"externalIp"`      // CIDR form, e.g. 203.0.113.10/24
	Enabled         bool   `json:"enabled"`
	// GatewayChassis is the OVN chassis (a host's system-id) that carries the VPC's
	// external traffic; OVN needs one for SNAT.
	GatewayChassis string `json:"gatewayChassis"`
}

// FloatingIP is an external address mapped one-to-one to a machine's private address.
type FloatingIP struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	VPCID       string    `json:"vpcId"`
	ExternalIP  string    `json:"externalIp"`
	InternalIP  string    `json:"internalIp,omitempty"`
	PortID      string    `json:"portId,omitempty"`
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
}

// SecurityRule is one allow or drop rule of a security group.
type SecurityRule struct {
	ID            string `json:"id"`
	Direction     string `json:"direction"` // ingress, egress
	Action        string `json:"action"`    // allow, allow-related, drop, reject
	Priority      int    `json:"priority"`  // 1–32767
	Protocol      string `json:"protocol"`  // tcp, udp, icmp, any
	PortMin       int    `json:"portMin,omitempty"`
	PortMax       int    `json:"portMax,omitempty"`
	RemoteCIDR    string `json:"remoteCidr,omitempty"`
	RemoteGroupID string `json:"remoteGroupId,omitempty"`
	Description   string `json:"description,omitempty"`
}

// SecurityGroup is a set of ports with the rules that apply to them (an OVN port group
// with stateful ACLs).
type SecurityGroup struct {
	ID          string         `json:"id"`
	ProjectID   string         `json:"projectId"`
	VPCID       string         `json:"vpcId"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Rules       []SecurityRule `json:"rules"`
	DefaultDeny bool           `json:"defaultDeny"`
	MemberPorts []string       `json:"memberPorts"`
	Created     time.Time      `json:"created"`
	Updated     time.Time      `json:"updated"`
}

// HealthCheckSpec is how a load balancer checks its members.
type HealthCheckSpec struct {
	Protocol        string `json:"protocol"` // tcp, http
	Port            int    `json:"port"`
	IntervalSeconds int    `json:"intervalSeconds"`
	TimeoutSeconds  int    `json:"timeoutSeconds"`
	Fall            int    `json:"fall"`
	Rise            int    `json:"rise"`
	HTTPPath        string `json:"httpPath,omitempty"`
}

// LoadBalancer spreads a virtual IP's connections over member addresses (OVN load_balancer).
type LoadBalancer struct {
	ID               string           `json:"id"`
	ProjectID        string           `json:"projectId"`
	VPCID            string           `json:"vpcId"`
	Name             string           `json:"name"`
	Description      string           `json:"description,omitempty"`
	VIP              string           `json:"vip"`
	Port             int              `json:"port"`
	Protocol         string           `json:"protocol"` // tcp, udp
	Members          []string         `json:"members"`  // ip:port
	AttachedSwitches []string         `json:"attachedSwitches"`
	AttachedRouters  []string         `json:"attachedRouters"`
	HealthCheck      *HealthCheckSpec `json:"healthCheck,omitempty"`
	Status           string           `json:"status"`
	Created          time.Time        `json:"created"`
	Updated          time.Time        `json:"updated"`
}

// IPsecConfig encrypts the overlay tunnels between hosts.
type IPsecConfig struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"` // psk (cert not yet)
	// PSK is the pre-shared key; the API never sends it back (see Redacted).
	PSK       string `json:"psk,omitempty"`
	SKBMark   uint   `json:"skbMark,omitempty"`
	Status    string `json:"status"` // disabled, active, error
	LastError string `json:"lastError,omitempty"`
}

// Redacted is cfg without its key, for answers.
func (c IPsecConfig) Redacted() IPsecConfig {
	if c.PSK != "" {
		c.PSK = "********"
	}
	return c
}
