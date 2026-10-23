// Checks that every locale has exactly the keys of the English table.
// Run with: node tools/check_i18n.js

const fs = require('fs');
const path = require('path');

const file = path.join(__dirname, '..', 'web', 'js', 'i18n.js');
const src = fs.readFileSync(file, 'utf8');

function tableOf(name) {
  const start = src.indexOf(`const ${name} = {`);
  if (start < 0) throw new Error(`table ${name} not found`);
  const open = src.indexOf('{', start);
  let depth = 0;
  let i = open;
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') {
      depth--;
      if (depth === 0) break;
    }
  }
  const body = src.slice(open + 1, i);
  const keys = [];
  const re = /^\s*'([^']+)'\s*:/gm;
  let m;
  while ((m = re.exec(body)) !== null) keys.push(m[1]);
  return keys;
}

const en = tableOf('en');
const fa = tableOf('fa');

let bad = 0;
for (const k of en) {
  if (!fa.includes(k)) {
    console.log('missing in fa:', k);
    bad++;
  }
}
for (const k of fa) {
  if (!en.includes(k)) {
    console.log('missing in en:', k);
    bad++;
  }
}

// The placeholders have to survive the translation, or a string silently
// loses its variable.
const body = src.slice(src.indexOf('const fa = {'));
for (const k of en) {
  const placeholders = (k.match(/\{[a-zA-Z]+\}/g) || []).sort();
  if (!placeholders.length) continue;
  const re = new RegExp(`'${k.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}'\\s*:\\s*'([^']*)'`);
  const m = body.match(re);
  if (!m) continue;
  const got = (m[1].match(/\{[a-zA-Z]+\}/g) || []).sort();
  if (placeholders.join(',') !== got.join(',')) {
    console.log(`placeholder mismatch in fa for ${k}: want ${placeholders.join(',')} got ${got.join(',')}`);
    bad++;
  }
}

console.log(`en: ${en.length} keys, fa: ${fa.length} keys, ${bad} problem(s)`);
process.exitCode = bad ? 1 : 0;
