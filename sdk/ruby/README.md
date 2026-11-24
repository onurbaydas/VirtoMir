# virtomir (Ruby)

A small client for the [VirtoMir](https://github.com/onurbaydas/VirtoMir) API (`/api/v1`): machines, power, autostart and snapshots, with `request` for everything else. Ruby 3.0+, no dependencies.

## Install

The gem is on GitHub Packages (a GitHub token with `read:packages` is needed, even for public packages):

```ruby
# Gemfile
source "https://rubygems.pkg.github.com/onurbaydas" do
  gem "virtomir"
end
```

```
bundle config https://rubygems.pkg.github.com/onurbaydas <github-user>:<token>
bundle install
```

## Use

Make an API token on the **Account** page of VirtoMir.

```ruby
require "virtomir"

vm = VirtoMir::Client.new("https://hv1:8420", token: ENV["VIRTOMIR_TOKEN"])
vm.machines.each { |m| puts "#{m['name']} #{m['state']}" }

if (web = vm.find_machine("web"))
  vm.create_snapshot(web["uuid"], "before-upgrade")
  vm.power(web["uuid"], :reboot)
end

pools = vm.request(:get, "/api/v1/pools")
```

`host: "hv2"` sends every call to a cluster member through this host. `ca_file:` trusts a self-signed host certificate. Errors are `VirtoMir::Error` with `status` and the API's message.
