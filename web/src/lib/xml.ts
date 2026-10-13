// Minimal XML syntax highlighter for libvirt definitions: escapes everything first, then
// wraps tags, attribute names, values and comments in spans. Output is safe for {@html}.

const esc = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')

const TOKEN = /(<!--[\s\S]*?-->)|(<\/?)([\w:.-]+)((?:\s+[\w:.-]+\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*(\/?>)|([^<]+)/g
const ATTR = /([\w:.-]+)(\s*=\s*)("[^"]*"|'[^']*')/g

export function highlightXML(xml: string): string[] {
  let out = ''
  for (const m of xml.matchAll(TOKEN)) {
    const [, comment, open, name, attrs, close, text] = m
    if (comment) out += `<span class="x-c">${esc(comment)}</span>`
    else if (name) {
      const a = (attrs ?? '').replace(ATTR, '\u0000$1\u0001$2\u0002$3\u0003')
      const attrsHTML = esc(a)
        .replace(/\u0000/g, '<span class="x-a">')
        .replace(/\u0001/g, '</span>')
        .replace(/\u0002/g, '<span class="x-v">')
        .replace(/\u0003/g, '</span>')
      out += `<span class="x-p">${esc(open)}</span><span class="x-t">${esc(name)}</span>${attrsHTML}<span class="x-p">${esc(close)}</span>`
    } else if (text) out += esc(text)
    else out += esc(m[0])
  }
  // Split into lines without breaking spans: comments are the only multi-line tokens.
  return splitLines(out)
}

function splitLines(html: string): string[] {
  const lines: string[] = []
  let open: string | null = null
  for (const raw of html.split('\n')) {
    let line: string = open ? open + raw : raw
    const opens = [...line.matchAll(/<span class="[^"]+">/g)].length
    const closes = (line.match(/<\/span>/g) ?? []).length
    if (opens > closes) {
      const last: RegExpMatchArray | undefined = [...line.matchAll(/<span class="[^"]+">/g)].pop()
      open = last ? last[0] : null
      line += '</span>'
    } else open = null
    lines.push(line)
  }
  if (lines.length && lines[lines.length - 1] === '') lines.pop()
  return lines
}
