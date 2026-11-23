// Replays a recorded terminal session in xterm.js and captures frames.
const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const [, , input, outDir, title, scaleArg] = process.argv;
const FPS = 12;
const scale = Number(scaleArg || 1);
const rec = JSON.parse(fs.readFileSync(input, 'utf8'));
const xtermJs = fs.readFileSync(path.join(__dirname, 'node_modules/@xterm/xterm/lib/xterm.js'), 'utf8');
const xtermCss = fs.readFileSync(path.join(__dirname, 'node_modules/@xterm/xterm/css/xterm.css'), 'utf8');

const html = `<!doctype html><html><head><meta charset="utf-8"><style>
${xtermCss}
html,body{margin:0;padding:0;background:#1e1e2e}
#win{display:inline-block;background:#1e1e2e}
#bar{height:34px;background:#181825;display:flex;align-items:center;padding:0 14px;gap:8px;position:relative}
.dot{width:12px;height:12px;border-radius:50%}
#title{position:absolute;left:0;right:0;text-align:center;color:#a6adc8;font:13px "Inter",sans-serif;pointer-events:none}
#t{padding:14px 16px 16px 16px}
.xterm-viewport{overflow:hidden!important}
.xterm, .xterm *{font-variant-ligatures:none!important;font-feature-settings:"calt" 0,"liga" 0!important}
</style></head><body>
<div id="win"><div id="bar"><span class="dot" style="background:#ff5f57"></span><span class="dot" style="background:#febc2e"></span><span class="dot" style="background:#28c840"></span><div id="title">${title}</div></div><div id="t"></div></div>
<script>${xtermJs}</script>
<script>
window.ready = document.fonts.load('15px "JetBrains Mono"').then(() => {
  window.term = new Terminal({
    cols: ${rec.cols}, rows: ${rec.rows}, fontFamily: '"JetBrains Mono", "DejaVu Sans Mono", monospace',
    fontSize: 15, lineHeight: 1.15, cursorBlink: false, cursorStyle: 'block', scrollback: 0,
    theme: { background: '#1e1e2e', foreground: '#cdd6f4', cursor: '#f5e0dc', cursorAccent: '#1e1e2e',
      black: '#45475a', red: '#f38ba8', green: '#a6e3a1', yellow: '#f9e2af', blue: '#89b4fa',
      magenta: '#f5c2e7', cyan: '#94e2d5', white: '#bac2de', brightBlack: '#585b70', brightRed: '#f38ba8',
      brightGreen: '#a6e3a1', brightYellow: '#f9e2af', brightBlue: '#89b4fa', brightMagenta: '#f5c2e7',
      brightCyan: '#94e2d5', brightWhite: '#a6adc8' },
  });
  term.open(document.getElementById('t'));
  window.writeAsync = (s) => new Promise((r) => term.write(s, r));
});
</script></body></html>`;

(async () => {
  fs.mkdirSync(outDir, { recursive: true });
  const browser = await chromium.launch();
  const page = await browser.newPage({ deviceScaleFactor: scale, viewport: { width: 1200, height: 800 } });
  await page.setContent(html);
  await page.evaluate(() => window.ready);
  await page.waitForTimeout(300);
  const win = await page.$('#win');

  const events = rec.events;
  const end = events[events.length - 1][0];
  let idx = 0, frameNo = 0, lastHash = null;
  const frames = []; // {file, duration}
  for (let t = 0; t <= end + 1e-9; t += 1 / FPS) {
    let chunk = '';
    while (idx < events.length && events[idx][0] <= t) chunk += events[idx++][1];
    if (chunk) await page.evaluate((c) => window.writeAsync(c), chunk);
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => r())));
    const buf = await win.screenshot();
    const hash = crypto.createHash('sha1').update(buf).digest('hex');
    if (hash === lastHash) { frames[frames.length - 1].duration += 1 / FPS; continue; }
    const file = path.join(outDir, `f${String(frameNo++).padStart(4, '0')}.png`);
    fs.writeFileSync(file, buf);
    frames.push({ file, duration: 1 / FPS });
    lastHash = hash;
  }
  frames[frames.length - 1].duration += 1.5; // hold the last frame a bit longer
  const list = frames.map((f) => `file '${path.resolve(f.file)}'\nduration ${f.duration.toFixed(3)}`).join('\n')
    + `\nfile '${path.resolve(frames[frames.length - 1].file)}'\n`;
  fs.writeFileSync(path.join(outDir, 'list.txt'), list);
  console.log(`${frames.length} unique frames, ${end.toFixed(1)}s`);
  await browser.close();
})();
