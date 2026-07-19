// Reference screen dumper: drives ghostty-vt.wasm and prints a JSON screen
// dump in the same shape the Go side emits, so the two can be diffed cell by
// cell.
//
// usage: node dump.mjs <cols> <rows> <input-file>
import fs from "node:fs";

const K = {
  POINT_ACTIVE: 0,
  CELL_CODEPOINT: 1,
  CELL_WIDE: 3,
  TERM_COLS: 1,
  TERM_ROWS: 2,
  TERM_CURSOR_X: 3,
  TERM_CURSOR_Y: 4,
  TERM_ACTIVE_SCREEN: 6,
  TERM_CURSOR_VISIBLE: 7,
  TERM_SCROLLBACK_ROWS: 15,
};

const [, , colsArg, rowsArg, file] = process.argv;
const cols = Number(colsArg), rows = Number(rowsArg);
const input = fs.readFileSync(file);

const wasmPath = new URL("./ghostty-vt.wasm", import.meta.url);
const inst = new WebAssembly.Instance(
  new WebAssembly.Module(fs.readFileSync(wasmPath)),
  { env: { log: () => {} } },
);
const e = inst.exports;
const dv = () => new DataView(e.memory.buffer);

// --- terminal ---------------------------------------------------------------
const optP = e.ghostty_wasm_alloc_u8_array(8);
dv().setUint16(optP, cols, true);
dv().setUint16(optP + 2, rows, true);
dv().setUint32(optP + 4, 0, true); // no scrollback: compare the active screen only
const outP = e.ghostty_wasm_alloc_opaque();
if (e.ghostty_terminal_new(0, outP, optP) !== 0) throw new Error("terminal_new failed");
const term = dv().getUint32(outP, true);
e.ghostty_wasm_free_u8_array(optP, 8);
e.ghostty_wasm_free_opaque(outP);

// Feed in modest chunks so a huge corpus file does not need one giant
// allocation, and so chunk boundaries exercise the parser's resumption the
// same way a PTY read would.
const CHUNK = 4096;
for (let off = 0; off < input.length; off += CHUNK) {
  const part = input.subarray(off, Math.min(off + CHUNK, input.length));
  const p = e.ghostty_wasm_alloc_u8_array(part.length);
  new Uint8Array(e.memory.buffer).set(part, p);
  e.ghostty_terminal_vt_write(term, p, part.length);
  e.ghostty_wasm_free_u8_array(p, part.length);
}

// --- read back --------------------------------------------------------------
const u32P = e.ghostty_wasm_alloc_u8_array(4);
function tget(key) {
  if (e.ghostty_terminal_get(term, key, u32P) !== 0) return null;
  return dv().getUint32(u32P, true);
}
function tgetU8(key) {
  if (e.ghostty_terminal_get(term, key, u32P) !== 0) return null;
  return dv().getUint8(u32P);
}

const pointP = e.ghostty_wasm_alloc_u8_array(24);
const refP = e.ghostty_wasm_alloc_u8_array(12);
const cellP = e.ghostty_wasm_alloc_u8_array(8);
const stP = e.ghostty_wasm_alloc_u8_array(72);

function colorSpec(base) {
  const d = dv();
  const tag = d.getUint32(base, true);
  if (tag === 1) return String(d.getUint8(base + 8)); // palette index
  if (tag === 2) {
    const hex = (n) => d.getUint8(n).toString(16).toUpperCase().padStart(2, "0");
    return "#" + hex(base + 8) + hex(base + 9) + hex(base + 10);
  }
  return "default";
}

function readRow(y) {
  const d0 = dv();
  d0.setUint32(pointP, K.POINT_ACTIVE, true);
  d0.setUint16(pointP + 8, 0, true);
  d0.setUint32(pointP + 12, y, true);
  d0.setUint32(refP, 12, true);
  if (e.ghostty_terminal_grid_ref(term, pointP, refP) !== 0) return null;
  const out = [];
  for (let x = 0; x < cols; x++) {
    dv().setUint16(refP + 8, x, true);
    if (e.ghostty_grid_ref_cell(refP, cellP) !== 0) {
      out.push({ r: " ", w: 1, a: "" });
      continue;
    }
    const cell = dv().getBigUint64(cellP, true);
    e.ghostty_cell_get(cell, K.CELL_CODEPOINT, u32P);
    const cp = dv().getUint32(u32P, true);
    e.ghostty_cell_get(cell, K.CELL_WIDE, u32P);
    const wide = dv().getUint32(u32P, true);
    const w = wide === 1 ? 2 : wide === 2 || wide === 3 ? 0 : 1;

    dv().setUint32(stP, 72, true);
    const toks = [];
    if (e.ghostty_grid_ref_style(refP, stP) === 0) {
      const d = dv();
      if (d.getUint8(stP + 56)) toks.push("b");
      if (d.getUint8(stP + 57)) toks.push("i");
      if (d.getInt32(stP + 64, true) !== 0) toks.push("u");
      if (d.getUint8(stP + 60)) toks.push("r");
      if (d.getUint8(stP + 62)) toks.push("s");
      if (d.getUint8(stP + 59)) toks.push("k");
      if (w === 2) toks.push("w");
      const fg = colorSpec(stP + 8), bg = colorSpec(stP + 24);
      if (fg !== "default") toks.push("fg:" + fg);
      if (bg !== "default") toks.push("bg:" + bg);
    } else if (w === 2) toks.push("w");

    out.push({ r: cp === 0 ? " " : String.fromCodePoint(cp), w, a: toks.join(",") });
  }
  return out;
}

const grid = [];
for (let y = 0; y < rows; y++) grid.push(readRow(y) ?? []);

process.stdout.write(JSON.stringify({
  cols, rows,
  cursor: { x: tget(K.TERM_CURSOR_X), y: tget(K.TERM_CURSOR_Y), visible: !!tgetU8(K.TERM_CURSOR_VISIBLE) },
  alt: tgetU8(K.TERM_ACTIVE_SCREEN) === 1,
  grid,
}) + "\n");
