/**
 * Verification test suite for @monoflow/docs
 */
const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');

console.log('Running @monoflow/docs verification suite...');

// 1. Verify app.js syntax
try {
  execSync('node -c ' + path.join(__dirname, 'app.js'), { stdio: 'pipe' });
  console.log('✓ app.js syntax valid');
} catch (err) {
  console.error('✗ app.js syntax error:', err.message);
  process.exit(1);
}

// 2. Verify index.html existence and integrity
const htmlPath = path.join(__dirname, 'index.html');
if (!fs.existsSync(htmlPath)) {
  console.error('✗ index.html missing');
  process.exit(1);
}
const htmlContent = fs.readFileSync(htmlPath, 'utf8');

if (htmlContent.includes('antigravity/monoflow')) {
  console.error('✗ index.html contains legacy antigravity repository URL');
  process.exit(1);
}

if (!htmlContent.includes('https://github.com/M0xoo/monoflow')) {
  console.error('✗ index.html missing M0xoo/monoflow repository URL');
  process.exit(1);
}

if (!htmlContent.includes('workbench-viewport')) {
  console.error('✗ index.html missing workbench viewport');
  process.exit(1);
}
console.log('✓ index.html structure and integrity verified');

// 3. Verify styles.css
const cssPath = path.join(__dirname, 'styles.css');
if (!fs.existsSync(cssPath)) {
  console.error('✗ styles.css missing');
  process.exit(1);
}
const cssContent = fs.readFileSync(cssPath, 'utf8');
if (cssContent.length < 1000) {
  console.error('✗ styles.css is truncated');
  process.exit(1);
}
console.log('✓ styles.css verified (' + cssContent.length + ' bytes)');

console.log('✓ All @monoflow/docs tests passed!');
process.exit(0);
