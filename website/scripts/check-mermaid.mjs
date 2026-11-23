// Parses every ```mermaid block under ../docs with mermaid's own parser (jsdom provides the DOM).
import {JSDOM} from 'jsdom';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const dom = new JSDOM('<!doctype html><html><body></body></html>');
globalThis.window = dom.window;
globalThis.document = dom.window.document;
for (const k of ['Element', 'HTMLElement', 'SVGElement', 'Node', 'DOMParser'])
  if (!(k in globalThis)) globalThis[k] = dom.window[k];

const {default: mermaid} = await import('mermaid');
mermaid.initialize({startOnLoad: false});

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../docs');
const files = [];
(function walk(d) {
  for (const e of fs.readdirSync(d, {withFileTypes: true})) {
    const p = path.join(d, e.name);
    e.isDirectory() ? walk(p) : p.endsWith('.md') && files.push(p);
  }
})(root);

let total = 0, bad = 0;
for (const f of files) {
  const re = /```mermaid\n([\s\S]*?)```/g;
  let m;
  while ((m = re.exec(fs.readFileSync(f, 'utf8')))) {
    total++;
    try {
      await mermaid.parse(m[1]);
    } catch (err) {
      bad++;
      console.error(`FAIL ${path.relative(root, f)} block ${total}: ${err.message ?? err}`);
    }
  }
}
console.log(`checked ${total} mermaid blocks, ${bad} failed`);
process.exit(bad ? 1 : 0);
