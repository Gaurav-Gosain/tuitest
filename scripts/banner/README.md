# banner

Generates the 1280x360 README header. This directory is the canonical copy;
`vtgl` and `webterm` carry a verbatim copy at the same path, so a change to the
house style is made here and copied out.

## Usage

```sh
node make-banner.mjs configs/tuitest.json -o ../../docs/images/banner.png
```

Flags override the config file, and a banner can be made without one:

```sh
node make-banner.mjs --name vtgl --split 2 \
  --tagline "a WebGL2 glyph-atlas terminal renderer" \
  --accent blue --motif atlas \
  --meta "instanced draws,damage-driven uploads,shelf-packed atlas" \
  -o out.png
```

`node make-banner.mjs --help` lists every flag.

## Config

| field | meaning |
| --- | --- |
| `name` | the wordmark |
| `split` | character index where the accent colour starts |
| `tagline` | one line under the wordmark; keep it under ~46 characters |
| `meta` | array of lines, each an array of short chips |
| `accent` | `teal`, `green`, `blue`, `violet`, `amber`, `rose`, or a hex colour |
| `motif` | `graph`, `cells`, `atlas`, `chrome`, `none` |
| `motifOpts` | per-motif parameters, including `seed` |
| `palette` | overrides for `bg0`, `bg1`, `fg`, `muted`, `dim` |

## House style

The name splits into a neutral head and an accented tail at a real morpheme
boundary: `tui|test`, `vt|gl`, `web|term`, `turbo|graph`. That is what keeps the
two-tone wordmark from looking arbitrary, and it is the strongest single cue
that these repositories belong together.

The motif on the right is a diagram of what the project actually does, not
decoration: a cell grid with a lit assertion region for `tuitest`, a shelf-packed
atlas page for `vtgl`, stacked window chrome for `webterm`. Adding a project
means adding a motif to `motifs.mjs` that means something.

Type is JetBrains Mono throughout, the same face the projects are set in.
Everything sits inside a 100px gutter, so nothing crops when GitHub rounds the
corners. The 16px meta line is the smallest element and renders at 8px once
GitHub halves the image, which is the floor: do not add anything smaller.

## How it renders

The banner is laid out as HTML and screenshotted with headless chromium at 2x,
then downsampled with ImageMagick. A browser is used because the wordmark has to
be JetBrains Mono and a browser is the one tool here that shapes an installed
system font correctly; the 2x supersample is what makes the small type survive
GitHub scaling the image to half width.

Chromium is asked for a window twice the banner height and the result is cropped
to a known rectangle, because headless chromium does not give the page a CSS
viewport equal to `--window-size` and the ratio moves between versions. Relying
on it would silently clip the last line of meta text.

Requirements: `chromium` (or `BANNER_CHROMIUM` pointing at a chromium or chrome
binary), ImageMagick 6 or 7, and JetBrains Mono installed. No `node_modules` and
no network access. Output is deterministic: motifs use a seeded PRNG, so
regenerating a banner from the same config reproduces it.
