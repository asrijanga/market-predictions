// Turning a result into screen text. The terminal is 62 columns wide, so
// everything here is laid out to that width in the style of a machine that
// only ever had 62 columns.
const W = 62;

const pad = (s, n) => String(s).padEnd(n).slice(0, n);
const padL = (s, n) => String(s).padStart(n).slice(-n);
const pct = (x, digits = 1) => `${x >= 0 ? '+' : ''}${(100 * x).toFixed(digits)}%`;

export function rule(char = '─') {
  return char.repeat(W);
}

export function banner(view) {
  const stance = view.stance.toUpperCase();
  const score = `${view.score >= 0 ? '+' : ''}${view.score.toFixed(2)}`;
  const out = [];
  out.push('┌' + '─'.repeat(W - 2) + '┐');
  out.push('│' + pad(` ${view.symbol}  ${stance}`, W - 2 - score.length - 1) + score + ' │');
  out.push('└' + '─'.repeat(W - 2) + '┘');
  return out;
}

// arrow is the direction glyph a terminal of this era would have had.
function arrow(direction) {
  return direction === 'up' ? '▲ UP  ' : '▼ DOWN';
}

export function lines(view) {
  const out = [];
  out.push(...banner(view));
  out.push(`\x01 SPOT ${view.spot.toFixed(2)}   AS OF ${view.asOf}   RUN ${view.elapsed}`);
  out.push('');

  out.push('\x02 FORECAST');
  out.push('\x01 HORIZON        DIR     P(UP)   TYPICAL   80% RANGE');
  for (const o of view.outlooks) {
    const name = o.name.replace('Next ', '').toUpperCase();
    out.push(
      ' ' + pad(name, 14) + pad(arrow(o.direction), 8) +
      padL(`${Math.round(100 * o.probUp)}%`, 5) + '  ' +
      padL(pct(o.median), 8) + '  ' +
      padL(`${Math.round(o.low)}-${Math.round(o.high)}`, 11)
    );
  }
  out.push('');

  if (view.for?.length) {
    out.push('\x02 SUPPORTING');
    for (const s of view.for.slice(0, 4)) out.push(signalLine(s, '+'));
  }
  if (view.against?.length) {
    out.push('\x02 AGAINST');
    for (const s of view.against.slice(0, 3)) out.push(signalLine(s, '-'));
  }
  out.push('');

  if (view.plan) {
    const p = view.plan;
    out.push('\x02 CALL PLAN');
    out.push(` ${p.trigger}  BETWEEN ${p.window.toUpperCase()}`);
    out.push(` BUY ${p.expiry} ${p.strike} CALL   COST ~${p.cost.toFixed(2)}`);
    out.push(`\x01 FILLS ${Math.round(100 * p.fillProb)}%  RETURN ${pct(p.meanReturn)}  ` +
             `WIN ${Math.round(100 * p.probProfit)}%  BREAKEVEN ${p.breakEven}`);
  } else {
    out.push('\x02 CALL PLAN');
    out.push('\x01 NO ENTRY PLAN CLEARS THE FILTERS');
  }

  if (view.earnings) {
    out.push('');
    out.push(`\x01 EARNINGS ${view.earnings}  IMPLIED MOVE ${pct(view.impliedMove)}  ` +
             `IV CRUSH ${Math.round(100 * view.crush)}%`);
  }
  for (const warning of view.warnings ?? []) out.push(`\x01 NOTE: ${warning.toUpperCase()}`);
  return out;
}

function signalLine(s, sign) {
  const score = `${s.score >= 0 ? '+' : ''}${s.score.toFixed(2)}`;
  const label = `${s.name}: ${s.detail}`;
  return ` ${sign} ` + pad(label, W - 10) + padL(score, 6);
}

export const BOOT = [
  'MKTPREDICT 8000  (C) 1984 ASRIJANGA SYSTEMS',
  '64K RAM SYSTEM   ANALYTIC COPROCESSOR PRESENT',
  '',
  '\x01SELF TEST ................................ OK',
  '\x01MARKET DATA LINK ......................... OK',
  '\x01VOLATILITY UNIT .......................... OK',
  '\x01MONTE CARLO ENGINE ....................... OK',
  '',
  'READY.',
  '',
  '\x01TYPE A TICKER SYMBOL AND PRESS RETURN.',
  '\x01EXAMPLES: AAPL   NVDA   MSFT   WDC',
  '',
];
