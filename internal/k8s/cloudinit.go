package k8s

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

// The node's cloud-init installs the guest agent, writes the cluster files and starts
// /usr/local/sbin/virtomir-k8s as a systemd unit, so cloud-init finishes at once and the
// orchestrator follows the script through its state file.
const (
	StateFile = "/var/lib/virtomir/k8s-bootstrap.state"
	LogFile   = "/var/log/virtomir-k8s.log"
)

// NodeInput is what one node's cloud-init needs.
type NodeInput struct {
	Cluster Cluster
	Node    Node
	// Endpoint is host:port of the API for nodes that join ("" for the first control plane).
	Endpoint string
	Token    string
}

func (in NodeInput) first() bool {
	cps := in.Cluster.ControlPlanes()
	return in.Node.Role == RoleControlPlane && len(cps) > 0 && cps[0].Name == in.Node.Name
}

func (in NodeInput) vip() string {
	if in.Cluster.Spec.ControlPlanes > 1 {
		return in.Cluster.APIAddress()
	}
	return ""
}

// APIAddress is the address every node reaches the API on when it is not one control
// plane's: the given VIP or the one reserved from the pool.
func (c Cluster) APIAddress() string {
	if c.Spec.VIP != "" {
		return c.Spec.VIP
	}
	return c.ReservedVIP
}

// shared prelude: state file, log, retries and the packages every node needs.
const bootstrapScript = `#!/bin/bash
# Written by VirtoMir: sets up node {{.Node}} of Kubernetes cluster {{.Cluster}}.
set -Eeuo pipefail
STATE={{.StateFile}}
mkdir -p "$(dirname "$STATE")"
if [ -f "$STATE" ] && grep -qx done "$STATE"; then exit 0; fi
echo running >"$STATE"
trap 'echo "failed at line $LINENO: $BASH_COMMAND" >"$STATE"' ERR
exec >>{{.LogFile}} 2>&1
export DEBIAN_FRONTEND=noninteractive
retry() { for i in 1 2 3 4 5 6; do "$@" && return 0; sleep $((i * 10)); done; return 1; }

printf 'overlay\nbr_netfilter\n' >/etc/modules-load.d/k8s.conf
modprobe overlay
modprobe br_netfilter
printf 'net.bridge.bridge-nf-call-iptables = 1\nnet.bridge.bridge-nf-call-ip6tables = 1\nnet.ipv4.ip_forward = 1\n' >/etc/sysctl.d/k8s.conf
sysctl --system >/dev/null
swapoff -a
sed -i '/\sswap\s/d' /etc/fstab

retry apt-get update
retry apt-get install -y apt-transport-https ca-certificates curl gpg containerd
mkdir -p /etc/containerd
containerd config default >/etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
# Debian's containerd looks for CNI plugins in /usr/lib/cni; kubernetes-cni and the pod
# network put them in /opt/cni/bin.
sed -i 's#/usr/lib/cni#/opt/cni/bin#g' /etc/containerd/config.toml
systemctl restart containerd

mkdir -p /etc/apt/keyrings
retry curl -fsSL https://pkgs.k8s.io/core:/stable:/v{{.Minor}}/deb/Release.key -o /tmp/kubernetes.key
gpg --dearmor --yes -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg /tmp/kubernetes.key
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v{{.Minor}}/deb/ /' >/etc/apt/sources.list.d/kubernetes.list
retry apt-get update
retry apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl
systemctl enable kubelet
IFACE=$(ip -o -4 addr show scope global | awk '{print $2; exit}')
{{- if .First}}

sed -i "s/@VERSION@/$(kubeadm version -o short)/" /etc/virtomir/kubeadm.yaml
{{- if .VIP}}
sed "s/@IFACE@/$IFACE/; s#@KUBECONFIG@#/etc/kubernetes/super-admin.conf#" /etc/virtomir/kube-vip.yaml >/etc/kubernetes/manifests/kube-vip.yaml
{{- end}}
kubeadm init --config /etc/virtomir/kubeadm.yaml
{{- if .VIP}}
sed -i 's#/etc/kubernetes/super-admin.conf#/etc/kubernetes/admin.conf#' /etc/kubernetes/manifests/kube-vip.yaml
{{- end}}
export KUBECONFIG=/etc/kubernetes/admin.conf
retry curl -fsSL {{.CNIURL}} -o /tmp/cni.yaml
{{- if .Flannel}}
sed -i 's#10.244.0.0/16#{{.PodCIDR}}#' /tmp/cni.yaml
{{- end}}
retry kubectl apply -f /tmp/cni.yaml
{{- else if .ControlPlane}}

for i in 1 2 3; do
  if kubeadm join {{.Endpoint}} --token {{.Token}} --discovery-token-ca-cert-hash {{.CAHash}} --control-plane --node-name {{.Node}}; then break; fi
  kubeadm reset -f || true
  sleep 30
done
test -f /etc/kubernetes/admin.conf
{{- if .VIP}}
sed "s/@IFACE@/$IFACE/; s#@KUBECONFIG@#/etc/kubernetes/admin.conf#" /etc/virtomir/kube-vip.yaml >/etc/kubernetes/manifests/kube-vip.yaml
{{- end}}
{{- else}}

# The API may not answer yet: keep trying for ten minutes.
for i in $(seq 1 60); do
  if kubeadm join {{.Endpoint}} --token {{.Token}} --discovery-token-ca-cert-hash {{.CAHash}} --node-name {{.Node}}; then break; fi
  kubeadm reset -f || true
  sleep 10
done
test -f /etc/kubernetes/kubelet.conf
{{- end}}
echo done >"$STATE"
`

const kubeadmConfig = `apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
bootstrapTokens:
- token: "{{.Token}}"
  description: "VirtoMir: the nodes made with the cluster"
  ttl: 24h0m0s
nodeRegistration:
  name: {{.Node}}
  criSocket: unix:///run/containerd/containerd.sock
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
clusterName: {{.Cluster}}
kubernetesVersion: "@VERSION@"
{{- if .VIP}}
controlPlaneEndpoint: "{{.VIP}}:6443"
{{- end}}
networking:
  podSubnet: {{.PodCIDR}}
  serviceSubnet: {{.ServiceCIDR}}
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
cgroupDriver: systemd
`

const kubeVIPManifest = `apiVersion: v1
kind: Pod
metadata:
  name: kube-vip
  namespace: kube-system
spec:
  containers:
  - name: kube-vip
    image: {{.KubeVIPImage}}
    imagePullPolicy: IfNotPresent
    args: ["manager"]
    env:
    - {name: vip_arp, value: "true"}
    - {name: port, value: "6443"}
    - name: vip_nodename
      valueFrom:
        fieldRef:
          fieldPath: spec.nodeName
    - {name: vip_interface, value: "@IFACE@"}
    - {name: vip_subnet, value: "32"}
    - {name: cp_enable, value: "true"}
    - {name: cp_namespace, value: kube-system}
    - {name: vip_leaderelection, value: "true"}
    - {name: vip_leasename, value: plndr-cp-lock}
    - {name: vip_leaseduration, value: "15"}
    - {name: vip_renewdeadline, value: "10"}
    - {name: vip_retryperiod, value: "2"}
    - {name: address, value: "{{.VIP}}"}
    securityContext:
      capabilities:
        add: [NET_ADMIN, NET_RAW]
        drop: [ALL]
    volumeMounts:
    - mountPath: /etc/kubernetes/admin.conf
      name: kubeconfig
  hostAliases:
  - hostnames: [kubernetes]
    ip: 127.0.0.1
  hostNetwork: true
  volumes:
  - name: kubeconfig
    hostPath:
      path: "@KUBECONFIG@"
`

type tmplData struct {
	Cluster, Node, Minor, StateFile, LogFile string
	First, ControlPlane, Flannel             bool
	Endpoint, Token, CAHash, VIP             string
	CNIURL, PodCIDR, ServiceCIDR             string
	KubeVIPImage                             string
}

func render(name, text string, d tmplData) (string, error) {
	t, err := template.New(name).Parse(text)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = t.Execute(&b, d)
	return b.String(), err
}

type file struct {
	path, perm, content string
}

// UserData is the node's #cloud-config.
func UserData(in NodeInput) (string, error) {
	c, n := in.Cluster, in.Node
	hash, err := c.PKI.CAHash()
	if err != nil {
		return "", err
	}
	d := tmplData{
		Cluster: c.Spec.Name, Node: n.Name, Minor: c.Version, StateFile: StateFile, LogFile: LogFile,
		First: in.first(), ControlPlane: n.Role == RoleControlPlane, Flannel: c.Spec.CNI == CNIFlannel,
		Endpoint: in.Endpoint, Token: in.Token, CAHash: hash, VIP: in.vip(),
		CNIURL: cniManifests[c.Spec.CNI], PodCIDR: c.Spec.PodCIDR, ServiceCIDR: c.Spec.ServiceCIDR,
		KubeVIPImage: KubeVIPImage,
	}
	if !d.First && in.Endpoint == "" {
		return "", fmt.Errorf("node %s joins, but the API endpoint is not known", n.Name)
	}
	script, err := render("bootstrap", bootstrapScript, d)
	if err != nil {
		return "", err
	}
	files := []file{{"/usr/local/sbin/virtomir-k8s", "0700", script}}
	if d.ControlPlane {
		p := c.PKI
		files = append(files,
			file{"/etc/kubernetes/pki/ca.crt", "0644", p.CACert},
			file{"/etc/kubernetes/pki/ca.key", "0600", p.CAKey},
			file{"/etc/kubernetes/pki/front-proxy-ca.crt", "0644", p.FrontProxyCACert},
			file{"/etc/kubernetes/pki/front-proxy-ca.key", "0600", p.FrontProxyKey},
			file{"/etc/kubernetes/pki/etcd/ca.crt", "0644", p.EtcdCACert},
			file{"/etc/kubernetes/pki/etcd/ca.key", "0600", p.EtcdCAKey},
			file{"/etc/kubernetes/pki/sa.key", "0600", p.SAKey},
			file{"/etc/kubernetes/pki/sa.pub", "0644", p.SAPub},
		)
		if d.VIP != "" {
			m, err := render("kube-vip", kubeVIPManifest, d)
			if err != nil {
				return "", err
			}
			files = append(files, file{"/etc/virtomir/kube-vip.yaml", "0644", m})
		}
	}
	if d.First {
		cfg, err := render("kubeadm", kubeadmConfig, d)
		if err != nil {
			return "", err
		}
		files = append(files, file{"/etc/virtomir/kubeadm.yaml", "0600", cfg})
	}

	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	fmt.Fprintf(&b, "hostname: %s\nfqdn: %s\nmanage_etc_hosts: true\n", q(n.Name), q(n.Name))
	if len(c.Spec.SSHKeys) > 0 {
		b.WriteString("users:\n  - name: kube\n    groups: [sudo]\n    sudo: \"ALL=(ALL) NOPASSWD:ALL\"\n    shell: /bin/bash\n    lock_passwd: true\n    ssh_authorized_keys:\n")
		for _, k := range c.Spec.SSHKeys {
			fmt.Fprintf(&b, "      - %s\n", q(k))
		}
	}
	b.WriteString("ssh_pwauth: false\npackage_update: true\npackages:\n  - qemu-guest-agent\nwrite_files:\n")
	for _, f := range files {
		fmt.Fprintf(&b, "  - path: %s\n    permissions: %q\n    encoding: b64\n    content: %s\n", f.path, f.perm,
			base64.StdEncoding.EncodeToString([]byte(f.content)))
	}
	b.WriteString("runcmd:\n  - [systemctl, enable, --now, qemu-guest-agent]\n")
	b.WriteString("  - [systemd-run, --unit=virtomir-k8s, --collect, /usr/local/sbin/virtomir-k8s]\n")
	return b.String(), nil
}
