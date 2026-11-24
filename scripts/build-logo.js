// Builds the Viff "Halo V" logo files from the same geometry as the canvas artboard.
const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');

const OUT = process.argv[2];
const ACCENT = '#3B5BFD';
const NAV_RING = '#1F2A37'; // ring on the Cerulean navbar: slate

function band(samples) {
  const L = [], R = [];
  for (const s of samples) {
    const m = Math.hypot(s.tx, s.ty) || 1;
    const nx = -s.ty / m, ny = s.tx / m, h = s.w / 2;
    L.push([s.x + nx * h, s.y + ny * h]);
    R.push([s.x - nx * h, s.y - ny * h]);
  }
  const f = (p) => p[0].toFixed(2) + ' ' + p[1].toFixed(2);
  return 'M ' + L.map(f).join(' L ') + ' L ' + R.reverse().map(f).join(' L ') + ' Z';
}
function ring(o) {
  const th = o.tiltDeg * Math.PI / 180, c = Math.cos(th), s = Math.sin(th);
  const N = 96, out = [];
  const t0 = o.front ? -0.06 : Math.PI - 0.06, t1 = t0 + Math.PI + 0.12;
  for (let i = 0; i <= N; i++) {
    const t = t0 + (t1 - t0) * i / N;
    const lx = o.a * Math.cos(t), ly = o.b * Math.sin(t);
    const dx = -o.a * Math.sin(t), dy = o.b * Math.cos(t);
    const depth = (Math.sin(t) + 1) / 2;
    const w = (o.wMin + (o.wMax - o.wMin) * depth) * o.k + o.pad;
    out.push({ x: o.cx + lx * c - ly * s, y: o.cy + lx * s + ly * c, tx: dx * c - dy * s, ty: dx * s + dy * c, w });
  }
  return band(out);
}
const G = { cx: 32, cy: 33, a: 30.5, b: 10.5, tiltDeg: -12, wMin: 1.4, wMax: 5.4 };
// V with left-only spurs at the three terminals (arm tops and foot), so the V reads as moving right.
const SPUR = 6, SPUR_H = 5, TIP = 1.2, CQ = 0.2;
const xOuterL = (y) => 12 + 14 * (y - 12) / 44;
const xInnerR = (y) => 40 - 8 * (y - 12) / 22;
const f2 = (n) => Math.round(n * 100) / 100;
const pt = (x, y) => f2(x) + ' ' + f2(y);
const V_L = ['M', pt(12 - SPUR, 12), 'L', pt(24, 12), 'L', pt(32, 34), 'L', pt(32, 56), 'L', pt(26 - SPUR, 56), 'L', pt(26 - SPUR, 56 - TIP),
  'Q', pt(26 - SPUR + CQ * SPUR, 56 - SPUR_H), pt(xOuterL(56 - SPUR_H), 56 - SPUR_H), 'L', pt(xOuterL(12 + SPUR_H), 12 + SPUR_H),
  'Q', pt(12 - SPUR + CQ * SPUR, 12 + SPUR_H), pt(12 - SPUR, 12 + TIP), 'Z'].join(' ');
const V_R = ['M', pt(40 - SPUR, 12), 'L', pt(52, 12), 'L', pt(38, 56), 'L', pt(32, 56), 'L', pt(32, 34), 'L', pt(xInnerR(12 + SPUR_H), 12 + SPUR_H),
  'Q', pt(40 - SPUR + CQ * SPUR, 12 + SPUR_H), pt(40 - SPUR, 12 + TIP), 'Z'].join(' ');
const V_OUTLINE = ['M', pt(12 - SPUR, 12), 'L', pt(24, 12), 'L', pt(32, 34), 'L', pt(xInnerR(12 + SPUR_H), 12 + SPUR_H),
  'Q', pt(40 - SPUR + CQ * SPUR, 12 + SPUR_H), pt(40 - SPUR, 12 + TIP), 'L', pt(40 - SPUR, 12), 'L', pt(52, 12), 'L', pt(38, 56), 'L', pt(26 - SPUR, 56), 'L', pt(26 - SPUR, 56 - TIP),
  'Q', pt(26 - SPUR + CQ * SPUR, 56 - SPUR_H), pt(xOuterL(56 - SPUR_H), 56 - SPUR_H), 'L', pt(xOuterL(12 + SPUR_H), 12 + SPUR_H),
  'Q', pt(12 - SPUR + CQ * SPUR, 12 + SPUR_H), pt(12 - SPUR, 12 + TIP), 'Z'].join(' ');

// bg: color used for the separation gaps (null = transparent gaps, for use on any background)
function mark({ k = 1, bg = null, dark = false, id = 'v', accent = ACCENT }) {
  // Metallic ring options measured from slate (#1F2A37) as the darkest tone:
  //   'slate-metal'  moderate highlight, 'slate-metal-soft' subtle highlight, 'steel' the brighter gunmetal.
  const RINGS = {
    'steel': ['#1F2A37', '#6B7684', '#D3DAE3', '#4A5461', '#1F2A37'],
    'slate-metal': ['#1F2A37', '#3A4656', '#8A98AA', '#2C3744', '#1F2A37'],
    'slate-metal-soft': ['#1F2A37', '#2F3B4A', '#667383', '#283240', '#1F2A37']
  };
  const ringStops = RINGS[accent];
  const ringFill = ringStops ? `url(#${id}-ring)` : accent;
  const ringDef = ringStops
    ? `<linearGradient id="${id}-ring" x1="0" y1="0" x2="1" y2="0.25">` + ringStops.map((c, i) => `<stop offset="${[0, 0.3, 0.5, 0.7, 1][i]}" stop-color="${c}"/>`).join('') + `</linearGradient>`
    : '';
  const back = ring({ ...G, front: false, k, pad: 0 });
  const front = ring({ ...G, front: true, k, pad: 0 });
  const halo = ring({ ...G, front: true, k, pad: k > 1 ? 3 : 2.4 });
  const gl = dark
    ? `<stop offset="0" stop-color="#FFFFFF"/><stop offset="0.42" stop-color="#C3C9D3"/><stop offset="0.5" stop-color="#8D96A2"/><stop offset="0.72" stop-color="#E1E5EA"/><stop offset="1" stop-color="#7C8592"/>`
    : `<stop offset="0" stop-color="#F3F5F8"/><stop offset="0.42" stop-color="#B4BBC6"/><stop offset="0.5" stop-color="#7F8893"/><stop offset="0.72" stop-color="#D2D7DE"/><stop offset="1" stop-color="#6F7885"/>`;
  const gr = dark
    ? `<stop offset="0" stop-color="#A3ACB8"/><stop offset="0.4" stop-color="#626B77"/><stop offset="0.55" stop-color="#434A54"/><stop offset="0.8" stop-color="#8F98A4"/><stop offset="1" stop-color="#3A414A"/>`
    : `<stop offset="0" stop-color="#8E97A3"/><stop offset="0.4" stop-color="#525B66"/><stop offset="0.55" stop-color="#363D47"/><stop offset="0.8" stop-color="#7D8691"/><stop offset="1" stop-color="#2E343D"/>`;
  // With a transparent background the gaps are cut with a mask instead of painted.
  const defs = `<defs>
<linearGradient id="${id}-l" x1="0" y1="0" x2="0.35" y2="1">${gl}</linearGradient>
<linearGradient id="${id}-r" x1="0" y1="0" x2="0.35" y2="1">${gr}</linearGradient>
${ringDef}
<mask id="${id}-mv"><rect width="64" height="64" fill="#fff"/><path d="${halo}" fill="#000"/></mask>
<mask id="${id}-mb"><rect width="64" height="64" fill="#fff"/><path d="${V_OUTLINE}" fill="none" stroke="#000" stroke-width="3"/></mask>
</defs>`;
  const body = bg
    ? `<path d="${back}" fill="${ringFill}"/>
<path d="${V_OUTLINE}" fill="none" stroke="${bg}" stroke-width="3"/>
<path d="${V_L}" fill="url(#${id}-l)"/>
<path d="${V_R}" fill="url(#${id}-r)"/>
<path d="${halo}" fill="${bg}"/>
<path d="${front}" fill="${ringFill}"/>`
    : `<path d="${back}" fill="${ringFill}" mask="url(#${id}-mb)"/>
<g mask="url(#${id}-mv)">
<path d="${V_L}" fill="url(#${id}-l)"/>
<path d="${V_R}" fill="url(#${id}-r)"/>
</g>
<path d="${front}" fill="${ringFill}"/>`;
  return { defs, body };
}

function svgDoc(inner, w, h, viewBox, extra = '') {
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="${viewBox}"${extra}>\n${inner}\n</svg>\n`;
}

// 1. Mark, transparent background (site/GitHub), light & dark variants
const mL = mark({ k: 1, id: 'vl' });
const mD = mark({ k: 1, dark: true, id: 'vd' });
const markLight = svgDoc(mL.defs + '\n' + mL.body, 512, 512, '0 0 64 64');
const markDark = svgDoc(mD.defs + '\n' + mD.body, 512, 512, '0 0 64 64');
fs.writeFileSync(path.join(OUT, 'viff-mark.svg'), markLight);
fs.writeFileSync(path.join(OUT, 'viff-mark-dark.svg'), markDark);

// 2. Favicon SVG: heavier ring, transparent, adapts to dark tabs via media query on the facet gradients
const fI = mark({ k: 1.4, id: 'fi' });
const favSvg = svgDoc(fI.defs + '\n' + fI.body, 64, 64, '0 0 64 64');
fs.writeFileSync(path.join(OUT, 'favicon.svg'), favSvg);

// 3. Lockup (mark + wordmark) — the wordmark is outlined from Space Grotesk SemiBold so the SVG needs no font.
const opentype = require('opentype.js');
const font = opentype.loadSync(path.join(__dirname, 'fontdl', 'SpaceGrotesk-600.ttf'));
function wordmarkPath(x, y, size, tracking) {
  // opentype.js getPath applies kerning; letter-spacing is added per glyph via a manual advance.
  let cursor = x, d = '';
  const glyphs = font.stringToGlyphs('viff');
  glyphs.forEach((g, i) => {
    d += g.getPath(cursor, y, size).toPathData(2) + ' ';
    let adv = g.advanceWidth * size / font.unitsPerEm;
    if (i < glyphs.length - 1) adv += font.getKerningValue(g, glyphs[i + 1]) * size / font.unitsPerEm;
    cursor += adv + tracking;
  });
  return { d: d.trim(), end: cursor - tracking };
}
const WM = wordmarkPath(86, 58, 52, -1.8);
const LOCKUP_W = Math.ceil(WM.end + 8);
function lockup({ dark, nav = false, ring = null }) {
  const m = mark({ k: 1, dark: dark || nav, id: nav ? (ring ? 'ls' : 'ln') : dark ? 'ld' : 'll', accent: ring || (nav ? NAV_RING : ACCENT) });
  const ink = (dark || nav) ? '#FFFFFF' : '#111317';
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${LOCKUP_W * 4}" height="320" viewBox="0 0 ${LOCKUP_W} 80">
${m.defs}
<g transform="translate(8 8)">${m.body}</g>
<path d="${WM.d}" fill="${ink}"/>
</svg>`;
}
fs.writeFileSync(path.join(OUT, 'viff-logo.svg'), lockup({ dark: false }));
fs.writeFileSync(path.join(OUT, 'viff-logo-dark.svg'), lockup({ dark: true }));
fs.writeFileSync(path.join(OUT, 'viff-logo-navbar.svg'), lockup({ dark: false, nav: true, ring: 'slate-metal-soft' }));
fs.writeFileSync(path.join(OUT, 'viff-logo-navbar-flat.svg'), lockup({ dark: false, nav: true }));
const mS = mark({ k: 1, dark: true, id: 'vs', accent: 'slate-metal-soft' });
fs.writeFileSync(path.join(OUT, 'viff-mark-navbar.svg'), svgDoc(mS.defs + '\n' + mS.body, 512, 512, '0 0 64 64'));


// 4. PNG renders via Chromium
(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ deviceScaleFactor: 1 });

  async function shot(svg, w, h, file, bgcss = 'transparent') {
    await page.setViewportSize({ width: w, height: h });
    await page.setContent(`<!doctype html><html><head>
<link href="https://fonts.googleapis.com/css2?family=Space+Grotesk:wght@600&display=swap" rel="stylesheet">
<style>html,body{margin:0;background:${bgcss}}svg{display:block}</style></head>
<body>${svg.replace(/width="\d+" height="\d+"/, `width="${w}" height="${h}"`)}</body></html>`);
    await page.evaluate(() => document.fonts.ready);
    await page.waitForTimeout(300);
    await page.screenshot({ path: path.join(OUT, file), omitBackground: bgcss === 'transparent' });
  }

  // mark PNGs (transparent)
  for (const s of [1024, 512, 256, 180]) await shot(markLight, s, s, `viff-mark-${s}.png`);
  await shot(markDark, 512, 512, 'viff-mark-dark-512.png');
  // lockups
  // lockup PNGs: full size, plus navbar sizes (height 32 and 64 CSS px at 2x = 64 / 128 px tall)
  for (const h of [640, 128, 64]) {
    const w = Math.round(LOCKUP_W * h / 80);
    await shot(lockup({ dark: false }), w, h, h === 640 ? 'viff-logo.png' : `viff-logo-h${h}.png`);
    await shot(lockup({ dark: true }), w, h, h === 640 ? 'viff-logo-dark.png' : `viff-logo-dark-h${h}.png`);
    await shot(lockup({ dark: false, nav: true, ring: 'slate-metal-soft' }), w, h, h === 640 ? 'viff-logo-navbar.png' : `viff-logo-navbar-h${h}.png`);
  }
  // GitHub social preview 1280x640, dark
  const soc = mark({ k: 1, dark: true, id: 'sp' });
  const social = `<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="640" viewBox="0 0 1280 640">
<defs>${soc.defs.replace('<defs>', '').replace('</defs>', '')}</defs>
<rect width="1280" height="640" fill="#111317"/>
<g transform="translate(300 170) scale(4.6875)">${soc.body}</g>
<text x="640" y="400" font-family="'Space Grotesk', Arial, sans-serif" font-weight="600" font-size="200" letter-spacing="-8" fill="#F4F5F7">viff</text>
<text x="640" y="470" font-family="'JetBrains Mono', ui-monospace, monospace" font-size="28" fill="#A7ADB8">signadot.github.io/viff</text>
</svg>`;
  await page.setViewportSize({ width: 1280, height: 640 });
  await page.setContent(`<!doctype html><html><head>
<link href="https://fonts.googleapis.com/css2?family=Space+Grotesk:wght@600&family=JetBrains+Mono&display=swap" rel="stylesheet">
<style>html,body{margin:0}svg{display:block}</style></head><body>${social}</body></html>`);
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(300);
  await page.screenshot({ path: path.join(OUT, 'viff-social-1280x640.png') });

  // favicon PNGs (icon cut, transparent) and the .ico
  const icoSizes = [16, 32, 48];
  const pngs = [];
  for (const s of icoSizes) {
    const file = `favicon-${s}.png`;
    await shot(favSvg, s, s, file);
    pngs.push({ s, buf: fs.readFileSync(path.join(OUT, file)) });
  }
  // ICO container with PNG-encoded entries
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0); header.writeUInt16LE(1, 2); header.writeUInt16LE(pngs.length, 4);
  const dir = Buffer.alloc(16 * pngs.length);
  let offset = 6 + dir.length;
  pngs.forEach((p, i) => {
    const o = i * 16;
    dir.writeUInt8(p.s === 256 ? 0 : p.s, o); dir.writeUInt8(p.s === 256 ? 0 : p.s, o + 1);
    dir.writeUInt8(0, o + 2); dir.writeUInt8(0, o + 3);
    dir.writeUInt16LE(1, o + 4); dir.writeUInt16LE(32, o + 6);
    dir.writeUInt32LE(p.buf.length, o + 8); dir.writeUInt32LE(offset, o + 12);
    offset += p.buf.length;
  });
  fs.writeFileSync(path.join(OUT, 'favicon.ico'), Buffer.concat([header, dir, ...pngs.map((p) => p.buf)]));
  // apple touch icon: icon cut on a solid light tile
  const touch = mark({ k: 1.2, bg: '#F4F5F7', id: 'at' });
  await shot(svgDoc(`<rect width="64" height="64" fill="#F4F5F7"/>\n` + touch.defs + '\n' + touch.body, 180, 180, '0 0 64 64'), 180, 180, 'apple-touch-icon.png', '#F4F5F7');

  await browser.close();
  console.log(fs.readdirSync(OUT).map((f) => `${f}\t${fs.statSync(path.join(OUT, f)).size}`).join('\n'));
})();
