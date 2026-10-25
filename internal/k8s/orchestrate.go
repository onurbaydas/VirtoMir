package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// VMRequest is one node's machine.
type VMRequest struct {
	Name, Image, StoragePool, Network, IPPool, Project, UserData string
	VCPUs                                                        int
	MemoryMiB, DiskGiB                                           uint64
}

// ExecResult is a command run inside a node.
type ExecResult struct {
	ExitCode       int
	Stdout, Stderr string
}

// Driver does what the orchestrator needs on the members, as the user who asked.
type Driver interface {
	CreateVM(ctx context.Context, hostID string, r VMRequest) (uuid string, err error)
	DeleteVM(ctx context.Context, hostID, uuid string) error
	Exec(ctx context.Context, hostID, uuid string, argv ...string) (ExecResult, error)
	ReserveIP(ctx context.Context, pool, name string) (string, error)
	ReleaseIP(ctx context.Context, pool, ip string) error
	// AntiAffinity keeps machines on different hosts where it can (a "should" rule).
	AntiAffinity(ctx context.Context, name string, uuids []string) (ruleID string, err error)
	DeleteRule(ctx context.Context, id string) error
}

// Orchestrator runs the long operations of clusters in the background, one per cluster.
type Orchestrator struct {
	store  *Store
	driver func(Cluster) Driver
	log    *slog.Logger

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup

	// Poll is how often a node is asked how far it got; BootTimeout bounds one node's
	// cloud-init, ReadyTimeout the wait for Ready nodes.
	Poll, BootTimeout, ReadyTimeout time.Duration
}

// New makes an orchestrator; driver gives the driver acting as a cluster's creator.
func New(store *Store, driver func(Cluster) Driver, log *slog.Logger) *Orchestrator {
	if log == nil {
		log = slog.Default()
	}
	return &Orchestrator{store: store, driver: driver, log: log, running: map[string]bool{},
		Poll: 10 * time.Second, BootTimeout: 40 * time.Minute, ReadyTimeout: 15 * time.Minute}
}

// Wait waits for every running operation (tests, shutdown).
func (o *Orchestrator) Wait() { o.wg.Wait() }

// Running reports whether an operation of cluster id runs here now.
func (o *Orchestrator) Running(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.running[id]
}

// start runs fn for cluster id in the background, after putting it in state; the cluster
// ends ready, or failed with fn's error.
func (o *Orchestrator) start(ctx context.Context, id, state, what string, fn func(context.Context, Driver) error) error {
	o.mu.Lock()
	if o.running[id] {
		o.mu.Unlock()
		return fmt.Errorf("%w: something already runs on this cluster", ErrBusy)
	}
	c, err := o.store.Update(id, func(c *Cluster) error {
		if c.Busy() && c.State != state {
			return fmt.Errorf("%w: the cluster is %s", ErrBusy, c.State)
		}
		c.State, c.Message = state, what
		return nil
	})
	if err != nil {
		o.mu.Unlock()
		return err
	}
	o.running[id] = true
	o.mu.Unlock()
	o.store.Log(id, false, "%s", what)
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer func() {
			o.mu.Lock()
			delete(o.running, id)
			o.mu.Unlock()
		}()
		err := fn(ctx, o.driver(c))
		if errors.Is(err, errDeleted) {
			return
		}
		state, msg := StateReady, ""
		if err != nil {
			state, msg = StateFailed, err.Error()
			o.store.Log(id, true, "%s", err)
			o.log.Warn("Kubernetes cluster", "cluster", c.Spec.Name, "err", err)
		} else {
			o.store.Log(id, false, "done")
		}
		_, _ = o.store.Update(id, func(c *Cluster) error {
			c.State, c.Message = state, msg
			return nil
		})
	}()
	return nil
}

var errDeleted = errors.New("cluster deleted")

// Recover is called once at start: an operation cut off by a restart cannot continue
// safely, so the cluster is marked failed (deleting continues).
func (o *Orchestrator) Recover(ctx context.Context) {
	for _, c := range o.store.List() {
		switch {
		case c.State == StateDeleting:
			_ = o.Delete(ctx, c.ID)
		case c.Busy():
			_, _ = o.store.Update(c.ID, func(c *Cluster) error {
				c.Message = "VirtoMir restarted while the cluster was " + c.State + "; check the nodes, then scale, upgrade or delete it again"
				c.State = StateFailed
				return nil
			})
		}
	}
}

// ---- provisioning ------------------------------------------------------------------------

// Provision makes the machines of a new cluster (already in the store) and waits for them.
func (o *Orchestrator) Provision(ctx context.Context, id string) error {
	return o.start(ctx, id, StateProvisioning, "creating the cluster", func(ctx context.Context, d Driver) error {
		return o.provision(ctx, d, id)
	})
}

func (o *Orchestrator) provision(ctx context.Context, d Driver, id string) error {
	c, _ := o.store.Get(id)
	if c.Spec.ControlPlanes > 1 && c.Spec.VIP == "" && c.ReservedVIP == "" {
		ip, err := d.ReserveIP(ctx, c.Spec.IPPool, "Kubernetes API of "+c.Spec.Name)
		if err != nil {
			return fmt.Errorf("reserve the API address: %w", err)
		}
		c, _ = o.store.Update(id, func(c *Cluster) error { c.ReservedVIP = ip; return nil })
		o.store.Log(id, false, "API address %s reserved from the IP pool", ip)
	}
	if vip := c.APIAddress(); vip != "" && c.Spec.ControlPlanes > 1 {
		c, _ = o.store.Update(id, func(c *Cluster) error { c.Endpoint = "https://" + vip + ":6443"; return nil })
	}
	cps := c.ControlPlanes()
	// The first control plane makes the cluster; the others join it one at a time (etcd
	// takes one new member at a time); the workers join together.
	if err := o.bringUp(ctx, d, id, cps[0].Name, c.PKI.Token); err != nil {
		return err
	}
	for _, n := range cps[1:] {
		if err := o.bringUp(ctx, d, id, n.Name, c.PKI.Token); err != nil {
			return err
		}
	}
	var workers []string
	for _, n := range c.Nodes {
		if n.Role == RoleWorker {
			workers = append(workers, n.Name)
		}
	}
	if err := o.bringUpAll(ctx, d, id, workers, c.PKI.Token); err != nil {
		return err
	}
	if err := o.waitReady(ctx, d, id, ""); err != nil {
		return err
	}
	c, _ = o.store.Get(id)
	if c.Spec.Workers == 0 {
		// Without workers the control planes run the workloads too.
		if _, err := o.kubectl(ctx, d, c, "taint", "nodes", "--all", "node-role.kubernetes.io/control-plane-"); err != nil {
			o.store.Log(id, true, "control planes not opened for workloads: %s", err)
		}
	}
	// Ready nodes do not prove the pod network works; CoreDNS running on it does.
	if err := o.waitDNS(ctx, d, id); err != nil {
		return err
	}
	if hosts := distinctHosts(c.ControlPlanes()); hosts > 1 && c.RuleID == "" {
		var uuids []string
		for _, n := range c.ControlPlanes() {
			uuids = append(uuids, n.UUID)
		}
		rule, err := d.AntiAffinity(ctx, "Kubernetes "+c.Spec.Name+" control planes apart", uuids)
		if err != nil {
			o.store.Log(id, true, "no placement rule for the control planes: %s", err)
		} else {
			_, _ = o.store.Update(id, func(c *Cluster) error { c.RuleID = rule; return nil })
		}
	}
	return nil
}

func distinctHosts(nodes []Node) int {
	seen := map[string]bool{}
	for _, n := range nodes {
		seen[n.HostID] = true
	}
	return len(seen)
}

func (o *Orchestrator) bringUpAll(ctx context.Context, d Driver, id string, names []string, token string) error {
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = o.bringUp(ctx, d, id, name, token)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (o *Orchestrator) setNode(id, name string, fn func(*Node)) Cluster {
	c, _ := o.store.Update(id, func(c *Cluster) error {
		for i := range c.Nodes {
			if c.Nodes[i].Name == name {
				fn(&c.Nodes[i])
			}
		}
		return nil
	})
	return c
}

func nodeNamed(c Cluster, name string) (Node, bool) {
	i := slices.IndexFunc(c.Nodes, func(n Node) bool { return n.Name == name })
	if i < 0 {
		return Node{}, false
	}
	return c.Nodes[i], true
}

// bringUp creates node name's machine and waits until its cloud-init has joined it.
func (o *Orchestrator) bringUp(ctx context.Context, d Driver, id, name, token string) error {
	c, _ := o.store.Get(id)
	n, ok := nodeNamed(c, name)
	if !ok {
		return fmt.Errorf("no node %s", name)
	}
	fail := func(err error) error {
		o.setNode(id, name, func(n *Node) { n.State, n.Message = NodeFailed, err.Error() })
		return fmt.Errorf("%s: %w", name, err)
	}
	if n.UUID == "" {
		in := NodeInput{Cluster: c, Node: n, Token: token}
		if !in.first() {
			in.Endpoint = strings.TrimPrefix(c.Endpoint, "https://")
		}
		ud, err := UserData(in)
		if err != nil {
			return fail(err)
		}
		size := c.Spec.Worker
		if n.Role == RoleControlPlane {
			size = c.Spec.ControlPlane
		}
		uuid, err := d.CreateVM(ctx, n.HostID, VMRequest{Name: n.Name, Image: c.Spec.Image, StoragePool: c.Spec.StoragePool,
			Network: c.Spec.Network, IPPool: c.Spec.IPPool, Project: c.Spec.Project, UserData: ud,
			VCPUs: size.VCPUs, MemoryMiB: size.MemoryMiB, DiskGiB: size.DiskGiB})
		if err != nil {
			return fail(fmt.Errorf("create the machine: %w", err))
		}
		o.setNode(id, name, func(n *Node) { n.UUID, n.State, n.Message = uuid, NodeJoining, "" })
		n.UUID, n.State = uuid, NodeJoining
		o.store.Log(id, false, "%s created; installing Kubernetes %s", name, c.Version)
	}
	if err := o.waitState(ctx, d, n, StateFile, o.BootTimeout); err != nil {
		return fail(err)
	}
	ip, err := o.nodeIP(ctx, d, n)
	if err != nil {
		return fail(err)
	}
	o.setNode(id, name, func(n *Node) { n.IP, n.Message = ip, "" })
	if c, _ := o.store.Get(id); c.Endpoint == "" {
		_, _ = o.store.Update(id, func(c *Cluster) error { c.Endpoint = "https://" + ip + ":6443"; return nil })
	}
	o.store.Log(id, false, "%s joined the cluster (%s)", name, ip)
	return nil
}

// exec runs argv inside node n, with a deadline per call (the API between members has one).
func (o *Orchestrator) exec(ctx context.Context, d Driver, n Node, argv ...string) (ExecResult, error) {
	c, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	return d.Exec(c, n.HostID, n.UUID, argv...)
}

// waitState waits until the script writing file inside n says done; a failure brings the
// end of the node's log along.
func (o *Orchestrator) waitState(ctx context.Context, d Driver, n Node, file string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		r, err := o.exec(ctx, d, n, "/bin/cat", file)
		switch {
		case err != nil:
			last = err // booting, no agent yet
		case r.ExitCode != 0:
			last = errors.New("the setup has not started yet")
		default:
			st := strings.TrimSpace(r.Stdout)
			if st == "done" {
				return nil
			}
			if strings.HasPrefix(st, "failed") {
				tail, _ := o.exec(ctx, d, n, "/usr/bin/tail", "-n", "15", LogFile)
				return fmt.Errorf("%s (log %s inside the node):\n%s", st, LogFile, strings.TrimSpace(tail.Stdout))
			}
			last = nil
		}
		if time.Now().After(deadline) {
			if last != nil {
				return fmt.Errorf("not set up after %s: %w", timeout, last)
			}
			return fmt.Errorf("not set up after %s; see %s inside the node", timeout, LogFile)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(o.Poll):
		}
	}
}

// virtual interfaces the pod network adds; a node's own address is on none of them.
var podIfaces = []string{"cni", "flannel", "cali", "tunl", "vxlan", "docker", "kube-ipvs", "veth"}

// parseNodeIP picks the node's address from `ip -o -4 addr show scope global`.
func parseNodeIP(out string) string {
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		if len(f) < 4 || f[2] != "inet" {
			continue
		}
		if slices.ContainsFunc(podIfaces, func(p string) bool { return strings.HasPrefix(f[1], p) }) {
			continue
		}
		ip, _, _ := strings.Cut(f[3], "/")
		return ip
	}
	return ""
}

func (o *Orchestrator) nodeIP(ctx context.Context, d Driver, n Node) (string, error) {
	r, err := o.exec(ctx, d, n, "/usr/sbin/ip", "-o", "-4", "addr", "show", "scope", "global")
	if err != nil {
		return "", fmt.Errorf("read its address: %w", err)
	}
	if ip := parseNodeIP(r.Stdout); ip != "" {
		return ip, nil
	}
	return "", errors.New("it has no IPv4 address")
}

// kubectl runs kubectl as the cluster admin on the first control plane that answers.
func (o *Orchestrator) kubectl(ctx context.Context, d Driver, c Cluster, args ...string) (string, error) {
	argv := append([]string{"/usr/bin/kubectl", "--kubeconfig", "/etc/kubernetes/admin.conf"}, args...)
	var last error = errors.New("no control plane is up")
	for _, n := range c.ControlPlanes() {
		if n.UUID == "" {
			continue
		}
		r, err := o.exec(ctx, d, n, argv...)
		if err != nil {
			last = err
			continue
		}
		if r.ExitCode != 0 {
			return "", fmt.Errorf("kubectl %s: %s", args[0], strings.TrimSpace(r.Stderr+" "+r.Stdout))
		}
		return r.Stdout, nil
	}
	return "", last
}

// nodeStatus is what `kubectl get nodes -o json` says of each node.
type nodeStatus struct {
	Ready   bool
	Version string
}

func parseNodes(raw string) (map[string]nodeStatus, error) {
	var l struct {
		Items []struct {
			Metadata struct{ Name string }
			Status   struct {
				Conditions []struct{ Type, Status string }
				NodeInfo   struct {
					KubeletVersion string `json:"kubeletVersion"`
				} `json:"nodeInfo"`
			}
		}
	}
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		return nil, fmt.Errorf("read the node list: %w", err)
	}
	out := map[string]nodeStatus{}
	for _, it := range l.Items {
		st := nodeStatus{Version: it.Status.NodeInfo.KubeletVersion}
		for _, cond := range it.Status.Conditions {
			if cond.Type == "Ready" {
				st.Ready = cond.Status == "True"
			}
		}
		out[it.Metadata.Name] = st
	}
	return out, nil
}

// waitReady waits until every node of the cluster is Ready (with kubelet minor version
// want, if set) and records their versions.
func (o *Orchestrator) waitReady(ctx context.Context, d Driver, id, want string) error {
	deadline := time.Now().Add(o.ReadyTimeout)
	for {
		c, _ := o.store.Get(id)
		raw, err := o.kubectl(ctx, d, c, "get", "nodes", "-o", "json")
		var waiting []string
		if err == nil {
			var st map[string]nodeStatus
			if st, err = parseNodes(raw); err == nil {
				for _, n := range c.Nodes {
					s, ok := st[n.Name]
					o.setNode(id, n.Name, func(x *Node) {
						if ok {
							x.Version = s.Version
						}
						if ok && s.Ready && (want == "" || minorOf(s.Version) == want) {
							x.State = NodeReady
						}
					})
					if !ok || !s.Ready || (want != "" && minorOf(s.Version) != want) {
						waiting = append(waiting, n.Name)
					}
				}
				if len(waiting) == 0 {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("nodes not ready after %s: %w", o.ReadyTimeout, err)
			}
			return fmt.Errorf("not ready after %s: %s", o.ReadyTimeout, strings.Join(waiting, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(o.Poll):
		}
	}
}

// waitDNS waits until CoreDNS has a running replica: the pod network carries pods.
func (o *Orchestrator) waitDNS(ctx context.Context, d Driver, id string) error {
	deadline := time.Now().Add(o.ReadyTimeout)
	for {
		c, _ := o.store.Get(id)
		out, err := o.kubectl(ctx, d, c, "-n", "kube-system", "get", "deployment", "coredns", "-o", "jsonpath={.status.availableReplicas}")
		if err == nil && strings.TrimSpace(out) != "" && strings.TrimSpace(out) != "0" {
			return nil
		}
		if time.Now().After(deadline) {
			why := "no replica is available"
			if err != nil {
				why = err.Error()
			}
			return fmt.Errorf("the pod network does not work: CoreDNS did not start within %s (%s); see kubectl -n kube-system describe pods", o.ReadyTimeout, why)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(o.Poll):
		}
	}
}

// minorOf is 1.34 for v1.34.2.
func minorOf(v string) string {
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// ---- scaling -----------------------------------------------------------------------------

// Scale grows or shrinks the workers to count.
func (o *Orchestrator) Scale(ctx context.Context, id string, count int) error {
	if count < 0 || count > 50 {
		return invalidf("a cluster has 0 to 50 workers")
	}
	c, ok := o.store.Get(id)
	if !ok {
		return fmt.Errorf("%w: cluster %s", ErrNotFound, id)
	}
	if c.State != StateReady && c.State != StateFailed {
		return fmt.Errorf("%w: the cluster is %s", ErrBusy, c.State)
	}
	have := len(c.Nodes) - len(c.ControlPlanes())
	what := fmt.Sprintf("scaling the workers from %d to %d", have, count)
	return o.start(ctx, id, StateScaling, what, func(ctx context.Context, d Driver) error {
		return o.scale(ctx, d, id, count)
	})
}

func (o *Orchestrator) scale(ctx context.Context, d Driver, id string, count int) error {
	c, _ := o.store.Get(id)
	var workers []Node
	for _, n := range c.Nodes {
		if n.Role == RoleWorker {
			workers = append(workers, n)
		}
	}
	if count < len(workers) {
		for _, n := range slices.Backward(workers[count:]) {
			if err := o.removeNode(ctx, d, id, n); err != nil {
				return err
			}
		}
		_, _ = o.store.Update(id, func(c *Cluster) error { c.Spec.Workers = count; return nil })
		return nil
	}
	// Workers left failed by an earlier try are made again.
	for _, n := range workers {
		if n.State == NodeFailed {
			if err := o.removeNode(ctx, d, id, n); err != nil {
				return err
			}
		}
	}
	c, _ = o.store.Get(id)
	token, err := o.kubectlToken(ctx, d, c)
	if err != nil {
		return err
	}
	have := len(c.Nodes) - len(c.ControlPlanes())
	hosts := c.Spec.Hosts
	var names []string
	c, err = o.store.Update(id, func(c *Cluster) error {
		for i := have; i < count; i++ {
			name := NextWorkerName(*c)
			c.Nodes = append(c.Nodes, Node{Name: name, Role: RoleWorker, HostID: hosts[i%len(hosts)], State: NodeCreating})
			names = append(names, name)
		}
		c.Spec.Workers = count
		return nil
	})
	if err != nil {
		return err
	}
	if err := o.bringUpAll(ctx, d, id, names, token); err != nil {
		return err
	}
	return o.waitReady(ctx, d, id, "")
}

// kubectlToken makes a bootstrap token for new nodes, valid for two hours.
func (o *Orchestrator) kubectlToken(ctx context.Context, d Driver, c Cluster) (string, error) {
	for _, n := range c.ControlPlanes() {
		r, err := o.exec(ctx, d, n, "/usr/bin/kubeadm", "token", "create", "--ttl", "2h", "--description", "VirtoMir: new workers")
		if err == nil && r.ExitCode == 0 {
			if t := strings.TrimSpace(r.Stdout); len(t) == 23 {
				return t, nil
			}
		}
	}
	return "", errors.New("no control plane made a token for the new workers")
}

// removeNode drains a node, takes it out of Kubernetes and deletes its machine.
func (o *Orchestrator) removeNode(ctx context.Context, d Driver, id string, n Node) error {
	c, _ := o.store.Get(id)
	if n.State != NodeFailed && n.State != NodeCreating {
		o.setNode(id, n.Name, func(x *Node) { x.State = NodeDraining })
		if _, err := o.kubectl(ctx, d, c, "drain", n.Name, "--ignore-daemonsets", "--delete-emptydir-data", "--force", "--timeout=5m"); err != nil {
			o.store.Log(id, true, "%s not drained: %s", n.Name, err)
		}
	}
	if _, err := o.kubectl(ctx, d, c, "delete", "node", n.Name, "--ignore-not-found"); err != nil {
		o.store.Log(id, true, "%s not removed from Kubernetes: %s", n.Name, err)
	}
	if n.UUID != "" {
		if err := d.DeleteVM(ctx, n.HostID, n.UUID); err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("delete the machine of %s: %w", n.Name, err)
		}
	}
	_, _ = o.store.Update(id, func(c *Cluster) error {
		c.Nodes = slices.DeleteFunc(c.Nodes, func(x Node) bool { return x.Name == n.Name })
		return nil
	})
	o.store.Log(id, false, "%s removed", n.Name)
	return nil
}

// ---- upgrades ----------------------------------------------------------------------------

// Upgrade moves the cluster to the next minor version, one node at a time: the control
// planes first (kubeadm upgrade apply on the first, upgrade node on the rest), then the
// workers, each drained while its kubelet is replaced.
func (o *Orchestrator) Upgrade(ctx context.Context, id string) error {
	c, ok := o.store.Get(id)
	if !ok {
		return fmt.Errorf("%w: cluster %s", ErrNotFound, id)
	}
	// An upgrade that failed is tried again towards the same version.
	next := c.UpgradeTo
	if next == "" {
		next = NextVersion(c.Version)
	}
	if next == "" {
		return invalidf("Kubernetes %s is the newest version VirtoMir offers", c.Version)
	}
	if c.State != StateReady && c.State != StateFailed {
		return fmt.Errorf("%w: the cluster is %s", ErrBusy, c.State)
	}
	if _, err := o.store.Update(id, func(c *Cluster) error { c.UpgradeTo = next; return nil }); err != nil {
		return err
	}
	return o.start(ctx, id, StateUpgrading, "upgrading to "+next, func(ctx context.Context, d Driver) error {
		if err := o.upgrade(ctx, d, id, next); err != nil {
			return err
		}
		_, err := o.store.Update(id, func(c *Cluster) error { c.UpgradeTo = ""; return nil })
		return err
	})
}

// upgradeScript is one step of an upgrade inside a node, run as a systemd unit that writes
// its state like the setup does.
func upgradeScript(step, minor, body string) (unit, state, script string) {
	unit = "virtomir-k8s-" + step
	state = "/var/lib/virtomir/k8s-" + step + ".state"
	script = fmt.Sprintf(`set -Eeuo pipefail
STATE=%[1]s
mkdir -p /var/lib/virtomir
echo running >"$STATE"
trap 'echo "failed at line $LINENO: $BASH_COMMAND" >"$STATE"' ERR
exec >>%[2]s 2>&1
export DEBIAN_FRONTEND=noninteractive
retry() { for i in 1 2 3 4 5 6; do "$@" && return 0; sleep $((i * 10)); done; return 1; }
sed -i 's#/v[0-9.]*/deb/#/v%[3]s/deb/#g' /etc/apt/sources.list.d/kubernetes.list
retry curl -fsSL https://pkgs.k8s.io/core:/stable:/v%[3]s/deb/Release.key -o /tmp/kubernetes.key
gpg --dearmor --yes -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg /tmp/kubernetes.key
retry apt-get update
%[4]s
echo done >"$STATE"
`, state, LogFile, minor, body)
	return unit, state, script
}

const (
	upgradeKubeadmFirst = `apt-mark unhold kubeadm
retry apt-get install -y kubeadm
apt-mark hold kubeadm
kubeadm upgrade apply -y "$(kubeadm version -o short)"`
	upgradeKubeadmOther = `apt-mark unhold kubeadm
retry apt-get install -y kubeadm
apt-mark hold kubeadm
kubeadm upgrade node`
	upgradeKubelet = `apt-mark unhold kubelet kubectl
retry apt-get install -y kubelet kubectl
apt-mark hold kubelet kubectl
systemctl daemon-reload
systemctl restart kubelet`
)

func (o *Orchestrator) runStep(ctx context.Context, d Driver, n Node, step, minor, body string) error {
	unit, state, script := upgradeScript(step, minor, body)
	r, err := o.exec(ctx, d, n, "/usr/bin/systemd-run", "--unit="+unit, "--collect", "/bin/bash", "-c", script)
	if err != nil {
		return err
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("start %s: %s", unit, strings.TrimSpace(r.Stderr))
	}
	return o.waitState(ctx, d, n, state, o.BootTimeout)
}

func (o *Orchestrator) upgrade(ctx context.Context, d Driver, id, minor string) error {
	c, _ := o.store.Get(id)
	order := append(c.ControlPlanes(), slices.DeleteFunc(slices.Clone(c.Nodes), func(n Node) bool { return n.Role == RoleControlPlane })...)
	stepTag := strings.ReplaceAll(minor, ".", "-")
	for i, n := range order {
		if minorOf(n.Version) == minor {
			continue // done in an earlier try
		}
		o.setNode(id, n.Name, func(x *Node) { x.State = NodeUpgrade })
		o.store.Log(id, false, "upgrading %s to %s", n.Name, minor)
		body := upgradeKubeadmOther
		if i == 0 {
			body = upgradeKubeadmFirst
		}
		if err := o.runStep(ctx, d, n, "kubeadm-"+stepTag, minor, body); err != nil {
			o.setNode(id, n.Name, func(x *Node) { x.State, x.Message = NodeFailed, err.Error() })
			return fmt.Errorf("%s: kubeadm upgrade: %w", n.Name, err)
		}
		c, _ = o.store.Get(id)
		if _, err := o.kubectl(ctx, d, c, "drain", n.Name, "--ignore-daemonsets", "--delete-emptydir-data", "--force", "--timeout=5m"); err != nil {
			o.store.Log(id, true, "%s not fully drained: %s", n.Name, err)
		}
		err := o.runStep(ctx, d, n, "kubelet-"+stepTag, minor, upgradeKubelet)
		if _, uerr := o.kubectl(ctx, d, c, "uncordon", n.Name); uerr != nil {
			o.store.Log(id, true, "%s not uncordoned: %s", n.Name, uerr)
		}
		if err != nil {
			o.setNode(id, n.Name, func(x *Node) { x.State, x.Message = NodeFailed, err.Error() })
			return fmt.Errorf("%s: kubelet upgrade: %w", n.Name, err)
		}
		if i == 0 {
			_, _ = o.store.Update(id, func(c *Cluster) error { c.Version = minor; c.Spec.Version = minor; return nil })
		}
	}
	if err := o.waitReady(ctx, d, id, minor); err != nil {
		return err
	}
	return o.waitDNS(ctx, d, id)
}

// ---- deleting ----------------------------------------------------------------------------

// Delete removes the cluster's machines, its API address and placement rule, then the
// cluster. It also cleans up a cluster that failed half-way.
func (o *Orchestrator) Delete(ctx context.Context, id string) error {
	c, ok := o.store.Get(id)
	if !ok {
		return fmt.Errorf("%w: cluster %s", ErrNotFound, id)
	}
	if o.Running(id) && c.State != StateDeleting {
		return fmt.Errorf("%w: wait until the cluster is no longer %s", ErrBusy, c.State)
	}
	return o.start(ctx, id, StateDeleting, "deleting the cluster", func(ctx context.Context, d Driver) error {
		c, _ := o.store.Get(id)
		var errs []error
		for _, n := range slices.Backward(c.Nodes) {
			if n.UUID == "" {
				continue
			}
			if err := d.DeleteVM(ctx, n.HostID, n.UUID); err != nil && !errors.Is(err, ErrNotFound) {
				errs = append(errs, fmt.Errorf("%s: %w", n.Name, err))
				continue
			}
			_, _ = o.store.Update(id, func(c *Cluster) error {
				c.Nodes = slices.DeleteFunc(c.Nodes, func(x Node) bool { return x.Name == n.Name })
				return nil
			})
		}
		if len(errs) > 0 {
			return fmt.Errorf("machines not deleted, try again: %w", errors.Join(errs...))
		}
		if c.ReservedVIP != "" {
			if err := d.ReleaseIP(ctx, c.Spec.IPPool, c.ReservedVIP); err != nil && !errors.Is(err, ErrNotFound) {
				o.log.Warn("Kubernetes API address not released", "cluster", c.Spec.Name, "ip", c.ReservedVIP, "err", err)
			}
		}
		if c.RuleID != "" {
			if err := d.DeleteRule(ctx, c.RuleID); err != nil && !errors.Is(err, ErrNotFound) {
				o.log.Warn("placement rule not deleted", "cluster", c.Spec.Name, "rule", c.RuleID, "err", err)
			}
		}
		if err := o.store.Delete(id); err != nil {
			return err
		}
		return errDeleted
	})
}
