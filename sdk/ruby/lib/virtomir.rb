# frozen_string_literal: true

require "json"
require "net/http"
require "openssl"
require "uri"
require_relative "virtomir/version"

# Client for the VirtoMir API (/api/v1). Sign in with an API token made on the Account page.
#
#   vm = VirtoMir::Client.new("https://hv1:8420", token: ENV["VIRTOMIR_TOKEN"])
#   vm.machines.each { |m| puts "#{m['name']} #{m['state']}" }
module VirtoMir
  POWER_ACTIONS = %w[start shutdown reboot force-off pause resume reset save].freeze

  # An error answer of the API.
  class Error < StandardError
    attr_reader :status

    def initialize(status, message)
      @status = status
      super("VirtoMir API #{status}: #{message}")
    end

    def not_found?
      status == 404
    end
  end

  # Whether a machine's state counts as on (running, paused, ...).
  def self.running?(machine)
    !["", "shutoff", "crashed", "pmsuspended"].include?(machine["state"].to_s)
  end

  class Client
    attr_reader :base_url
    # Route every call to this cluster member: /api/v1/hosts/<host>/...
    attr_accessor :host

    # base_url looks like https://hv1:8420. ca_file trusts a self-signed host certificate;
    # verify: false skips the check (only for a lab).
    def initialize(base_url, token: nil, host: nil, ca_file: nil, verify: true, timeout: 120)
      uri = begin
        URI.parse(base_url)
      rescue URI::InvalidURIError
        nil
      end
      unless uri.is_a?(URI::HTTP) && uri.host && !uri.host.empty?
        raise ArgumentError, "url must look like https://host:8420, not #{base_url.inspect}"
      end

      @base_url = base_url.sub(%r{/+\z}, "")
      @uri = URI.parse(@base_url)
      @token = token
      @host = host
      @ca_file = ca_file
      @verify = verify
      @timeout = timeout
    end

    # Sends one request; body (if not nil) is sent as JSON. Returns the parsed answer, or nil when empty.
    def request(method, path, body = nil)
      uri = URI.parse(@base_url + route(path))
      req = Net::HTTPGenericRequest.new(method.to_s.upcase, !body.nil?, true, uri.request_uri)
      req["Accept"] = "application/json"
      req["Authorization"] = "Bearer #{@token}" if @token && !@token.empty?
      unless body.nil?
        req["Content-Type"] = "application/json"
        req.body = JSON.generate(body)
      end
      res = connection.request(req)
      text = res.body.to_s
      code = res.code.to_i
      raise Error.new(code, error_message(text, res.message)) if code >= 300

      text.strip.empty? ? nil : JSON.parse(text)
    end

    # ---- machines ---------------------------------------------------------------------

    def machines
      (request(:get, "/api/v1/domains") || {})["domains"] || []
    end

    # The machine page's full model of one machine.
    def machine(uuid)
      request(:get, "/api/v1/domains/#{seg(uuid)}")
    end

    # Finds a machine by name or UUID; nil when there is none.
    def find_machine(name_or_uuid)
      machines.find { |m| m["name"] == name_or_uuid || m["uuid"].to_s.casecmp?(name_or_uuid) }
    end

    def power(uuid, action)
      action = action.to_s.tr("_", "-")
      raise ArgumentError, "unknown action #{action}; one of #{POWER_ACTIONS.join(', ')}" unless POWER_ACTIONS.include?(action)

      request(:post, "/api/v1/domains/#{seg(uuid)}/actions/#{action}")
      nil
    end

    def autostart(uuid, enabled)
      request(:put, "/api/v1/domains/#{seg(uuid)}/autostart", { enabled: enabled })
      nil
    end

    # Deletes a machine; with remove_storage its disks go too.
    def delete_machine(uuid, remove_storage: false)
      request(:delete, "/api/v1/domains/#{seg(uuid)}#{remove_storage ? '?storage=remove' : ''}")
      nil
    end

    # ---- snapshots --------------------------------------------------------------------

    def snapshots(uuid)
      (request(:get, "/api/v1/domains/#{seg(uuid)}/snapshots") || {})["snapshots"] || []
    end

    # Takes a snapshot and returns its name (an empty name lets VirtoMir pick one).
    def create_snapshot(uuid, name = "", description: "")
      r = request(:post, "/api/v1/domains/#{seg(uuid)}/snapshots", { name: name, description: description })
      (r || {})["name"].to_s
    end

    # Goes back to a snapshot. With safety the current state is snapshotted first.
    def revert_snapshot(uuid, name, safety: true)
      request(:post, "/api/v1/domains/#{seg(uuid)}/snapshots/#{seg(name)}/revert#{safety ? '' : '?safety=false'}")
    end

    def delete_snapshot(uuid, name)
      request(:delete, "/api/v1/domains/#{seg(uuid)}/snapshots/#{seg(name)}")
      nil
    end

    private

    def seg(s)
      URI.encode_www_form_component(s.to_s).gsub("+", "%20")
    end

    def route(path)
      return path unless @host && !@host.to_s.empty? && path.start_with?("/api/v1/")

      "/api/v1/hosts/#{seg(@host)}#{path.delete_prefix('/api/v1')}"
    end

    def connection
      http = Net::HTTP.new(@uri.host, @uri.port)
      http.open_timeout = 30
      http.read_timeout = @timeout
      if @uri.scheme == "https"
        http.use_ssl = true
        http.ca_file = @ca_file if @ca_file
        http.verify_mode = @verify ? OpenSSL::SSL::VERIFY_PEER : OpenSSL::SSL::VERIFY_NONE
      end
      http
    end

    def error_message(text, fallback)
      msg = text.strip
      begin
        e = JSON.parse(text)
        msg = e["error"] if e.is_a?(Hash) && e["error"].is_a?(String) && !e["error"].empty?
      rescue JSON::ParserError
        # not JSON
      end
      msg.empty? ? fallback.to_s : msg
    end
  end
end
