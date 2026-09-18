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
    el('p', { class: 'fair__label', text: title }),
    el('div', { class: 'signals' }, rows.map(signalRow)));
}

// ------------------------------------------------------------ fair value

// fairCard is the heart of the page: what the shares cost, what the
// filings say they are worth, and the distance between the two.
function fairCard(view) {
  const f = view.fair;
  const cheap = f.upside > 0;

  const card = el('div', { class: 'card fair' },
    el('div', { class: 'fair__pair' },
      el('div', { class: 'fair__side' },
        el('p', { class: 'fair__label', text: 'Trading at' }),
        el('p', { class: 'fair__price', text: `$${money(view.spot)}` })),
      el('div', { class: 'fair__arrow', 'data-tone': cheap ? 'bull' : 'bear' },
        el('span', { text: cheap ? '\u2192' : '\u2192' })),
      el('div', { class: 'fair__side' },
        el('p', { class: 'fair__label', text: 'Looks worth' }),
        el('p', { class: 'fair__price', 'data-tone': cheap ? 'bull' : 'bear',
          text: `$${money(f.value)}` }))),
    el('p', { class: 'fair__gap', 'data-tone': cheap ? 'bull' : 'bear' },
      // "above" already carries the direction; a minus sign as well reads
      // as a double negative.
      `${Math.abs(100 * f.upside).toFixed(1)}% ${cheap ? 'below' : 'above'} what the filings support`));

  const detail = el('div', { class: 'fair__detail' },
    stat('Confidence', f.confidence, `the models span ${Math.round(100 * (f.spread ?? 0))}% of the estimate`),
    stat('Discount rate', `${(100 * f.discount).toFixed(1)}%`, 'risk-free plus this share of market risk'),
    stat('Growth assumed', `${(100 * f.growth).toFixed(1)}%/yr`, 'from the analysts, capped'),
  );
  if (f.trailingPE) {
    detail.append(stat('Price / earnings', f.trailingPE.toFixed(1),
      f.forwardPE ? `${f.forwardPE.toFixed(1)} on next year's` : 'trailing'));
  }
  card.append(detail);

  if (f.methods?.length) {
    const list = el('div', { class: 'methods' });
    for (const m of f.methods) {
      list.append(el('div', { class: 'method' },
        el('span', { class: 'method__name', text: sentence(m.name) }),
        el('span', { class: 'method__value', text: `$${money(m.value)}` }),
        el('span', { class: 'method__weight', text: `${Math.round(100 * m.weight)}%` }),
        el('span', { class: 'method__note', text: m.note })));
    }
    card.append(el('details', { class: 'methods__wrap' },
      el('summary', { text: 'How that was worked out' }), list));
  }
  return card;
}

function stat(label, value, note) {
  return el('div', { class: 'ministat' },
    el('span', { class: 'ministat__label', text: label }),
    el('span', { class: 'ministat__value', text: value }),
    note ? el('span', { class: 'ministat__note', text: note }) : null);
}

// --------------------------------------------------------------- timing

function timingCard(t) {
  if (!t) {
    return el('div', { class: 'card plan--empty' },
      'This multiple has not returned to its own level reliably enough to time. ' +
      'A gap can stay open for years, and pretending otherwise would be the ' +
      'least honest thing on this page.');
  }
  return el('div', { class: 'card' },
    el('p', { class: 'timing__head' },
      el('span', { class: 'timing__value', text: monthsLabel(t.medianMonths) }),
      el('span', { class: 'timing__note', text: 'for half of simulated paths to reach fair value' })),
    el('div', { class: 'fair__detail' },
      stat('Half the gap closes in', monthsLabel(t.halfLifeMonths), 'on the fitted reversion speed'),
      stat('Arrive within a year', `${Math.round(100 * t.withinYear)}%`, 'of simulated paths')));
}

function monthsLabel(m) {
  if (!m || m <= 0) return 'no estimate';
  if (m < 1.5) return 'about a month';
  if (m < 24) return `${Math.round(m)} months`;
  return `${(m / 12).toFixed(1)} years`;
}

// ----------------------------------------------------------------- notes

function notes(view, catalog) {
  const items = [];
  if (view.earnings) {
    items.push(`Earnings on ${view.earnings}, with the options market pricing a ` +
      `${percent(view.impliedMove)} move around it.`);
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

  if (view.fair) {
    frag.append(section('What it looks worth', fairCard(view)));
    frag.append(section('How long that might take', timingCard(view.timing)));
  }

  const groups = [
    signalGroup('Supporting', view.for),
    signalGroup('Against', view.against),
  ].filter(Boolean);
  if (groups.length) {
    frag.append(section('Why', el('div', { class: 'grid grid--two' }, groups)));
  }

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
