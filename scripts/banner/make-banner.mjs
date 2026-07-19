#!/usr/bin/env node
// README banner generator.
//
// Emits a 1280x360 PNG in one house style so a family of repositories share a
// header. Layout, type scale and palette are derived from the turbograph banner
// this style started from; the only per-project parts are the name, the
// tagline, the meta chips, the accent colour and the motif.
//
// Rendering goes through headless chromium rather than a drawing library
// because the wordmark must be JetBrains Mono, the same face the projects
// themselves are set in, and a browser is the one tool here that shapes system
// fonts correctly. It is rendered at 2x and downsampled, which is what makes
// the type hold up when GitHub scales the image to half width.
//
// Requires: chromium (or set BANNER_CHROMIUM), ImageMagick, JetBrains Mono.
// No node_modules, no network. Output is deterministic.
//
// Usage:
//   node make-banner.mjs configs/tuitest.json -o ../../docs/images/banner.png
//   node make-banner.mjs --name vtgl --split 2 --tagline "..." --motif atlas -o out.png
//
// See README.md in this directory.

import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { MOTIFS, MOTIF_W, MOTIF_H } from './motifs.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));

// --- house style -----------------------------------------------------------
// Geometry measured off the reference banner. The 100px side gutters are the
// load-bearing number: they keep every glyph clear of the edge at any crop or
// rounded corner GitHub applies, and they set the optical margin that makes the
// composition read as deliberate rather than as text dropped on a rectangle.
const W = 1280, H = 360, GUTTER = 100;

// Sizes and offsets are matched to the reference banner by measuring ink boxes,
// not guessed. The wordmark carries real negative tracking: at 85px a monospace
// face sets far too loose for a logotype, and tightening it is what makes the
// name read as one object instead of ten letters.
const STYLE = {
  wordmark: { size: 85, weight: 700, tracking: -2.56, top: 99 },
  tagline: { size: 25.1, weight: 400, top: 195 },
  meta: { size: 16, weight: 400, top: 258, lineHeight: 28, sepPad: 13.6 },
};

const DEFAULT_PALETTE = {
  bg0: '#0b0e14',   // corner black, cool and slightly blue
  bg1: '#151b26',   // lift toward the motif side
  fg: '#e6e9ef',    // wordmark, near-white but never pure white
  muted: '#9aa3b2', // tagline
  dim: '#59606f',   // meta chips
  accent: '#5ad1c8',
};

// Per-project accents. Each is bright enough to pass on a light GitHub theme
// backdrop and desaturated enough not to vibrate against the dark field.
export const ACCENTS = {
  teal: '#5ad1c8',
  green: '#7fd88f',
  blue: '#7aa2f7',
  violet: '#b58cf6',
  amber: '#e0a86a',
  rose: '#f0808f',
};

const FONT_STACK = "'JetBrains Mono','JetBrainsMono Nerd Font','JetBrainsMonoNL Nerd Font','JetBrains Mono NL',ui-monospace,monospace";

function esc(s) {
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// --- html ------------------------------------------------------------------
function buildHtml(cfg) {
  const p = { ...DEFAULT_PALETTE, ...(cfg.palette || {}) };
  if (cfg.accent) p.accent = ACCENTS[cfg.accent] || cfg.accent;

  // The name splits into a neutral head and an accented tail. Splitting at a
  // real morpheme boundary (tui|test, vt|gl, web|term) is what stops this from
  // looking like an arbitrary two-tone effect: the colour marks a seam that is
  // already in the word.
  const name = cfg.name || '';
  const split = cfg.split ?? name.length;
  const head = esc(name.slice(0, split));
  const tail = esc(name.slice(split));

  // Chips are separated by a slash with CSS padding around it. Padding rather
  // than literal spaces because HTML collapses runs of whitespace, so a spaced
  // separator in the source would silently render as a single space.
  const meta = (cfg.meta || []).map((line) =>
    `<div class="meta-line">${line.map(esc).join('<span class="sep">/</span>')}</div>`
  ).join('');

  const motifFn = MOTIFS[cfg.motif] || MOTIFS.none;
  const motif = motifFn(p, cfg.motifOpts || {});

  // Motif sits in the right third, mirroring the left gutter so the whole
  // composition is symmetric about its own margins.
  const motifX = W - GUTTER - MOTIF_W + 20;
  const motifY = (H - MOTIF_H) / 2;

  return `<!doctype html><html><head><meta charset="utf-8"><style>
  *{margin:0;padding:0;box-sizing:border-box}
  html,body{width:${W}px;height:${H}px;background:${p.bg0}}
  .banner{position:relative;width:${W}px;height:${H}px;overflow:hidden;
    font-family:${FONT_STACK};
    font-feature-settings:"liga" 0,"calt" 0;
    -webkit-font-smoothing:antialiased;
    background:${p.bg0};}
  /* Three stacked fields, none of them a literal gradient sweep: a cool base,
     a soft accent glow behind the motif, and a vignette that pulls the corners
     down so the image has an edge without needing a border. */
  .bg{position:absolute;inset:0;
    background:
      radial-gradient(680px 420px at 78% 48%, ${p.accent}1f, transparent 68%),
      radial-gradient(900px 600px at 12% 8%, ${p.bg1}, transparent 70%),
      linear-gradient(120deg, ${p.bg0} 0%, ${p.bg1} 62%, ${p.bg0} 100%);}
  .vignette{position:absolute;inset:0;
    background:radial-gradient(120% 130% at 50% 50%, transparent 42%, #00000066 100%);}
  /* Hairline top and bottom rules. They give the banner a defined edge on a
     light GitHub theme, where a dark image otherwise floats. */
  .rule{position:absolute;left:0;right:0;height:1px;background:linear-gradient(90deg,transparent,${p.accent}38 22%,${p.accent}38 78%,transparent)}
  .rule.t{top:0} .rule.b{bottom:0}

  .text{position:absolute;left:${GUTTER}px;top:0;width:${W - GUTTER * 2 - MOTIF_W + 60}px}
  .wordmark{position:absolute;top:${STYLE.wordmark.top}px;
    font-size:${STYLE.wordmark.size}px;font-weight:${STYLE.wordmark.weight};
    letter-spacing:${STYLE.wordmark.tracking}px;line-height:1;white-space:nowrap;color:${p.fg}}
  /* The accented tail carries a soft bloom the neutral head does not. It reads
     as the name being lit from the motif side rather than as a glow effect, and
     it survives the downscale as a faint halo that keeps the accent from going
     flat at half size. */
  .wordmark .tail{color:${p.accent};
    text-shadow:0 0 30px ${p.accent}40, 0 0 68px ${p.accent}1f}
  .tagline{position:absolute;top:${STYLE.tagline.top}px;
    font-size:${STYLE.tagline.size}px;font-weight:${STYLE.tagline.weight};
    line-height:1.2;white-space:nowrap;color:${p.muted}}
  .meta{position:absolute;top:${STYLE.meta.top}px;
    font-size:${STYLE.meta.size}px;font-weight:${STYLE.meta.weight};color:${p.dim}}
  .meta-line{line-height:${STYLE.meta.lineHeight}px;white-space:nowrap}
  .meta .sep{color:${p.accent};opacity:.45;padding:0 ${STYLE.meta.sepPad}px}

  .motif{position:absolute;left:${motifX}px;top:${motifY}px;
    width:${MOTIF_W}px;height:${MOTIF_H}px;opacity:.95}
  </style></head><body>
  <div class="banner">
    <div class="bg"></div>
    <div class="vignette"></div>
    <div class="rule t"></div><div class="rule b"></div>
    <svg class="motif" viewBox="0 0 ${MOTIF_W} ${MOTIF_H}" xmlns="http://www.w3.org/2000/svg">
      <defs><filter id="soft" x="-60%" y="-60%" width="220%" height="220%">
        <feGaussianBlur stdDeviation="2.2" result="b"/>
        <feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge>
      </filter></defs>
      ${motif}
    </svg>
    <div class="text">
      <div class="wordmark">${head}<span class="tail">${tail}</span></div>
      <div class="tagline">${esc(cfg.tagline || '')}</div>
      <div class="meta">${meta}</div>
    </div>
  </div></body></html>`;
}

// --- render ----------------------------------------------------------------
function findChromium() {
  const candidates = [process.env.BANNER_CHROMIUM, 'chromium', 'chromium-browser',
    'google-chrome-stable', 'google-chrome', '/usr/bin/chromium'].filter(Boolean);
  for (const c of candidates) {
    try {
      execFileSync(c, ['--version'], { stdio: 'ignore', timeout: 15000 });
      return c;
    } catch { /* try the next one */ }
  }
  throw new Error('no chromium found; set BANNER_CHROMIUM to a chromium or chrome binary');
}

function magick(args) {
  // ImageMagick 7 ships `magick`; 6 ships `convert`. Accept either.
  for (const bin of ['magick', 'convert']) {
    try {
      execFileSync(bin, args, { stdio: ['ignore', 'ignore', 'pipe'], timeout: 60000 });
      return;
    } catch (e) {
      if (e.code === 'ENOENT') continue;
      throw new Error(`${bin} failed: ${e.stderr?.toString().trim() || e.message}`);
    }
  }
  throw new Error('ImageMagick not found (need `magick` or `convert`)');
}

export function renderBanner(cfg, outPath) {
  const chromium = findChromium();
  // ImageMagick will not create the output directory, and the error it raises
  // names the file rather than the missing parent, which reads as a permissions
  // problem. Create the parent first so a fresh clone can write straight to
  // docs/images/ before that directory exists.
  mkdirSync(dirname(resolve(outPath)), { recursive: true });
  // Scratch dir under the OS temp root, removed in the finally block whatever
  // happens. Chromium is given its own profile inside it so a run never
  // touches, or is blocked by, the user's real browser profile.
  const dir = mkdtempSync(join(tmpdir(), 'banner-'));
  try {
    const html = join(dir, 'banner.html');
    const shot = join(dir, 'shot.png');
    writeFileSync(html, buildHtml(cfg));
    execFileSync(chromium, [
      '--headless',
      '--disable-gpu',
      '--no-sandbox',
      '--hide-scrollbars',
      '--no-first-run',
      '--disable-extensions',
      '--disable-dev-shm-usage',
      `--user-data-dir=${join(dir, 'profile')}`,
      '--force-device-scale-factor=2',
      // The window is asked for deliberately taller than the banner. Headless
      // chromium does not hand the page a CSS viewport equal to --window-size
      // (the ratio varies with headless-shell version), so relying on it to be
      // exactly H would silently clip the last line of meta text. Rendering
      // into a roomy viewport and cropping a known rectangle is independent of
      // that quirk.
      `--window-size=${W},${H * 2}`,
      '--virtual-time-budget=3000',
      `--screenshot=${shot}`,
      `file://${html}`,
    ], { stdio: ['ignore', 'ignore', 'pipe'], timeout: 90000 });

    // Crop the banner rectangle at 2x, then downsample to 1x. The supersample
    // is the whole reason the small type survives GitHub rendering the banner
    // at half its pixel width.
    magick([shot, '-crop', `${W * 2}x${H * 2}+0+0`, '+repage',
      '-filter', 'Lanczos', '-resize', `${W}x${H}`,
      '-strip', '-define', 'png:color-type=6', '-quality', '95', outPath]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  return outPath;
}

// --- cli -------------------------------------------------------------------
function parseArgs(argv) {
  const cfg = {};
  let out = null, config = null;
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = () => argv[++i];
    switch (a) {
      case '-o': case '--out': out = next(); break;
      case '--name': cfg.name = next(); break;
      case '--split': cfg.split = Number(next()); break;
      case '--tagline': cfg.tagline = next(); break;
      case '--accent': cfg.accent = next(); break;
      case '--motif': cfg.motif = next(); break;
      // --meta may be repeated; each occurrence is one line of slash-separated
      // chips, given comma-separated.
      case '--meta': (cfg.meta ||= []).push(next().split(',').map((s) => s.trim())); break;
      case '-h': case '--help': out = '--help'; break;
      default:
        if (!a.startsWith('-')) config = a;
        else throw new Error(`unknown flag: ${a}`);
    }
  }
  return { cfg, out, config };
}

const USAGE = `make-banner.mjs [config.json] [flags] -o out.png

  --name <s>       project name, set in the wordmark
  --split <n>      character index where the accent colour starts
  --tagline <s>    one line under the wordmark
  --meta <a,b,c>   one line of meta chips; repeat for a second line
  --accent <name>  ${Object.keys(ACCENTS).join(' | ')} or a hex colour
  --motif <name>   ${Object.keys(MOTIFS).join(' | ')}
  -o <path>        output PNG (1280x360)

A config file supplies the same fields as JSON; flags override it.`;

async function main() {
  const { cfg, out, config } = parseArgs(process.argv.slice(2));
  if (out === '--help' || (!out && !config)) {
    process.stdout.write(USAGE + '\n');
    process.exit(out === '--help' ? 0 : 1);
  }
  let merged = cfg;
  if (config) {
    const loaded = JSON.parse(readFileSync(resolve(config), 'utf8'));
    merged = { ...loaded, ...cfg };
  }
  const dest = resolve(out || join(HERE, `${merged.name}-banner.png`));
  renderBanner(merged, dest);
  process.stdout.write(`${dest}\n`);
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  main().catch((e) => { process.stderr.write(`banner: ${e.message}\n`); process.exit(1); });
}
