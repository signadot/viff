// check.mjs — parse every ```mermaid block in the tree with mermaid itself.
//
// Driven by scripts/check-mermaid.sh, which is what resolves the version. Run it, not this.
//
// It PARSES rather than renders. A render needs a browser and would catch nothing more: every
// failure seen so far has been a grammar error that mermaid.parse() rejects, and the symptom in
// the browser is the parser's own message printed where the diagram should be. Parsing is the
// same verdict without Chromium.
//
// jsdom is not optional even so. mermaid pulls in DOMPurify at import time, which binds to a
// window, so a hand-rolled stub gets you `DOMPurify.sanitize is not a function` — an error that
// looks like a broken diagram and is not one. That false negative is the reason this file loads
// a real DOM before it loads mermaid.

import { JSDOM } from 'jsdom'
import fs from 'fs'

const dom = new JSDOM('<!doctype html><html><body></body></html>')
for (const k of ['document', 'navigator', 'HTMLElement', 'SVGElement', 'Element', 'Node', 'DOMParser']) {
  global[k] = dom.window[k]
}
global.window = dom.window

// After the globals, never before: mermaid reads them as it loads.
const mermaid = (await import('mermaid')).default
mermaid.initialize({ startOnLoad: false, securityLevel: 'loose' })

const files = process.argv.slice(2)
let blocks = 0
let failed = 0
let withDiagrams = 0

for (const file of files) {
  const found = [...fs.readFileSync(file, 'utf8').matchAll(/```mermaid\n([\s\S]*?)```/g)]
  if (found.length === 0) continue
  withDiagrams++
  for (const [i, m] of found.entries()) {
    blocks++
    const src = m[1]
    // The first line is the diagram type, which is what identifies a block to a person
    // scanning output — a line number would be the offset inside the fence, not the file.
    const kind = src.trim().split('\n')[0]
    try {
      await mermaid.parse(src)
    } catch (e) {
      failed++
      console.error(`${file}: block ${i + 1} (${kind}) does not parse`)
      // Mermaid's message carries the offending text and a caret. Keep the first lines: the
      // rest is a token list that is longer than it is useful.
      for (const line of String(e.message).split('\n').slice(0, 4)) {
        console.error(`  ${line}`)
      }
    }
  }
}

if (failed > 0) {
  console.error('')
  console.error(`${failed} of ${blocks} mermaid diagram(s) do not parse.`)
  console.error('A diagram that does not parse renders as mermaid\'s error text on the page,')
  console.error('so this is a broken page rather than an ugly one. Two that have bitten:')
  console.error('  - a `;` in message or note text terminates the statement;')
  console.error('  - `&lt;`/`&gt;` work in node labels but not in a participant alias — use {braces}.')
  process.exit(1)
}
console.log(`${blocks} mermaid diagram(s) in ${withDiagrams} file(s) parse`)
