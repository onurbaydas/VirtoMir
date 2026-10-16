# A NAT network with IPv4 and IPv6, a directory pool, a machine from a template and one
# from scratch. terraform init && terraform apply (with VIRTOMIR_URL / VIRTOMIR_TOKEN set).
terraform {
  required_providers {
    virtomir = { source = "onurbaydas/virtomir" }
  }
}

provider "virtomir" {
  insecure = true
}

data "virtomir_host" "this" {}

resource "virtomir_network" "lab" {
  name    = "lab"
  mode    = "nat"
  subnet  = "192.168.150.0/24"
  subnet6 = "fd42:150:150:1::/64"
}

resource "virtomir_pool" "fast" {
  name = "fast"
  path = "/srv/vm-fast"
}

resource "virtomir_machine" "web" {
  count     = 2
  name      = "web-${count.index + 1}"
  template  = "deb13-tpl"
  linked    = true
  vcpus     = 2
  memory_mb = 2048
  autostart = true
  cloud_init = {
    user     = "ops"
    ssh_keys = [file("~/.ssh/id_ed25519.pub")]
  }
}

resource "virtomir_machine" "db" {
  name      = "db-1"
  os        = "linux"
  vcpus     = 4
  memory_mb = 8192
  pool      = virtomir_pool.fast.name
  disk_gb   = 100
  iso       = "/var/lib/libvirt/images/debian-13-netinst.iso"
  network   = virtomir_network.lab.name
}

output "host" {
  value = "${data.virtomir_host.this.hostname}: libvirt ${data.virtomir_host.this.libvirt_version}, ${data.virtomir_host.this.cpus} CPUs"
}
