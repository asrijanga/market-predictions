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

// symbolColumns lays a symbol list out in columns that fit the screen.
export function symbolColumns(symbols) {
  const names = symbols.map((s) => (typeof s === 'string' ? s : s.symbol));
  const perRow = 6;
  const out = [];
  for (let i = 0; i < names.length; i += perRow) {
    out.push('\x01 ' + names.slice(i, i + perRow).map((n) => pad(n, 8)).join(''));
  }
  return out;
}

// SYMBOLS_PER_PAGE fills the screen without scrolling the header off it.
const SYMBOLS_PER_PAGE = 96;

// sample picks n names spread across the whole set, so the examples on the
// boot screen are not all filed under A.
function sample(symbols, n) {
  if (symbols.length <= n) return symbols.map((s) => s.symbol);
  const stride = symbols.length / n;
  const out = [];
  for (let i = 0; i < n; i++) out.push(symbols[Math.floor(i * stride)].symbol);
  return out;
}

// suggest finds names the person might have meant: a prefix of what they
// typed, or a name that contains it.
function suggest(query, symbols) {
  const names = symbols.map((s) => s.symbol);
  const starts = names.filter((n) => n.startsWith(query) || query.startsWith(n));
  const contains = names.filter((n) => !starts.includes(n) && n.includes(query));
  return [...starts, ...contains].slice(0, 12);
}

// unknown is the answer when the published set has nothing for a symbol.
// The machine says so plainly rather than pretending, and points at what it
// does have.
export function unknown(symbol, catalog) {
  const out = ['', `\x02 I DON'T KNOW ${symbol}.`, ''];
  const near = suggest(symbol, catalog.symbols);
  if (near.length) {
    out.push('\x01 DID YOU MEAN:');
    out.push(...symbolColumns(near));
    out.push('');
  }
  out.push(`\x01 THIS MACHINE HOLDS ${catalog.symbols.length} SYMBOLS, COMPUTED ${catalog.asOf}.`);
  out.push('\x01 IT ANSWERS FOR THOSE AND NOTHING ELSE.');
  out.push('\x01 TYPE LIST TO PAGE THROUGH THEM.');
  return out;
}

// listPage renders one page of the catalogue.
export function listPage(catalog, page) {
  const all = catalog.symbols;
  const pages = Math.max(1, Math.ceil(all.length / SYMBOLS_PER_PAGE));
  const current = ((page % pages) + pages) % pages;
  const slice = all.slice(current * SYMBOLS_PER_PAGE, (current + 1) * SYMBOLS_PER_PAGE);
  return [
    '',
    `\x02 CATALOGUE ${current * SYMBOLS_PER_PAGE + 1}-${current * SYMBOLS_PER_PAGE + slice.length} OF ${all.length}` +
      (pages > 1 ? `   PAGE ${current + 1}/${pages}` : ''),
    '',
    ...symbolColumns(slice),
    '',
    pages > 1 ? '\x01 TYPE LIST AGAIN FOR THE NEXT PAGE.' : '',
  ];
}

// DISCLAIMER is shown on the boot screen and under every report. The
// machine is a toy, and it says so wherever it says anything else.
export const DISCLAIMER = [
  '\x02 AN EXPERIMENT, FOR FUN. NOT FINANCIAL ADVICE.',
  '\x02 DO NOT USE THIS FOR ANY FINANCIAL BENEFIT.',
];

export function lines(view, catalog) {
  const out = [];
  out.push(...DISCLAIMER);
  out.push('');
  out.push(...banner(view));
  const run = view.cached ? `${view.elapsed} (FROM CACHE)` : view.elapsed;
  out.push(`\x01 SPOT ${view.spot.toFixed(2)}   AS OF ${view.asOf}   RUN ${run}`);
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
  if (catalog) {
    out.push('');
    out.push(`\x01 PUBLISHED SET, COMPUTED ${catalog.asOf}. NOT LIVE PRICES.`);
  }
  return out;
}

function signalLine(s, sign) {
  const score = `${s.score >= 0 ? '+' : ''}${s.score.toFixed(2)}`;
  const budget = W - 10;
  let label = `${s.name}: ${s.detail}`;
  // Mark a reading that had to be cut, rather than letting it run silently
  // off the edge of the tube.
  if (label.length > budget) label = label.slice(0, budget - 2) + '\u2026';
  return ` ${sign} ` + pad(label, budget) + padL(score, 6);
}

// boot is the power-on sequence. It tells the truth about which machine
// this is: one wired to a live model, or one serving a published set.
export function boot(catalog) {
  const out = [
    ...DISCLAIMER,
    '',
    'MKTPREDICT 8000  (C) 1984 MKTPREDICT SYSTEMS',
    '64K RAM SYSTEM   ANALYTIC COPROCESSOR PRESENT',
    '',
    '\x01SELF TEST ................................ OK',
  ];
  if (catalog) {
    out.push(`\x01ARCHIVE ${pad(catalog.asOf, 16)}................ OK`);
    out.push('\x01MONTE CARLO ENGINE ....................... OK');
    out.push('');
    out.push('READY.');
    out.push('');
    out.push(`\x01${catalog.symbols.length} SYMBOLS ON FILE, COMPUTED ${catalog.asOf}.`);
    out.push('\x01ANYTHING ELSE AND THIS MACHINE WILL SAY IT DOES NOT KNOW.');
    out.push('');
    out.push('\x01TYPE ONE AND PRESS RETURN, OR LIST TO SEE THEM ALL:');
    out.push(...symbolColumns(sample(catalog.symbols, 18)));
  } else {
    out.push('\x01MARKET DATA LINK ......................... OK');
    out.push('\x01VOLATILITY UNIT .......................... OK');
    out.push('\x01MONTE CARLO ENGINE ....................... OK');
    out.push('');
    out.push('READY.');
    out.push('');
    out.push('\x01TYPE A TICKER SYMBOL AND PRESS RETURN.');
    out.push('\x01EXAMPLES: AAPL   NVDA   MSFT   WDC');
  }
  out.push('');
  return out;
}
