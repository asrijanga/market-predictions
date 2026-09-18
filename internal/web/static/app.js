// MKTPREDICT - a machine that answers one question at a time.
//
// The page works two ways from the same code. Served by `mktpredict serve`
// it streams a live analysis over server-sent events. Published as a static
// site it reads answers computed at build time, because a browser cannot
// reach the market data providers directly: none of them send CORS headers.
import * as report from './report.js';

const form = document.getElementById('search-form');
const input = document.getElementById('symbol');
const suggestBox = document.getElementById('suggestions');
const hint = document.getElementById('search-hint');
const meta = document.getElementById('catalog-meta');
const result = document.getElementById('result');

const state = {
  catalog: null,   // null means a server computes on demand
  api: '',         // base URL of that server; empty means same origin
  matches: [],
  active: -1,
  stream: null,
  current: '',
};

const SYMBOL = /^[A-Z][A-Z.\-]{0,5}$/;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ------------------------------------------------------------- the catalog

// loadConfig finds the server that computes. A published site is served
// from a static host that cannot run the model, so it carries the address
// of one that can; served by that server itself, there is no config and
// same-origin is right.
async function loadConfig() {
  try {
    const res = await fetch('./config.json', { cache: 'no-cache' });
    if (!res.ok) return '';
    const cfg = await res.json();
    return typeof cfg.api === 'string' ? cfg.api.replace(/\/$/, '') : '';
  } catch {
    return '';
  }
}

async function loadCatalog() {
  try {
    const res = await fetch('./data/index.json', { cache: 'no-cache' });
    if (!res.ok) return null;
    const catalog = await res.json();
    return Array.isArray(catalog.symbols) && catalog.symbols.length ? catalog : null;
  } catch {
    return null;
  }
}

function describeCatalog() {
  if (!state.catalog) {
    meta.textContent = 'live · computed on demand';
    hint.textContent = 'Any listed US stock with options.';
    return;
  }
  const n = state.catalog.symbols.length;
  meta.textContent = `${n} symbols · ${state.catalog.asOf}`;

  const examples = ['AAPL', 'NVDA', 'MSFT'].filter(
    (s) => state.catalog.symbols.some((e) => e.symbol === s));
  hint.replaceChildren(
    document.createTextNode(
      `${n} stocks on file${examples.length ? `. Try ${examples.join(', ')}, or pick one below.` : '.'}`));
}

// ---------------------------------------------------------- the suggestion

function renderSuggestions() {
  if (!state.matches.length) {
    suggestBox.hidden = true;
    suggestBox.replaceChildren();
    input.setAttribute('aria-expanded', 'false');
    return;
  }
  suggestBox.replaceChildren(...state.matches.map((entry, i) => {
    const li = document.createElement('li');
    li.className = 'suggest__item';
    li.role = 'option';
    li.setAttribute('aria-selected', String(i === state.active));

    const sym = document.createElement('span');
    sym.className = 'suggest__sym';
    sym.textContent = entry.symbol;

    const stance = document.createElement('span');
    stance.className = 'suggest__stance';
    stance.textContent = entry.stance;

    li.append(sym, stance);
    // pointerdown fires before the input loses focus, so the click is not
    // swallowed by the blur that closes the list.
    li.addEventListener('pointerdown', (e) => { e.preventDefault(); run(entry.symbol); });
    return li;
  }));
  suggestBox.hidden = false;
  input.setAttribute('aria-expanded', 'true');
}

function closeSuggestions() {
  state.matches = [];
  state.active = -1;
  renderSuggestions();
}

input.addEventListener('input', () => {
  state.matches = report.suggestions(input.value, state.catalog);
  state.active = -1;
  renderSuggestions();
});

input.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { closeSuggestions(); return; }
  if (!state.matches.length) return;
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault();
    const step = e.key === 'ArrowDown' ? 1 : -1;
    state.active = (state.active + step + state.matches.length) % state.matches.length;
    renderSuggestions();
  }
});

input.addEventListener('blur', () => setTimeout(closeSuggestions, 120));

form.addEventListener('submit', (e) => {
  e.preventDefault();
  const picked = state.active >= 0 ? state.matches[state.active]?.symbol : null;
  run(picked ?? input.value);
});

// -------------------------------------------------------------- the browse

// A few hundred chips make a page nobody scrolls, so the resting view
// shows a first screenful and offers the rest.
const PREVIEW = 60;

function showBrowse({ all = false } = {}) {
  closeSuggestions();
  if (!state.catalog) return;
  const total = state.catalog.symbols.length;
  const shown = all ? state.catalog.symbols : state.catalog.symbols.slice(0, PREVIEW);

  const wrap = document.createElement('section');
  wrap.className = 'section reveal';

  const title = document.createElement('h2');
  title.className = 'section__title';
  title.textContent = all || total <= PREVIEW
    ? `All ${total} symbols`
    : `${total} symbols on file`;

  const list = document.createElement('ul');
  list.className = 'chips';
  for (const entry of shown) {
    const li = document.createElement('li');
    li.append(report.chip(entry, run));
    list.append(li);
  }
  wrap.append(title, list);

  if (!all && total > PREVIEW) {
    const more = document.createElement('p');
    more.className = 'search__hint';
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = `show all ${total}`;
    button.addEventListener('click', () => showBrowse({ all: true }));
    more.append(document.createTextNode(`Showing ${shown.length} of ${total} \u2014 `), button);
    wrap.append(more);
  }
  result.replaceChildren(wrap);
}

// ------------------------------------------------------------- the running

function show(node) {
  result.replaceChildren(node);
}

async function run(raw, { push = true } = {}) {
  const symbol = String(raw ?? '').trim().toUpperCase().replace(/[^A-Z.\-]/g, '');
  closeSuggestions();
  input.blur();
  if (!SYMBOL.test(symbol)) {
    show(report.message('Type a ticker', 'Something like AAPL, NVDA or MSFT.'));
    return;
  }

  state.stream?.close();
  state.stream = null;
  state.current = symbol;
  input.value = symbol;
  if (push) {
    const url = `${location.pathname}?s=${encodeURIComponent(symbol)}`;
    if (location.search !== `?s=${symbol}`) history.pushState({ symbol }, '', url);
  }
  document.title = `${symbol} — MKTPREDICT`;

  const progress = report.working(symbol);
  result.setAttribute('aria-busy', 'true');
  show(progress.node);

  if (state.catalog) await runPublished(symbol, progress);
  else runLive(symbol, progress);
}

// runPublished reads an answer computed at build time. The stages are
// replayed at a readable pace rather than invented: these are the steps
// that actually produced the numbers.
async function runPublished(symbol, progress) {
  const known = state.catalog.symbols.some((s) => s.symbol === symbol);
  if (!known) {
    finish(report.unknown(symbol, state.catalog, run));
    return;
  }
  const fetching = fetch(`./data/${encodeURIComponent(symbol)}.json`, { cache: 'no-cache' });
  const stages = state.catalog.stages?.length
    ? state.catalog.stages : ['loading the published analysis'];

  for (let i = 0; i < stages.length; i++) {
    if (state.current !== symbol) return;   // a newer request took over
    progress.setStage(stages[i], (i + 1) / (stages.length + 1));
    await sleep(1400 / stages.length);
  }
  try {
    const res = await fetching;
    if (!res.ok) throw new Error(String(res.status));
    const view = await res.json();
    if (state.current !== symbol) return;
    finish(report.analysis(view, state.catalog));
  } catch {
    finish(report.message('Could not read that analysis',
      `${symbol} is in the catalog but its file could not be loaded. Try again.`));
  }
}

function runLive(symbol, progress) {
  const stream = new EventSource(`${state.api}/api/analyze?symbol=${encodeURIComponent(symbol)}`);
  state.stream = stream;

  stream.addEventListener('stage', (e) => {
    const { stage, fraction } = JSON.parse(e.data);
    progress.setStage(stage, fraction);
  });
  stream.addEventListener('result', (e) => {
    stream.close();
    state.stream = null;
    finish(report.analysis(JSON.parse(e.data), null));
  });
  stream.addEventListener('error', (e) => {
    stream.close();
    state.stream = null;
    let text = 'The server did not answer. Try again in a moment.';
    try { text = JSON.parse(e.data).message; } catch { /* transport failure */ }
    finish(report.message('No reading', text));
  });
}

function finish(node) {
  result.setAttribute('aria-busy', 'false');
  show(node);
}

// ------------------------------------------------------------- the wiring

addEventListener('popstate', () => {
  const symbol = new URLSearchParams(location.search).get('s');
  if (symbol) run(symbol, { push: false });
  else atRest();
});

(async () => {
  // A site that names a server computes on demand and ships no catalog,
  // so asking for one would only log a 404 on every visit.
  state.api = await loadConfig();
  state.catalog = state.api ? null : await loadCatalog();
  describeCatalog();
  const deep = new URLSearchParams(location.search).get('s');
  if (deep) {
    run(deep, { push: false });
    return;
  }
  atRest();
  // Focusing on a touch screen throws up the keyboard over the catalog
  // before the reader has seen it.
  if (matchMedia('(pointer: fine)').matches) input.focus();
})();

// atRest is what the page shows with nothing asked yet: the catalog, so
// the first frame demonstrates what the machine holds instead of waiting.
function atRest() {
  state.current = '';
  input.value = '';
  document.title = 'MKTPREDICT';
  result.setAttribute('aria-busy', 'false');
  if (state.catalog) showBrowse();
  else result.replaceChildren(report.message('Ready',
    'Type any listed US ticker with traded options and it will be analysed on demand.'));
}
