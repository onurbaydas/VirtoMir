# check_virtomir

A monitoring plugin for Nagios Core/XI, Icinga 2, Naemon, Checkmk, Shinken, Opsview and anything
else that runs a check and reads its exit code (0 OK, 1 WARNING, 2 CRITICAL, 3 UNKNOWN) and its
output (`STATUS - text | perfdata`).

## Build

```sh
go build -o check_virtomir ./contrib/check_virtomir
install -m 0755 check_virtomir /usr/lib/nagios/plugins/   # or /usr/lib64/nagios/plugins
```

One static binary, no dependencies.

## Checks

| Mode | Checks | WARNING / CRITICAL |
|---|---|---|
| `--host` | libvirt reachable, host CPU and memory | CPU or memory ≥ `-w` / `-c` %; CRITICAL when libvirt is unreachable |
| `--machine NAME` | the machine (name or UUID) runs; its CPU | CPU ≥ `-w` / `-c` %; CRITICAL when it does not run |
| `--pools` | how full each storage pool is | ≥ `-w` / `-c` % (default 85 / 95); an inactive pool is WARNING |
| `--cluster` | cluster members reachable; HA quorum | a member down: WARNING (half or more: CRITICAL); no quorum: CRITICAL |
| `--alerts` | VirtoMir's own open alerts | open warnings: WARNING; open critical alerts: CRITICAL (acknowledged ones are listed, not counted) |
| `--checkmk` | Checkmk special agent output | see below |

Common options: `--url https://host:8420`, `--token` (or `VIRTOMIR_TOKEN`; an API token of a
viewer is enough: *Account → API tokens*), `--fingerprint <sha256>` to pin VirtoMir's
self-signed certificate (*Host settings* shows it), or `--insecure` on a trusted network, and
`--timeout 15s`. Any failure to ask (network, certificate, token) is UNKNOWN, never OK.

```
$ check_virtomir --url https://hv-01:8420 --fingerprint 3f…9a --host -w 80 -c 95
VIRTOMIR WARNING - CPU 42 %, memory 87 %, 12 of 14 machines running | 'cpu'=42.0%;80;95;0;100 'memory'=87.0%;80;95;0;100 machines_running=12;;;0;14
```

## Icinga 2

```
object CheckCommand "virtomir" {
  command = [ PluginDir + "/check_virtomir" ]
  arguments = {
    "--url"         = "$virtomir_url$"
    "--fingerprint" = "$virtomir_fingerprint$"
    "--host"        = { set_if = "$virtomir_host$" }
    "--machine"     = "$virtomir_machine$"
    "--pools"       = { set_if = "$virtomir_pools$" }
    "--cluster"     = { set_if = "$virtomir_cluster$" }
    "--alerts"      = { set_if = "$virtomir_alerts$" }
    "-w"            = "$virtomir_warning$"
    "-c"            = "$virtomir_critical$"
  }
  env.VIRTOMIR_TOKEN = "$virtomir_token$"
}

apply Service "virtomir-host" {
  check_command = "virtomir"
  vars.virtomir_url = "https://" + host.address + ":8420"
  vars.virtomir_host = true
  vars.virtomir_warning = 80
  vars.virtomir_critical = 95
  assign where host.vars.virtomir
}
```

## Nagios / Naemon

```
define command {
  command_name  check_virtomir
  command_line  $USER1$/check_virtomir --url https://$HOSTADDRESS$:8420 --token $USER10$ $ARG1$
}
define service {
  use                  generic-service
  host_name            hv-01
  service_description  VirtoMir pools
  check_command        check_virtomir!--pools -w 85 -c 95
}
```

## Checkmk

`--checkmk` writes a special agent's output: the host's own checks as local checks, and each
machine as a **piggyback host** (`<<<<web-01>>>>`) with its state and CPU, so every machine
becomes a Checkmk host of its own. Use it as an *Individual program call instead of agent
access* (*Setup → Agents → Other integrations*):

```
check_virtomir --url https://$_HOSTADDRESS_4$:8420 --token <token> --fingerprint <sha256> --checkmk
```

and create hosts for the machine names (or let a dynamic host management rule do it).
