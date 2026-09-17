// The waiting animation. The server really is simulating twenty thousand
// price paths, so the screen draws price paths: a fan of random walks
// spreading out from today, with the distribution of where they end piling
// up against the right edge as the run proceeds.
import { PAD } from './terminal.js';

const PATHS = 120;

export class PathFan {
  constructor() {
    this.stage = 'starting';
    this.fraction = 0;
    this.shown = 0;
    this.paths = [];
    this.started = performance.now();
    this.seed = 1337;
  }

  // A small deterministic generator keeps the fan stable between frames.
  rand() {
    this.seed = (this.seed * 1664525 + 1013904223) >>> 0;
    return this.seed / 4294967296;
  }

  gauss() {
    let u = 0, v = 0;
    while (u === 0) u = this.rand();
    while (v === 0) v = this.rand();
    return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v);
  }

  setStage(stage, fraction) {
    this.stage = stage;
    this.fraction = Math.max(this.fraction, fraction);
  }

  buildPath(steps) {
    const pts = [];
    let value = 0;
    let vol = 0.9;
    for (let i = 0; i < steps; i++) {
      // A touch of volatility clustering, so the fan looks like the model
      // rather than like plain Brownian motion.
      vol = 0.94 * vol + 0.06 * (0.6 + 1.4 * this.rand());
      value += this.gauss() * vol * 0.55 + 0.035;
      pts.push(value);
    }
    return pts;
  }

  draw(ctx, canvas, time) {
    const w = canvas.width, h = canvas.height;
    const left = PAD + 10, right = w - PAD - 150;
    const top = PAD + 120, bottom = h - PAD - 150;
    const midY = (top + bottom) / 2;
    const steps = 96;

    // Grow the fan toward the reported progress, and keep it moving even
    // while a slow stage runs, so the screen never looks frozen.
    const target = Math.max(0.08, this.fraction) * PATHS;
    const elapsed = (performance.now() - this.started) / 1000;
    const creep = Math.min(PATHS * 0.55, elapsed * 9);
    const want = Math.min(PATHS, Math.max(target, creep));
    while (this.paths.length < want) this.paths.push(this.buildPath(steps));

    ctx.save();
    ctx.globalCompositeOperation = 'lighter';

    // Axis.
    ctx.strokeStyle = 'rgba(70, 255, 150, 0.18)';
    ctx.lineWidth = 2;
    ctx.beginPath();
    ctx.moveTo(left, midY);
    ctx.lineTo(right, midY);
    ctx.stroke();

    const scale = (bottom - top) / 26;
    const ends = [];
    for (let p = 0; p < this.paths.length; p++) {
      const pts = this.paths[p];
      const age = Math.min(1, (this.paths.length - p) / 14);
      const reveal = Math.min(1, age * 1.4);
      ctx.beginPath();
      ctx.strokeStyle = `rgba(80, 255, 160, ${0.05 + 0.16 * (1 - age)})`;
      ctx.lineWidth = 1.4;
      for (let i = 0; i < steps * reveal; i++) {
        const x = left + (right - left) * (i / (steps - 1));
        const y = midY - pts[i] * scale;
        i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y);
      }
      ctx.stroke();
      if (reveal >= 1) ends.push(pts[steps - 1]);
    }

    // The distribution of endpoints, drawn as a histogram against the edge.
    const bins = 26;
    const counts = new Array(bins).fill(0);
    for (const e of ends) {
      const b = Math.floor(((e * scale) / (bottom - top) + 0.5) * bins);
      if (b >= 0 && b < bins) counts[b]++;
    }
    const peak = Math.max(1, ...counts);
    const binH = (bottom - top) / bins;
    for (let b = 0; b < bins; b++) {
      const len = (counts[b] / peak) * 120;
      const y = bottom - b * binH - binH;
      ctx.fillStyle = `rgba(120, 255, 180, ${0.1 + 0.5 * (counts[b] / peak)})`;
      ctx.fillRect(right + 12, y + 2, len, binH - 3);
    }

    // A sweep bar, so a long stage still reads as motion.
    const sweep = left + ((time * 0.00022) % 1) * (right - left);
    const grad = ctx.createLinearGradient(sweep - 60, 0, sweep + 60, 0);
    grad.addColorStop(0, 'rgba(120, 255, 180, 0)');
    grad.addColorStop(0.5, 'rgba(160, 255, 200, 0.16)');
    grad.addColorStop(1, 'rgba(120, 255, 180, 0)');
    ctx.fillStyle = grad;
    ctx.fillRect(sweep - 60, top, 120, bottom - top);
    ctx.restore();

    // Stage caption and a block progress bar.
    const pct = Math.round(Math.min(0.99, Math.max(this.fraction, creep / PATHS * 0.6)) * 100);
    const barCells = 34;
    const filled = Math.round((pct / 100) * barCells);
    const bar = '█'.repeat(filled) + '░'.repeat(barCells - filled);
    ctx.font = '600 22px ui-monospace, Menlo, Consolas, monospace';
    ctx.fillStyle = '#c9ffe0';
    ctx.shadowColor = 'rgba(90, 255, 160, 0.8)';
    ctx.shadowBlur = 12;
    ctx.fillText(this.stage.toUpperCase(), left, bottom + 40);
    ctx.fillText(`${bar} ${String(pct).padStart(3)}%`, left, bottom + 78);
    ctx.shadowBlur = 0;
  }
}
