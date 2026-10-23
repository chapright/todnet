// Reports the translation keys that nothing in the interface asks for.
// Run with: node tools/check_unused_i18n.js
const fs = require('fs');
const path = require('path');

const web = path.join(__dirname, '..', 'web');
const i18n = fs.readFileSync(path.join(web, 'js', 'i18n.js'), 'utf8');

function tableOf(name) {
  const start = i18n.indexOf(`const ${name} = {`);
  const open = i18n.indexOf('{', start);
  let depth = 0;
  let i = open;
  for (; i < i18n.length; i++) {
    if (i18n[i] === '{') depth++;
    else if (i18n[i] === '}') { depth--; if (depth === 0) break; }
  }
  const body = i18n.slice(open + 1, i);
  const keys = [];
  const re = /^\s*'([^']+)'\s*:/gm;
  let m;
  while ((m = re.exec(body)) !== null) keys.push(m[1]);
  return keys;
}

function walk(dir, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(p, out);
    else if (/\.(js|html)$/.test(entry.name)) out.push(p);
  }
  return out;
}

const sources = walk(web)
  .filter((p) => !p.endsWith(path.join('js', 'i18n.js')))
  .map((p) => fs.readFileSync(p, 'utf8'))
  .join('\n');

const escape = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

const unused = tableOf('en').filter((key) => {
  // A key counts as used when it is asked for by name. The leaf of a name
  // that is built at runtime ('state.' + state) is reported separately.
  return !new RegExp(`['"\`]${escape(key)}['"\`]`).test(sources);
});

const dynamic = unused.filter((key) => {
  const leaf = escape(key.split('.').pop());
  return new RegExp(`\\b${leaf}\\b`).test(sources);
});

const never = unused.filter((k) => !dynamic.includes(k));

console.log('--- never referenced ---');
console.log(never.join('\n'));
console.log('--- leaf only, check by hand ---');
console.log(dynamic.join('\n'));
console.log(`${unused.length} key(s) of ${tableOf('en').length} are not asked for by name`);

// A leaf that is only reached by building its name at runtime is a judgement
// call; a key nothing mentions at all is not. It is either left over, or it
// was meant to be wired and never was — both are reasons to fail a build.
if (never.length) {
  console.error(`\n${never.length} key(s) are not asked for anywhere: remove them, or wire them.`);
  process.exit(1);
}
