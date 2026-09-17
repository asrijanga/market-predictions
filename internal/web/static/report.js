// report.js builds the DOM for one analysis.
//
// The published payload is shaped for a 62-column screen: signal names are
// abbreviated and readings are compressed. A page has room to be plain, so
// the labels are expanded back out here rather than shipping two copies of
// every string from the server.

const el = (tag, props = {}, ...kids) => {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v === null || v === undefined) continue;
    if (k === 'class') node.className = v;
    else if (k === 'text') node.textContent = v;
    else node.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    node.append(kid);
  }
  return node;
};

// The model writes its own labels in plain English; the page only has to
// start them with a capital.
const sentence = (t = '') => t.charAt(0).toUpperCase() + t.slice(1);

// ------------------------------------------------------------- formatting

const money = (n) => n.toLocaleString(undefined, {
  minimumFractionDigits: 2, maximumFractionDigits: 2,
});
const signed = (n) => `${n >= 0 ? '+' : '−'}${Math.abs(n).toFixed(2)}`;
const percent = (fraction, digits = 1) =>
  `${fraction >= 0 ? '+' : '−'}${Math.abs(fraction * 100).toFixed(digits)}%`;
const chance = (p) => `${Math.round(p * 100)}%`;

// stanceTone collapses the five stances onto the three things the page
// paints differently.
function stanceTone(stance = '') {
  const s = stance.toLowerCase();
  if (s.includes('bull')) return 'bull';
  if (s.includes('bear')) return 'bear';
  return 'flat';
}

function section(title, ...kids) {
  return el('section', { class: 'section' },
    el('h2', { class: 'section__title', text: title }),
    ...kids);
}

// --------------------------------------------------------------- verdict

function verdictCard(view) {
  const tone = stanceTone(view.stance);
  const pctOfHalf = Math.min(1, Math.abs(view.score)) * 50;

  const meter = el('div', { class: 'meter' },
    el('div', { class: 'meter__track' },
      el('span', { class: 'meter__zero' }),
      el('span', {
        class: 'meter__fill',
        'data-tone': tone,
        style: view.score >= 0
          ? `left:50%;width:${pctOfHalf}%`
          : `right:50%;width:${pctOfHalf}%`,
      })),
    el('div', { class: 'meter__scale' },
      el('span', { text: 'bearish' }),
      el('span', { text: 'neutral' }),
      el('span', { text: 'bullish' })));

  return el('div', { class: 'verdict reveal', 'data-tone': tone },
    el('div', { class: 'verdict__head' },
      el('h1', { class: 'verdict__symbol', text: view.symbol }),
      el('span', { class: 'verdict__spot', text: `$${money(view.spot)}` })),
    el('p', { class: `verdict__stance tone-${tone}`, text: view.stance }),
    meter,
    el('p', {
      class: 'verdict__meta',
      text: `score ${signed(view.score)} · as of ${view.asOf}`,
    }));
}

// --------------------------------------------------------------- outlook

function outlookCard(o, spot) {
  const up = o.direction === 'up';
  const medianPrice = spot * (1 + o.median);
  const span = Math.max(1e-9, o.high - o.low);
  const at = (price) =>
    `${Math.min(100, Math.max(0, ((price - o.low) / span) * 100))}%`;

  return el('div', { class: 'card' },
    el('p', { class: 'outlook__name', text: o.name }),
    el('p', { class: 'outlook__dir', 'data-tone': up ? 'bull' : 'bear' },
      el('span', { text: up ? '▲ Up' : '▼ Down' }),
      el('span', { class: 'outlook__prob', text: `${chance(o.probUp)} chance of a rise` })),
    el('p', { class: 'stat' },
      el('span', { class: 'stat__label', text: 'Typical move' }),
      el('span', { class: 'stat__value', text: percent(o.median) })),
    el('p', { class: 'stat' },
      el('span', { class: 'stat__label', text: 'Target date' }),
      el('span', { class: 'stat__value', text: o.date })),
    el('div', { class: 'range' },
      el('p', { class: 'range__legend' },
        el('span', { class: 'range__key', 'data-kind': 'spot' }, ` now $${money(spot)}`),
        el('span', { class: 'range__key', 'data-kind': 'median' }, ` median $${money(medianPrice)}`)),
      el('div', { class: 'range__track' },
        el('span', { class: 'range__band' }),
        el('span', { class: 'range__mark', 'data-kind': 'spot', style: `left:${at(spot)}` }),
        el('span', { class: 'range__mark', 'data-kind': 'median', style: `left:${at(medianPrice)}` })),
      el('div', { class: 'range__ends' },
        el('span', { text: `$${money(o.low)}` }),
        el('span', { text: '80% of outcomes' }),
        el('span', { text: `$${money(o.high)}` }))));
}

// --------------------------------------------------------------- signals

function signalRow(s) {
  const tone = s.score >= 0 ? 'bull' : 'bear';
  return el('div', { class: 'signal' },
    el('span', { class: 'signal__name', text: sentence(s.name) }),
    el('span', { class: 'signal__score', 'data-tone': tone, text: signed(s.score) }),
    el('span', { class: 'signal__detail', text: sentence(s.detail) }),
    el('span', { class: 'signal__weight', text: `weight ${Math.round(s.weight * 100)}%` }));
}

function signalGroup(title, rows) {
  if (!rows?.length) return null;
  return el('div', { class: 'card' },
    el('p', { class: 'outlook__name', text: title }),
    el('div', { class: 'signals' }, rows.map(signalRow)));
}

// ------------------------------------------------------------------ plan

function planCard(plan) {
  if (!plan) {
    return el('div', { class: 'card plan--empty' },
      'No call option on this stock clears the liquidity filters, so there is ' +
      'no entry plan worth naming. The reading above stands on its own.');
  }
  return el('div', { class: 'card' },
    el('p', { class: 'plan__line' },
      'Wait for ',
      el('span', { class: 'plan__key', text: plan.trigger.toLowerCase() }),
      ' between ', el('span', { class: 'plan__key', text: plan.window }),
      ', then buy the ',
      el('span', { class: 'plan__key', text: `${plan.expiry} $${plan.strike}` }),
      ' call.'),
    el('p', { class: 'stat' },
      el('span', { class: 'stat__label', text: 'Chance the trigger fires' }),
      el('span', { class: 'stat__value', text: chance(plan.fillProb) })),
    el('p', { class: 'stat' },
      el('span', { class: 'stat__label', text: 'Return if it does' }),
      el('span', { class: 'stat__value', text: percent(plan.meanReturn) })),
    el('p', { class: 'stat' },
      el('span', { class: 'stat__label', text: 'Chance of a profit' }),
      el('span', { class: 'stat__value', text: chance(plan.probProfit) })),
    el('p', { class: 'stat' },
      el('span', { class: 'stat__label', text: 'Cost per contract' }),
      el('span', { class: 'stat__value', text: `$${money(plan.cost)}` })),
    el('p', { class: 'stat' },
      el('span', { class: 'stat__label', text: 'Break-even drift' }),
      el('span', { class: 'stat__value', text: plan.breakEven.toLowerCase() })));
}

// ----------------------------------------------------------------- notes

function notes(view, catalog) {
  const items = [];
  if (view.earnings) {
    items.push(`Earnings on ${view.earnings}. The options market is pricing a ` +
      `${percent(view.impliedMove)} move around it, and implied volatility ` +
      `falls about ${Math.round(view.crush * 100)}% once it passes.`);
  }
  for (const w of view.warnings ?? []) items.push(sentence(w));
  if (catalog) {
    items.push(`Computed ${catalog.asOf} from ${(catalog.paths ?? 0).toLocaleString()} ` +
      `simulated paths. These are not live prices.`);
  } else if (view.elapsed) {
    items.push(`Computed just now, in ${view.elapsed}.`);
  }
  if (!items.length) return null;
  return el('ul', { class: 'notes' }, items.map((t) => el('li', { text: t })));
}

// ---------------------------------------------------------------- public

export function analysis(view, catalog) {
  const frag = document.createDocumentFragment();
  frag.append(verdictCard(view));

  if (view.outlooks?.length) {
    frag.append(section('Outlook',
      el('div', { class: 'grid grid--two' },
        view.outlooks.map((o) => outlookCard(o, view.spot)))));
  }

  const groups = [
    signalGroup('Supporting', view.for),
    signalGroup('Against', view.against),
  ].filter(Boolean);
  if (groups.length) {
    frag.append(section('Why', el('div', { class: 'grid grid--two' }, groups)));
  }

  frag.append(section('If you are buying calls', planCard(view.plan)));

  const footnotes = notes(view, catalog);
  if (footnotes) frag.append(footnotes);
  return frag;
}

export function message(title, body) {
  return el('div', { class: 'card message reveal' },
    el('p', { class: 'message__title', text: title }),
    el('p', { class: 'message__body', text: body }));
}

// unknown explains the boundary of the published set rather than failing
// silently, and offers the nearest things it does hold.
export function unknown(symbol, catalog, onPick) {
  // A typo usually shares a prefix; a symbol from another exchange shares
  // nothing, and then a handful of real examples is more use than silence.
  const near = suggestions(symbol, catalog, 8);
  const offer = near.length ? near : catalog.symbols.slice(0, 8);
  const offerLabel = near.length ? 'Did you mean' : 'On file, for example';
  const node = el('div', { class: 'card message reveal' },
    el('p', { class: 'message__title', text: `No reading for ${symbol}` }),
    el('p', {
      class: 'message__body',
      text: `This machine only holds the ${catalog.symbols.length} largest ` +
        `Nasdaq-screened stocks. ${symbol} is not one of them.`,
    }));
  if (offer.length) {
    node.append(el('p', { class: 'outlook__name', style: 'margin-top:1rem', text: offerLabel }),
      el('ul', { class: 'chips', style: 'margin-top:.5rem' },
        offer.map((e) => el('li', {}, chip(e, onPick)))));
  }
  return node;
}

export function chip(entry, onPick) {
  const b = el('button', {
    class: 'chip',
    type: 'button',
    'data-tone': stanceTone(entry.stance),
    text: entry.symbol,
    title: entry.stance,
  });
  b.addEventListener('click', () => onPick(entry.symbol));
  return b;
}

// suggestions ranks by how a person types a ticker: prefix first, then
// anything containing what they typed.
export function suggestions(query, catalog, limit = 8) {
  if (!catalog) return [];
  const q = query.trim().toUpperCase();
  if (!q) return [];
  const starts = [];
  const contains = [];
  for (const e of catalog.symbols) {
    if (e.symbol === q) starts.unshift(e);
    else if (e.symbol.startsWith(q)) starts.push(e);
    else if (e.symbol.includes(q)) contains.push(e);
    if (starts.length >= limit) break;
  }
  return [...starts, ...contains].slice(0, limit);
}

// working reports the stages the model actually runs, so the wait shows
// the real pipeline rather than an invented bar.
export function working(symbol) {
  const fill = el('span', { class: 'working__fill' });
  const list = el('ul', { class: 'working__stages' });
  const label = el('p', { class: 'working__label', text: `Analysing ${symbol}` });
  const node = el('div', { class: 'card working reveal' },
    label,
    el('div', { class: 'working__bar' }, fill),
    list);

  return {
    node,
    setStage(stage, fraction) {
      fill.style.width = `${Math.round(Math.min(1, Math.max(0, fraction)) * 100)}%`;
      const last = list.lastElementChild;
      if (last && last.textContent === stage) return;
      list.append(el('li', { text: stage }));
      while (list.children.length > 4) list.firstElementChild.remove();
    },
  };
}
