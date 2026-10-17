// A tiny pretend shell behind the demo's serial console: a login banner, a prompt and a few
// commands with plausible output, so the console tab can be tried without a real guest.

import type { DemoMachine } from './world'

const OS: Record<DemoMachine['os'], { pretty: string; kernel: string }> = {
  debian: { pretty: 'Debian GNU/Linux 13 (trixie)', kernel: '6.12.48-1-amd64' },
  ubuntu: { pretty: 'Ubuntu 24.04.3 LTS', kernel: '6.8.0-85-generic' },
  rocky: { pretty: 'Rocky Linux 10.0 (Red Quartz)', kernel: '6.12.0-55.el10.x86_64' },
  alpine: { pretty: 'Alpine Linux v3.22', kernel: '6.12.40-0-virt' },
  windows: { pretty: 'Windows (Special Administration Console)', kernel: '' },
}

/** Starts the shell; returns the function that takes the user's keystrokes. */
export function serialShell(m: DemoMachine, write: (s: string) => void): (input: string) => void {
  const os = OS[m.os]
  const out = (s: string) => write(s.replace(/\n/g, '\r\n'))
  const prompt = () => out(`\x1b[32mroot@${m.name}\x1b[0m:\x1b[34m~\x1b[0m# `)
  let line = ''
  let greeted = false

  if (m.os === 'windows') {
    out('\nComputer is booting, SAC started and initialized.\n\nUse the "ch -?" command for information about using channels.\nUse the "?" command for general help.\n\n')
    out('\x1b[2m(VirtoMir demo: this is a simulated console.)\x1b[0m\n\nSAC>')
    return (s) => {
      if (s.includes('\r')) out('\nThe demo cannot run Windows commands. On a real host this is the live serial console.\n\nSAC>')
      else out(s)
    }
  }

  const commands: Record<string, () => string> = {
    help: () =>
      'This is VirtoMir\'s serial console, running against a simulated machine.\nTry: uname -a, uptime, free -h, df -h, ip -br a, systemctl status, cat /etc/os-release, clear, exit\n',
    'uname -a': () => `Linux ${m.name} ${os.kernel} #1 SMP PREEMPT_DYNAMIC x86_64 GNU/Linux\n`,
    uptime: () => ` ${new Date().toTimeString().slice(0, 8)} up 12 days,  3:41,  1 user,  load average: ${((m.cpu / 100) * m.vcpus).toFixed(2)}, 0.84, 0.77\n`,
    'free -h': () => {
      const total = m.memoryMiB / 1024
      const used = total * m.memUsedFrac
      return `               total        used        free      shared  buff/cache   available\nMem:           ${total.toFixed(1)}Gi       ${used.toFixed(1)}Gi       ${(total * 0.12).toFixed(1)}Gi        12Mi       ${(total - used - total * 0.12).toFixed(1)}Gi       ${(total - used).toFixed(1)}Gi\nSwap:             0B          0B          0B\n`
    },
    'df -h': () =>
      `Filesystem      Size  Used Avail Use% Mounted on\n/dev/vda1       ${m.diskGiB - 1}G   ${Math.round(m.diskGiB * 0.43)}G   ${Math.round(m.diskGiB * 0.53)}G  45% /\n/dev/vda15      124M   12M  113M  10% /boot/efi\n`,
    'ip -br a': () => `lo               UNKNOWN        127.0.0.1/8 ::1/128\nenp1s0           UP             ${m.ip ?? '192.168.122.99'}/24 fe80::5054:ff:fe3a:1101/64\n`,
    'ip a': () => commands['ip -br a'](),
    'cat /etc/os-release': () => `PRETTY_NAME="${os.pretty}"\nNAME="${os.pretty.split(' ')[0]}"\nID=${m.os}\n`,
    'systemctl status': () =>
      `● ${m.name}\n    State: running\n    Units: 241 loaded (incl. loaded aliases)\n     Jobs: 0 queued\n   Failed: 0 units\n    Since: 12 days ago\n`,
    hostname: () => `${m.name}\n`,
    whoami: () => 'root\n',
    ls: () => '\x1b[34msnap\x1b[0m  notes.txt\n',
    'cat notes.txt': () => 'Everything here is make-believe: VirtoMir\'s online demo has no real machines.\nInstall it on a Linux host to drive real KVM guests: https://github.com/onurbaydas/VirtoMir\n',
    pwd: () => '/root\n',
    date: () => new Date().toString() + '\n',
  }

  function run(cmd: string) {
    cmd = cmd.trim().replace(/\s+/g, ' ')
    if (!cmd) return
    if (cmd === 'clear') return out('\x1b[2J\x1b[H')
    if (cmd === 'exit' || cmd === 'logout') return out(`logout\n\n${os.pretty} ${m.name} ttyS0\n\n${m.name} login: root (automatic login)\n\n`)
    const f = commands[cmd]
    out(f ? f() : `-bash: ${cmd.split(' ')[0]}: command not found (the demo knows only a few; type help)\n`)
  }

  return (s) => {
    for (const ch of s) {
      if (ch === '\r' || ch === '\n') {
        out('\n')
        if (!greeted) {
          greeted = true
          out(`${os.pretty} ${m.name} ttyS0\n\n${m.name} login: root (automatic login)\n\n\x1b[2mVirtoMir demo — a simulated serial console. Type help.\x1b[0m\n\n`)
        } else run(line)
        line = ''
        prompt()
      } else if (ch === '\x7f' || ch === '\b') {
        if (line) {
          line = line.slice(0, -1)
          out('\b \b')
        }
      } else if (ch === '\x03') {
        line = ''
        out('^C\n')
        prompt()
      } else if (ch >= ' ') {
        line += ch
        out(ch)
      }
    }
  }
}
