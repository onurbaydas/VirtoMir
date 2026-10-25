packer {
  required_plugins {
    virtomir = {
      source  = "github.com/onurbaydas/virtomir"
      version = ">= 0.1.0"
    }
  }
}

variable "url" {
  type    = string
  default = env("VIRTOMIR_URL")
}

variable "token" {
  type      = string
  default   = env("VIRTOMIR_TOKEN")
  sensitive = true
}

source "virtomir" "debian13" {
  url           = var.url
  token         = var.token
  insecure      = true # the host's self-signed certificate
  template_name = "debian-13-base"
  base_image    = "/var/lib/libvirt/images/debian-13-genericcloud-amd64.qcow2"
  storage_pool  = "images"
  disk_gib      = 20
  network       = "default"
  ssh_username  = "packer" # made by cloud-init, with a key Packer generates
}

build {
  sources = ["source.virtomir.debian13"]

  provisioner "shell" {
    inline = [
      "sudo apt-get update",
      "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y nginx",
      # Machines deployed from the template run cloud-init again with their own seed.
      "sudo cloud-init clean --logs --machine-id",
      "sudo userdel -r packer || true",
    ]
  }
}
