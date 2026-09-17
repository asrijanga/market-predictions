// A character-cell terminal drawn onto a 2D canvas. Everything the screen
// shows goes through here: the CRT shader only ever sees this canvas.
const COLS = 62;
const ROWS = 30;
const CELL_W = 26;
const CELL_H = 38;
const PAD = 30;

export class Terminal {
  constructor() {
    this.canvas = document.createElement('canvas');
    this.canvas.width = COLS * CELL_W + PAD * 2;
    this.canvas.height = ROWS * CELL_H + PAD * 2;
    this.ctx = this.canvas.getContext('2d', { alpha: false });
    this.cols = COLS;
    this.rows = ROWS;
    this.lines = [];
    this.input = '';
    this.prompt = '';
    this.cursorVisible = true;
    this.dirty = true;
    this.overlay = null; // a function that paints over the text layer
    this.clear();
  }

  clear() {
    this.lines = [];
    this.dirty = true;
  }

  // write appends lines, scrolling off the top once the screen is full.
  write(text = '') {
    for (const raw of String(text).split('\n')) {
      for (const line of wrap(raw, COLS)) this.lines.push(line);
    }
    const max = ROWS - (this.prompt ? 2 : 0);
    while (this.lines.length > max) this.lines.shift();
    this.dirty = true;
  }

  setPrompt(prompt) {
    this.prompt = prompt;
    this.dirty = true;
  }

  setInput(value) {
    this.input = value;
    this.dirty = true;
  }

  setOverlay(fn) {
    this.overlay = fn;
    this.dirty = true;
  }

  setCursor(visible) {
    if (visible !== this.cursorVisible) {
      this.cursorVisible = visible;
      this.dirty = true;
    }
  }

  render(time) {
    if (this.overlay) this.dirty = true;
    if (!this.dirty) return false;
    const { ctx, canvas } = this;

    ctx.fillStyle = '#000';
    ctx.fillRect(0, 0, canvas.width, canvas.height);

    // Faint phosphor burn of the grid, as if the same form had been shown
    // on this tube for years.
    ctx.fillStyle = 'rgba(40, 255, 130, 0.028)';
    for (let r = 0; r < ROWS; r++) ctx.fillRect(PAD, PAD + r * CELL_H + CELL_H - 3, COLS * CELL_W, 1);

    ctx.font = `700 ${CELL_H - 10}px ui-monospace, "SF Mono", Menlo, Consolas, monospace`;
    ctx.textBaseline = 'top';
    ctx.fillStyle = '#e4fff0';
    ctx.shadowColor = 'rgba(90, 255, 160, 0.7)';
    ctx.shadowBlur = 8;

    for (let r = 0; r < this.lines.length; r++) {
      const line = this.lines[r];
      if (!line) continue;
      // A leading \x01 marks a dim line, \x02 a bright one.
      let text = line, alpha = 0.92;
      if (text.startsWith('\x01')) { text = text.slice(1); alpha = 0.72; }
      else if (text.startsWith('\x02')) { text = text.slice(1); alpha = 1; }
      ctx.globalAlpha = alpha;
      ctx.fillText(text, PAD, PAD + r * CELL_H);
    }
    ctx.globalAlpha = 1;

    if (this.prompt) {
      const row = Math.min(ROWS - 1, this.lines.length);
      const y = PAD + row * CELL_H;
      const text = this.prompt + this.input;
      ctx.fillText(text, PAD, y);
      if (this.cursorVisible) {
        ctx.fillRect(PAD + text.length * CELL_W, y + 2, CELL_W - 3, CELL_H - 12);
      }
    }

    ctx.shadowBlur = 0;
    if (this.overlay) this.overlay(ctx, canvas, time);

    this.dirty = false;
    return true;
  }
}

// wrap breaks a line at the screen edge, preferring word boundaries.
function wrap(text, cols) {
  if (text.length <= cols) return [text];
  const out = [];
  let rest = text;
  while (rest.length > cols) {
    let cut = rest.lastIndexOf(' ', cols);
    if (cut <= 0) cut = cols;
    out.push(rest.slice(0, cut));
    rest = rest.slice(cut).replace(/^ /, '');
  }
  out.push(rest);
  return out;
}

export { COLS, ROWS, CELL_W, CELL_H, PAD };
