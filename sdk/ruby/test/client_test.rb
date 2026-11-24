# frozen_string_literal: true

require "minitest/autorun"
require "socket"
require_relative "../lib/virtomir"

# A one-thread HTTP server that records requests and answers with a fixed reply.
class StubServer
  attr_reader :seen, :auth, :bodies
  attr_accessor :status, :reply

  def initialize
    @server = TCPServer.new("127.0.0.1", 0)
    @seen = []
    @auth = []
    @bodies = []
    @status = 200
    @reply = ""
    @thread = Thread.new { loop { handle(@server.accept) } }
  end

  def url
    "http://127.0.0.1:#{@server.addr[1]}/"
  end

  def close
    @thread.kill
    @server.close
  end

  private

  def handle(sock)
    line = sock.gets.to_s
    headers = {}
    while (h = sock.gets) && h != "\r\n"
      k, v = h.split(":", 2)
      headers[k.downcase] = v.to_s.strip
    end
    body = headers["content-length"] ? sock.read(headers["content-length"].to_i) : ""
    m, path, = line.split(" ")
    @seen << "#{m} #{path}"
    @auth << headers["authorization"]
    @bodies << body
    sock.write("HTTP/1.1 #{@status} X\r\nContent-Type: application/json\r\nContent-Length: #{@reply.bytesize}\r\nConnection: close\r\n\r\n#{@reply}")
    sock.close
  end
end

class ClientTest < Minitest::Test
  U = "6f1c2a5e-0000-4000-8000-000000000001"

  def setup
    @srv = StubServer.new
  end

  def teardown
    @srv.close
  end

  def test_lists_machines_with_the_token
    @srv.reply = JSON.generate(domains: [{ uuid: U, name: "web", state: "running", vcpus: 2 }])
    c = VirtoMir::Client.new(@srv.url, token: "tok")
    list = c.machines
    assert_equal "web", list.first["name"]
    assert VirtoMir.running?(list.first)
    assert_equal "Bearer tok", @srv.auth.first
    assert_equal "GET /api/v1/domains", @srv.seen.first
    @srv.reply = '{"domains":null}'
    assert_equal [], c.machines
    assert_nil c.find_machine("web")
  end

  def test_routes_through_a_cluster_member
    @srv.status = 204
    c = VirtoMir::Client.new(@srv.url, token: "tok", host: "hv2")
    c.power(U, :force_off)
    c.autostart(U, false)
    c.revert_snapshot(U, "before upgrade", safety: false)
    c.delete_machine(U, remove_storage: true)
    assert_equal [
      "POST /api/v1/hosts/hv2/domains/#{U}/actions/force-off",
      "PUT /api/v1/hosts/hv2/domains/#{U}/autostart",
      "POST /api/v1/hosts/hv2/domains/#{U}/snapshots/before%20upgrade/revert?safety=false",
      "DELETE /api/v1/hosts/hv2/domains/#{U}?storage=remove"
    ], @srv.seen
    assert_equal '{"enabled":false}', @srv.bodies[1]
    assert_raises(ArgumentError) { c.power(U, :explode) }
  end

  def test_creates_a_snapshot
    @srv.status = 201
    @srv.reply = '{"name":"before-upgrade"}'
    assert_equal "before-upgrade", VirtoMir::Client.new(@srv.url).create_snapshot(U, "before-upgrade")
  end

  def test_an_error_answer_carries_the_message
    @srv.status = 404
    @srv.reply = '{"error":"machine not found"}'
    e = assert_raises(VirtoMir::Error) { VirtoMir::Client.new(@srv.url).machine(U) }
    assert e.not_found?
    assert_match(/machine not found/, e.message)
  end

  def test_refuses_a_bad_url
    assert_raises(ArgumentError) { VirtoMir::Client.new("hv1:8420") }
  end
end
