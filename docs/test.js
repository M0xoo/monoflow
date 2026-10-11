/**
 * Verification test suite for @monoflow/docs
 */
const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');

console.log('Running @monoflow/docs verification suite...');

function fail(msg) {
  console.error('✗ ' + msg);
  process.exit(1);
}

// 1. app.js syntax
try {
  execSync('node -c ' + path.join(__dirname, 'app.js'), { stdio: 'pipe' });
  console.log('✓ app.js syntax valid');
} catch (err) {
  fail('app.js syntax error: ' + err.message);
}

// 2. index.html structure
const htmlPath = path.join(__dirname, 'index.html');
if (!fs.existsSync(htmlPath)) fail('index.html missing');
const html = fs.readFileSync(htmlPath, 'utf8');

if (html.includes('antigravity/monoflow')) fail('index.html contains legacy antigravity repository URL');
if (!html.includes('https://github.com/M0xoo/monoflow')) fail('index.html missing M0xoo/monoflow repository URL');
if (html.includes('get.monoflow.dev')) fail('index.html references unpublished get.monoflow.dev installer');

for (const id of ['provides', 'loop', 'install', 'commands']) {
  if (!html.includes(`id="${id}"`)) fail(`index.html missing #${id} section`);
}

const features = (html.match(/class="feature[ "]/g) || []).length;
if (features !== 6) fail(`expected 6 capability cards, found ${features}`);

// Every command family the CLI exposes should appear in the reference table.
const commands = ['init', 'clone', 'graph', 'subset', 'verify', 'checkpoint', 'cache', 'quarantine',
  'worktree', 'task', 'guard', 'review', 'commit', 'branch', 'sync', 'push', 'events', 'pr',
  'queue', 'ci check', 'ci health', 'pr status', 'outer', 'deliver', 'app test', 'version', 'scenario'];
const table = html.slice(html.indexOf('id="cmd-table"'));
for (const c of commands) {
  if (!table.includes(`<code>${c}`)) fail(`command reference missing "${c}"`);
}
console.log('✓ index.html structure, sections and command reference verified');

// 3. styles.css
const cssPath = path.join(__dirname, 'styles.css');
if (!fs.existsSync(cssPath)) fail('styles.css missing');
const css = fs.readFileSync(cssPath, 'utf8');
if (css.length < 1000) fail('styles.css is truncated');
if (!css.includes('prefers-color-scheme: dark')) fail('styles.css missing dark mode');
console.log('✓ styles.css verified (' + css.length + ' bytes)');

console.log('✓ All @monoflow/docs tests passed!');
process.exit(0);
