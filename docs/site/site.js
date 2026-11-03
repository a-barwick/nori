// Shared page shell: navigation, the built/planned filter, the request map and the roadmap grid.
const NAV = [
  ['Start', [['index', 'Overview'], ['product', 'What Nori should do'], ['request', 'Following a request']]],
  ['The loops', [['outer-loop', 'Outer loop'], ['classification', 'Classifying a message'], ['outer-model-failure', 'Outer model failure'], ['inner-loop', 'Inner loop'], ['transcript', 'Task transcript'], ['contract', 'How the loops talk'], ['tasks', 'Task lifecycle'], ['completion', 'Finishing a task']]],
  ['Agent service', [['system', 'Service overview'], ['records', 'Records and delivery'], ['computer', 'Persistent computer'], ['tools', 'Tools'], ['memory', 'Memory and context'], ['results', 'Results and files'], ['budgets', 'Budgets and controls'], ['observability', 'Observability'], ['deploy', 'Deployment'], ['authority', 'Approvals and writes'], ['proactive', 'Calendar offers']]],
  ['Clients', [['clients', 'Clients'], ['api', 'Service API'], ['web-client', 'Web client'], ['frontend', 'Web client architecture'], ['theme', 'Theme']]],
  ['Planned', [['schedules', 'Schedules'], ['roadmap', 'Roadmap']]],
  ['Reference', [['scenarios', 'Scenarios'], ['decisions', 'Decisions'], ['open-questions', 'Open questions'], ['code', 'Code map'], ['words', 'Words we use'], ['reading', 'Reading list'], ['sources', 'Sources']]],
];
const PLANNED = NAV.find(([group]) => group === 'Planned')[1].map(([id]) => id).filter(id => id !== 'roadmap');

document.addEventListener('DOMContentLoaded', () => document.querySelectorAll('table').forEach(table => {
  const names = [...(table.rows[0]?.querySelectorAll('th') || [])].map(th => th.textContent.trim());
  if (names.length < 3) return;
  table.classList.add('stack');
  table.querySelectorAll('tr').forEach(row => [...row.children].forEach((cell, i) => { if (i && cell.tagName === 'TD') cell.dataset.label = names[i]; }));
}));

const current = location.pathname.split('/').pop().replace('.html', '') || 'index';
const flat = NAV.flatMap(([, pages]) => pages);
const at = flat.findIndex(([id]) => id === current);

// The tab icon
document.head.insertAdjacentHTML('beforeend', '<link rel="icon" type="image/svg+xml" href="favicon.svg">');

// Tab titles: Dokyu on the first page, Dokyu · Section on the rest
document.title = current === 'index' ? 'Dokyu' : `Dokyu · ${document.title.split(' · ')[0]}`;

// Layout
const main = document.createElement('main');
main.append(...document.body.childNodes);
const rail = document.createElement('nav');
rail.className = 'rail';
rail.setAttribute('aria-label', 'Pages');
rail.innerHTML = `<div class="brand"><a href="index.html"><img src="favicon.svg" alt="">Nori Dokyu</a><button class="menu-btn" type="button">Pages</button></div>
  <div class="links">${NAV.map(([group, pages]) => `<a class="group" href="${pages[0][0]}.html">${group}</a>` + pages.map(([id, title]) =>
    `<a href="${id}.html"${id === current ? ' aria-current="page"' : ''}>${title}</a>`).join('')).join('')}</div>`;
rail.querySelector('.menu-btn').addEventListener('click', () => rail.classList.toggle('open'));
const layout = document.createElement('div');
layout.className = 'layout';
layout.append(rail, main);
document.body.append(layout);

// Previous and next
const pager = document.createElement('div');
pager.className = 'pager';
const link = (page, label) => page ? `<a href="${page[0]}.html"><span>${label}</span>${page[1]}</a>` : '<i></i>';
pager.innerHTML = link(flat[at - 1], 'Previous') + link(flat[at + 1], 'Next');
main.append(pager);

// Planned pages say so once at the top
if (PLANNED.includes(current)) {
  main.querySelector('.lede').insertAdjacentHTML('afterend', '<p class="planned-note"><span class="tag design" style="margin-left:0">planned, not built yet</span></p>');
}

// Built / planned filter, only on pages that mix the two
const filtered = [...main.querySelectorAll('.designed, .in-review')];
if (filtered.length && !PLANNED.includes(current)) {
  const bar = document.createElement('div');
  bar.className = 'legend';
  bar.setAttribute('role', 'group');
  bar.setAttribute('aria-label', 'Filter by what is built');
  bar.innerHTML = `<span><span class="swatch sw-solid"></span>built</span>
    <span><span class="swatch sw-dashed"></span>designed</span>
    ${main.querySelector('.in-review') ? '<span><span class="swatch sw-dotted"></span>in progress</span>' : ''}
    <span class="viewbar" style="margin-left:auto">
      <button data-view="all" class="on">Show all</button>
      <button data-view="impl">Built only</button>
      <button data-view="design">Highlight planned</button>
    </span><span id="viewStatus" aria-live="polite"></span>`;
  main.querySelector('.lede').after(bar);
  const buttons = bar.querySelectorAll('button');
  const status = bar.querySelector('#viewStatus');
  const noun = filtered.length === 1 ? 'item' : 'items';
  const setView = view => {
    buttons.forEach(b => b.classList.toggle('on', b.dataset.view === view));
    document.body.classList.toggle('only-impl', view === 'impl');
    document.body.classList.toggle('only-design', view === 'design');
    status.textContent = view === 'impl' ? `Hiding ${filtered.length} ${noun} on this page that aren't built yet.`
      : view === 'design' ? `Highlighting ${filtered.length} ${noun} on this page that aren't built yet.` : '';
    try { localStorage.setItem('nori-view', view); } catch {}
  };
  buttons.forEach(b => b.addEventListener('click', () => setView(b.dataset.view)));
  try { const saved = localStorage.getItem('nori-view'); if (saved) setView(saved); } catch {}
  const setBar = () => document.documentElement.style.setProperty('--bar', bar.offsetHeight + 'px');
  new ResizeObserver(setBar).observe(bar);
  setBar();
}

// Section IDs: every heading gets an ID and a copy-link icon on hover. Only the icon is interactive.
const slug = text => text.toLowerCase().replace(/&/g, ' and ').replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
const LINK_ICON = '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><path d="M6.5 9.5a3 3 0 0 0 4.4.1l2-2a3.1 3.1 0 0 0-4.4-4.4l-1 1"/><path d="M9.5 6.5a3 3 0 0 0-4.4-.1l-2 2a3.1 3.1 0 0 0 4.4 4.4l1-1"/></svg>';
const DONE_ICON = '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m3.5 8.5 3 3 6-7"/></svg>';
const copyText = async text => {
  try { await navigator.clipboard.writeText(text); return; } catch {}
  const area = document.createElement('textarea');
  area.value = text;
  document.body.append(area);
  area.select();
  document.execCommand('copy');
  area.remove();
};
const usedIds = new Set();
const heads = [...main.querySelectorAll('h1, h2, h3, .step .t, .scn > summary')].filter(h => h.matches('summary') || !h.closest('details'));
heads.forEach(head => {
  const nodes = [...head.childNodes];
  const end = nodes.findIndex(n => n.matches?.('.tag, a, .mono'));
  const titleNodes = end < 0 ? nodes : nodes.slice(0, end);
  const title = titleNodes.map(n => n.textContent).join('').trim();
  const isPage = head.matches('h1');
  let id = head.id || slug(title);
  for (let n = 2; !isPage && usedIds.has(id); n++) id = `${slug(title)}-${n}`;
  if (!isPage) { usedIds.add(id); head.id = id; }
  const target = isPage ? location.pathname : `#${id}`;
  head.dataset.title = title;
  const copy = document.createElement('a');
  copy.className = 'copy-link';
  copy.href = target;
  copy.title = 'Copy link to this section';
  copy.setAttribute('aria-label', `Copy link to ${title}`);
  copy.innerHTML = LINK_ICON;
  copy.addEventListener('click', async e => {
    e.preventDefault();
    history.replaceState(null, '', target);
    await copyText(location.href);
    copy.innerHTML = DONE_ICON;
    copy.classList.add('copied');
    copy.title = 'Copied';
    setTimeout(() => { copy.innerHTML = LINK_ICON; copy.classList.remove('copied'); copy.title = 'Copy link to this section'; }, 1500);
  });
  head.insertBefore(copy, end < 0 ? null : nodes[end]);
});

// The rail lists the current page's sections and follows the scroll position
const railHeads = heads.filter(h => h.matches('h2, .t'));
const currentLink = rail.querySelector('a[aria-current]');
if (railHeads.length > 1 && currentLink) {
  const subs = document.createElement('div');
  subs.className = 'subs';
  railHeads.forEach(head => {
    const a = document.createElement('a');
    a.href = `#${head.id}`;
    a.textContent = head.dataset.title;
    if (head.closest('.designed')) a.className = 'designed';
    subs.append(a);
  });
  currentLink.after(subs);
  const subLinks = [...subs.children];
  let queued = false;
  const follow = () => {
    queued = false;
    const line = (parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--bar')) || 0) + 90;
    const step = main.querySelector('.step.cur .t');
    const i = step ? railHeads.indexOf(step) : railHeads.findLastIndex(h => h.getClientRects().length && h.getBoundingClientRect().top <= line);
    subLinks.forEach((a, j) => a.classList.toggle('on', i === j));
  };
  addEventListener('scroll', () => { if (!queued) { queued = true; requestAnimationFrame(follow); } }, { passive: true });
  document.addEventListener('stepchange', follow);
  follow();
}

// IDs are added after load, so scroll to a linked section here
const openLinked = () => {
  const target = location.hash && document.getElementById(decodeURIComponent(location.hash.slice(1)));
  if (!target) return;
  target.closest('details')?.setAttribute('open', '');
  target.scrollIntoView();
};
openLinked();
addEventListener('hashchange', openLinked);

// Data panels: simulated requests, responses and saved records behind a step or a table row
const esc = text => String(text).replace(/[&<>]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c]));
const json = value => esc(JSON.stringify(value, null, 2)).replace(/("(?:[^"\\]|\\.)*")(: )?/g, (_, str, colon) =>
  colon ? `<span class="k">${str}</span>: ` : `<span class="s">${str}</span>`);
const block = (label, value) => value === undefined ? '' :
  `<div class="ex-label">${label}</div><pre class="json"><code>${json(value)}</code></pre>`;
const dataPanel = exchanges => {
  const panel = document.createElement('div');
  panel.className = 'step-data';
  panel.innerHTML = `<p class="note">Simulated example. The endpoints and field names follow the code, and “…” marks what's been shortened.</p>` +
    exchanges.map((x, i) => `<details class="ex"${x.edge ? ` data-edge="${x.edge}"` : ''}${i === 0 ? ' open' : ''}>
      <summary><span class="ex-route">${esc(x.from)} → ${esc(x.to)}</span><code>${esc(x.call)}</code></summary>
      ${x.note ? `<p class="note">${esc(x.note)}</p>` : ''}
      ${block('Request', x.request)}${block('Response', x.response)}${block('Saved', x.saved)}
      ${x.other ? `<div class="ex-label">Other outcomes</div><ul class="pts note">${x.other.map(o => `<li>${esc(o)}</li>`).join('')}</ul>` : ''}
      ${x.source ? `<p class="ex-src">${esc(x.source)}</p>` : ''}</details>`).join('');
  return panel;
};
const dataButton = onToggle => {
  const button = document.createElement('button');
  button.className = 'data-btn';
  button.type = 'button';
  button.textContent = 'Data';
  button.setAttribute('aria-expanded', 'false');
  button.addEventListener('click', () => {
    const open = button.getAttribute('aria-expanded') !== 'true';
    button.setAttribute('aria-expanded', String(open));
    onToggle(open);
  });
  return button;
};
main.querySelectorAll('tr[data-flow]').forEach(row => {
  const exchanges = window.FLOWS?.[row.dataset.flow];
  if (!exchanges) return;
  const extra = document.createElement('tr');
  extra.className = 'data-row';
  extra.hidden = true;
  const cell = extra.insertCell();
  cell.colSpan = row.cells.length;
  cell.append(dataPanel(exchanges));
  row.after(extra);
  row.cells[0].append(dataButton(open => { extra.hidden = !open; }));
});

// Request page: light up the systems the current step uses
if (document.querySelector('.trace')) {
  const NAMES = { user: 'User', tasks: 'Cloud Tasks', outer: 'Outer loop', runtime: 'Runtime', firestore: 'Firestore',
    temporal: 'Temporal', models: 'Models', inner: 'Inner loop', computer: 'Computer' };
  const TRACE = [
    { cap: 'The runtime saves the message, then schedules a wake.', nodes: ['user', 'runtime', 'firestore', 'tasks'],
      edges: ['user>service', 'service>firestore', 'service>tasks'] },
    { cap: 'A wake starts an outer turn, which takes the lease and pending inputs.', nodes: ['tasks', 'runtime', 'outer', 'firestore'],
      edges: ['tasks>service', 'firestore>service'] },
    { cap: 'The outer loop calls Jev and then the outer model. Usage is saved before each call.', nodes: ['outer', 'models', 'firestore'],
      edges: ['service>models', 'service>firestore'] },
    { cap: 'Commands and the reply are saved. The event stream sends the reply to the user.', nodes: ['outer', 'runtime', 'firestore', 'user'],
      edges: ['service>firestore', 'service>user'] },
    { cap: 'The relay reads saved commands and starts one workflow per task.', nodes: ['runtime', 'firestore', 'temporal'],
      edges: ['firestore>service', 'service>temporal'] },
    { cap: 'Two inner loops run independently, calling their model and using the computer.', nodes: ['temporal', 'inner', 'models', 'computer', 'firestore'],
      edges: ['temporal>inner', 'inner>models', 'inner>computer', 'inner>firestore'] },
    { cap: 'The result reaches the inbox, and an outer turn explains it to the user.', nodes: ['inner', 'firestore', 'runtime', 'outer', 'models', 'user'],
      edges: ['inner>firestore', 'firestore>service', 'service>models', 'service>user'] },
  ];
  const steps = [...document.querySelectorAll('.trace .step')];
  const cap = document.getElementById('traceCap');
  let last = 0;

  const showStep = i => {
    const t = TRACE[i];
    steps.forEach((step, j) => step.classList.toggle('cur', i === j));
    cap.innerHTML = `<b>Step ${i + 1} of ${TRACE.length}</b>${t.cap}`;
    document.dispatchEvent(new Event('stepchange'));
    const active = new Set(t.nodes);
    if (active.has('outer') || active.has('runtime')) active.add('service');
    document.querySelectorAll('.tn').forEach(n => n.classList.toggle('on', active.has(n.dataset.node)));
    document.querySelectorAll('.te').forEach(e => {
      const [a, b] = e.dataset.edge.split('-');
      const fwd = t.edges.includes(`${a}>${b}`), back = t.edges.includes(`${b}>${a}`);
      e.classList.toggle('on', fwd || back);
      e.classList.toggle('rev', back);
      e.setAttribute('marker-end', fwd ? 'url(#ah3)' : 'none');
      e.setAttribute('marker-start', back ? 'url(#ah3)' : 'none');
    });
  };
  steps.forEach((step, i) => {
    const line = document.createElement('div');
    line.className = 'step-sys';
    line.textContent = TRACE[i].nodes.map(n => NAMES[n]).join(' · ');
    step.lastElementChild.append(line);
    step.addEventListener('click', () => { last = i; showStep(i); });
    const exchanges = window.REQUEST_FLOW?.[i];
    if (!exchanges) return;
    const panel = dataPanel(exchanges);
    panel.hidden = true;
    panel.addEventListener('click', e => e.stopPropagation());
    panel.querySelectorAll('.ex').forEach(x => {
      const edge = document.querySelector(`.te[data-edge="${x.dataset.edge}"]`);
      x.addEventListener('mouseenter', () => edge?.classList.add('hot'));
      x.addEventListener('mouseleave', () => edge?.classList.remove('hot'));
    });
    line.before(panel);
    step.querySelector('.t').after(dataButton(open => { panel.hidden = !open; }));
  });
  // A link to a step shows that step until the reader starts scrolling
  const linked = steps.findIndex(step => step.querySelector('.t').id === decodeURIComponent(location.hash.slice(1)));
  let pinned = linked >= 0;
  ['wheel', 'touchmove', 'keydown'].forEach(type => addEventListener(type, () => { pinned = false; }, { passive: true }));
  last = Math.max(linked, 0);
  showStep(last);
  // The current step is the one crossing a line 64% down the window
  let queued = false;
  const follow = () => {
    queued = false;
    if (pinned) return;
    const line = innerHeight * 0.64;
    const i = steps.findLastIndex(step => step.getBoundingClientRect().top <= line);
    if (i >= 0 && i !== last) { last = i; showStep(i); }
  };
  addEventListener('scroll', () => { if (!queued) { queued = true; requestAnimationFrame(follow); } }, { passive: true });
}

// Roadmap grid
const road = document.getElementById('roadGrid');
if (road && window.ROAD) {
  road.innerHTML = window.ROAD.map(([n, t, d, tot, note]) =>
    `<div class="mile${n === 4 ? ' now' : ''}${n >= 7 ? ' designed' : ''}">
       <span class="mn">M${n}</span><span class="mt">${t}</span>
       <span class="mp">${note}</span>
       <div class="bar"><i style="width:${Math.round(d / tot * 100)}%"></i></div>
       <span class="mp">${d}/${tot} done</span>
     </div>`).join('');
}
