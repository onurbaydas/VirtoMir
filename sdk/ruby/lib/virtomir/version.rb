# frozen_string_literal: true

module VirtoMir
  # Set from the release tag when the gem is built (VIRTOMIR_GEM_VERSION).
  VERSION = ENV.fetch("VIRTOMIR_GEM_VERSION", "0.0.0.dev")
end
