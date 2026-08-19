// nodes.mjs — print the nodes mermaid actually BUILDS from each diagram, and warn about the ones
// nobody wrote.
//
// It exists because check-mermaid cannot see this class at all. A `;` terminates a statement, so
//
//     undecided --> stale: proposal-outrun — base ≠ tip;<br/>the branch is deleted
//
// is a VALID diagram: a transition, and then four statements each declaring a state called
// "the", "branch", "is", "deleted". It parses, it renders, and it renders four loose boxes.
// The parser has nothing to complain about, so the only reader that can catch it is one that
// looks at the state table rather than at the syntax.
//
// The heuristic is deliberately narrow: a state that no transition mentions, and that holds no
// children of its own, is either a deliberate island — rare — or the wreckage of a terminated
// label. Composites are excluded because a container needs no arrow to be legitimate, which the
// first run of this reader learnt by flagging both of them on the page it was written for.
//
//     node scripts/mermaid/nodes.mjs <file.md>...
import fs from 'fs';
import { JSDOM } from './node_modules/jsdom/lib/api.js';

const dom = new JSDOM('<!doctype html><body></body>', { pretendToBeVisual: true });
global.window = dom.window;
global.document = dom.window.document;
global.navigator = dom.window.navigator;
global.HTMLElement = dom.window.HTMLElement;

const mermaid = (await import('./node_modules/mermaid/dist/mermaid.esm.mjs')).default;
mermaid.initialize({ startOnLoad: false });

let bad = 0;
for (const file of process.argv.slice(2)) {
  const src = fs.readFileSync(file, 'utf8');
  const blocks = [...src.matchAll(/```mermaid\n([\s\S]*?)```/g)].map((m) => m[1]);
  for (const [i, body] of blocks.entries()) {
    const kind = body.trim().split('\n')[0].trim();
    if (!kind.startsWith('stateDiagram')) continue; // the state table is this reader's whole subject
    let db;
    try {
      db = (await mermaid.mermaidAPI.getDiagramFromText(body)).db;
      // The state table is built during EXTRACT, which rendering does — parsing alone leaves it
      // empty, which reads as "no states" rather than as "not looked yet".
      if (db.getRootDocV2 && db.extract) db.extract(db.getRootDocV2());
    } catch {
      continue; // check-mermaid.sh owns the parse; this reader owns what parsing produced
    }
    const states = db.getStates ? db.getStates() : new Map();
    const named = new Set();
    const walk = (doc) => {
      for (const st of doc || []) {
        if (st.stmt === 'relation') {
          for (const side of [st.state1, st.state2]) if (side?.id) named.add(side.id);
        }
        if (st.doc) walk(st.doc);
      }
    };
    walk(db.getRootDocV2 ? db.getRootDocV2().doc : []);
    // A COMPOSITE is a container rather than wreckage: it holds a doc of its own and no
    // transition need touch it, which is how `state "proposal" as P { … }` is written.
    const composite = (id) => {
      const v = states.get ? states.get(id) : states[id];
      return !!(v && v.doc && v.doc.length);
    };
    const loose = [...(states.keys ? states.keys() : Object.keys(states))]
      .filter((id) => !named.has(id) && !composite(id) &&
        !id.startsWith('root_') && !id.endsWith('_start') && !id.endsWith('_end'));
    if (loose.length) {
      bad++;
      console.error(`${file}: block ${i + 1} (${kind}) builds ${loose.length} state(s) no transition mentions:`);
      console.error(`  ${loose.join(', ')}`);
      console.error(`  A ';' in a label ends the statement, and each word after it becomes a state.`);
    }
  }
}
process.exit(bad ? 1 : 0);
