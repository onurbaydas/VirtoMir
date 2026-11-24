# frozen_string_literal: true

require_relative "lib/virtomir/version"

Gem::Specification.new do |s|
  s.name        = "virtomir"
  s.version     = VirtoMir::VERSION
  s.summary     = "Client for the VirtoMir API (/api/v1)"
  s.description = "Machines, power, autostart and snapshots on a VirtoMir host, with a request method for everything else."
  s.authors     = ["VirtoMir contributors"]
  s.license     = "MPL-2.0"
  s.homepage    = "https://github.com/onurbaydas/VirtoMir"
  s.files       = Dir["lib/**/*.rb"] + %w[README.md LICENSE]
  s.required_ruby_version = ">= 3.0"
  s.metadata = {
    "source_code_uri" => "https://github.com/onurbaydas/VirtoMir/tree/main/sdk/ruby",
    "github_repo" => "ssh://github.com/onurbaydas/VirtoMir",
    "rubygems_mfa_required" => "true"
  }
end
