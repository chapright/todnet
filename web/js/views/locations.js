// Locations: a searchable, keyboard-navigable grid with favorites and recents.
// No internal edge hostnames in the primary UI (U-05) — the host lives in the
// tooltip and in Connection details.

import { t } from '../i18n.js';
import { icon } from '../icons.js';
import { h, button, iconButton, flagChip, emptyState, skeleton, signalBars, segmented, segmentedSet } from '../components/ui.js';
import { countryName, ltrIsolate } from '../describeState.js';
import { flip, stagger } from '../motion.js';

// Every country Mozilla publishes has a region. CH, PL and IN were missing
// and fell into an "Other" bucket at the very bottom of the list, which
// is where a Swiss user looking for Switzerland would not look.
//
// The values are translation keys, not names: the headings are read in the
// interface's own language.
const REGION = {
  AR: 'region.southAmerica', AT: 'region.europe', AU: 'region.oceania', BE: 'region.europe', BG: 'region.europe', BR: 'region.southAmerica',
  CA: 'region.northAmerica', CH: 'region.europe', CL: 'region.southAmerica', CO: 'region.southAmerica', DE: 'region.europe', DK: 'region.europe',
  ES: 'region.europe', FI: 'region.europe', FR: 'region.europe', GB: 'region.europe', GH: 'region.africa', IE: 'region.europe', IN: 'region.asia',
  IT: 'region.europe', JP: 'region.asia', KR: 'region.asia', MX: 'region.northAmerica', MY: 'region.asia', NL: 'region.europe', NO: 'region.europe',
  NZ: 'region.oceania', PE: 'region.southAmerica', PH: 'region.asia', PL: 'region.europe', PT: 'region.europe', SE: 'region.europe',
  SG: 'region.asia', TH: 'region.asia', US: 'region.northAmerica', ZA: 'region.africa',
};
const REGION_ORDER = [
  'region.europe', 'region.northAmerica', 'region.asia', 'region.southAmerica',
  'region.oceania', 'region.africa', 'region.other',
];

export function createLocations(app) {
  const root = h('section', { class: 'view', id: 'view-locations' });
  const inner = h('div', { class: 'view-inner' });

  const countBadge = h('span', { class: 'loc-count', id: 'locCount', text: '' });
  const refreshBtn = iconButton({ name: 'refresh', label: t('loc.refresh'), size: 'lg', onClick: () => app.refreshLocations() });
  const head = h('div', { class: 'view-head' }, [
    h('div', {}, [
      h('h1', { class: 'view-title', text: t('loc.title') }),
      h('p', { class: 'view-sub', text: t('loc.sub') }),
    ]),
  ]);

  const search = h('input', {
    class: 'input',
    type: 'search',
    id: 'locSearch',
    placeholder: t('loc.search'),
    autocomplete: 'off',
    'aria-label': t('loc.search'),
    'data-tip': t('loc.searchShortcut'),
  });
  const searchWrap = h('div', { class: 'input-wrap loc-search' }, [
    h('span', { html: icon('search', { size: 16 }) }),
    search,
  ]);

  let filter = 'all';
  // The canonical segmented control. This view used to carry its own copy
  // of the markup and of the thumb-placement logic, and so did the Logs
  // view: three implementations of one component.
  const filterSeg = segmented({
    label: t('loc.title'),
    value: filter,
    options: [['all', t('loc.all')], ['favorites', t('loc.favorites')], ['recent', t('loc.recent')]],
    onChange: (v) => {
      filter = v;
      // The control is updated here rather than at the end of render(),
      // because render() returns early when the filtered list is empty and
      // the selected option would then never be reflected.
      segmentedSet(filterSeg, v);
      render();
    },
  });

  const tools = h('div', { class: 'loc-tools' }, [searchWrap, filterSeg, h('span', { class: 'grow' }), countBadge, refreshBtn]);
  const list = h('div', { id: 'locList' });
  inner.append(head, tools, list);
  root.append(inner);

  let query = '';
  let mode = 'loading'; // loading | ready | error | empty
  let lastLocations = null;

  search.addEventListener('input', () => {
    query = search.value.trim().toLowerCase();
    render();
  });
  search.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && search.value) {
      e.stopPropagation();
      search.value = '';
      query = '';
      render();
    }
    if (e.key === 'ArrowDown') {
      const first = list.querySelector('.loc-row');
      if (first) { e.preventDefault(); first.focus(); }
    }
  });

  function toggleFavorite(code, btn) {
    btn.classList.add('pop');
    setTimeout(() => btn.classList.remove('pop'), 340);
    app.toggleFavorite(code);
  }

  // The name a row shows, in the interface's own language. The server sends
  // Mozilla's English string, which no locale would translate.
  function displayName(loc) {
    return countryName(loc.countryCode) || loc.countryName;
  }

  function row(loc, selectedCc, favorites, best) {
    const key = `${loc.countryCode}|${loc.cityCode}`;
    const isSel = String(loc.countryCode || '').toUpperCase() === selectedCc;
    const isFav = favorites.includes(loc.countryCode);
    const name = displayName(loc);
    const btn = h('button', {
      class: 'loc-row',
      type: 'button',
      'data-flip': key,
      'data-key': key,
      'aria-pressed': isSel ? 'true' : 'false',
      title: loc.host || loc.countryName,
    }, [
      flagChip(loc.countryCode),
      h('span', { class: 'col grow' }, [
        h('span', { class: 'loc-name truncate', text: name }),
        // The measured latency comes from the edge itself now, so the row
        // can say how far away it is. The edge count is shown next to it
        // when a country has more than one.
        h('span', { class: 'loc-sub' }, [
          loc.latencyMs ? latencyBand(loc.latencyMs) : null,
          loc._edges > 1 ? t('loc.edges', { n: loc._edges }) : null,
        ].filter(Boolean).join(' · ')),
      ]),
      loc.latencyMs ? signalBars(latencyLevel(loc.latencyMs, best)) : null,
    ]);
    const star = iconButton({
      name: isFav ? 'star-fill' : 'star',
      // The star names the same country the row does. It named the server's
      // English name, so a Persian row read "اتریش" beside a button that
      // said "Austria".
      label: isFav ? t('loc.favRemove', { country: name }) : t('loc.favAdd', { country: name }),
      size: 'sm',
      cls: 'fav-btn' + (isFav ? ' is-fav' : ''),
    });
    star.addEventListener('click', (e) => {
      e.stopPropagation();
      toggleFavorite(loc.countryCode, star);
    });
    if (isSel) {
      btn.append(h('span', { class: 'loc-end', html: `<svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true"><path class="loc-check" d="M5 12.5l4.4 4.4L19 7.2" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>` }));
    }
    // The star is a sibling of the row, not a child: a control inside a
    // control is invalid, and the star has to be a stop of its own.
    const item = h('div', { class: 'loc-item' + (isSel ? ' is-selected' : '') }, [btn, star]);
    btn.addEventListener('click', () => app.selectLocation({
      ...loc,
      // With several edges behind one row, leave the city empty so the engine
      // picks the fastest instead of pinning the first one listed.
      cityCode: loc._edges > 1 ? '' : loc.cityCode,
    }));
    return item;
  }

  // The edges are anycast: every country resolves to the nearest Fastly
  // point of presence, so thirty of them measure the same one or two
  // milliseconds. A number to the millisecond is therefore false
  // precision, and the bars have to carry the signal instead.
  //
  // The measurement is still worth taking: it is the real path the tunnel
  // will take, and it is the one thing that separates an edge whose point
  // of presence is far away from the rest.
  function latencyBand(ms) {
    if (!ms) return '';
    // The number and its unit have to stay together, whatever the
    // paragraph around them is.
    if (ms < 10) return ltrIsolate('<10 ms');
    return ltrIsolate(Math.round(ms / 10) * 10 + ' ms');
  }

  // The bars are relative to the fastest measured edge, so the list says
  // "this one is closest" rather than repeating the same number.
  function latencyLevel(ms, best) {
    if (!ms || !best) return 0;
    const ratio = ms / best;
    if (ratio <= 1.15) return 4;
    if (ratio <= 1.6) return 3;
    if (ratio <= 2.5) return 2;
    return 1;
  }

  // The fastest measurement in the list.
  function bestLatency(pool) {
    let best = 0;
    for (const l of pool) {
      if (l.latencyMs && (!best || l.latencyMs < best)) best = l.latencyMs;
    }
    return best;
  }

  function render() {
    const state = app.state();
    if (!state) return;
    // The selected option is always reflected, even when the list is
    // empty and the function returns before the end.
    segmentedSet(filterSeg, filter);
    const favorites = (state.settings && state.settings.favorites) || [];
    const recent = (state.settings && state.settings.recent) || [];
    const selected = state.status.location || preferred(state.settings, lastLocations);
    const selectedKey = selected ? `${selected.countryCode}|${selected.cityCode}` : '';
    const selectedCc = selected ? String(selected.countryCode || '').toUpperCase() : '';

    if (mode === 'loading') {
      list.replaceChildren(h('div', { class: 'loc-skeleton' }, Array.from({ length: 9 }, () => skeleton())));
      return;
    }
    if (mode === 'error') {
      list.replaceChildren(emptyState({
        iconName: 'alert',
        title: t('loc.error.title'),
        text: t('loc.error.text'),
        action: button({ label: t('common.retry'), variant: 'secondary', onClick: () => app.refreshLocations() }),
      }));
      return;
    }
    if (mode === 'empty') {
      list.replaceChildren(emptyState({
        iconName: 'globe',
        title: t('loc.empty.title'),
        text: t('loc.empty.text'),
        action: button({ label: t('common.retry'), variant: 'secondary', onClick: () => app.refreshLocations() }),
      }));
      return;
    }
    if (!lastLocations) return;

    let pool = lastLocations.slice();
    if (filter === 'favorites') pool = pool.filter((l) => favorites.includes(l.countryCode));
    if (filter === 'recent') {
      const order = new Map(recent.map((c, i) => [c, i]));
      pool = pool.filter((l) => order.has(l.countryCode)).sort((a, b) => order.get(a.countryCode) - order.get(b.countryCode));
    }
    // How many there were before the search. The empty state has to say why
    // the list is empty: a search that matched nothing is not a list without
    // favourites.
    const beforeSearch = pool.length;
    if (query) {
      // The row shows the country in the interface's own language, so the
      // search has to know that name too: it compared the query against the
      // server's English name only, and a Persian user who typed the name
      // they were looking at found nothing.
      pool = pool.filter((l) =>
        `${countryName(l.countryCode)} ${l.countryName} ${l.countryCode}`.toLowerCase().includes(query),
      );
    }

    const rec = pool.find((l) => String(l.countryCode).toUpperCase() === 'REC');
    // The Remote Settings list can contain several edges per country, and the
    // API returns the country name as the city name, so one row per edge showed
    // the same label three times (audit U-05). Collapse to one row per country
    // and let the engine pick the best edge.
    const byCountry = new Map();
    for (const loc of pool) {
      const cc = String(loc.countryCode || '').toUpperCase();
      if (!cc || cc === 'REC') continue;
      const seen = byCountry.get(cc);
      if (seen) { seen._edges += 1; continue; }
      byCountry.set(cc, { ...loc, _edges: 1 });
    }
    const countries = [...byCountry.values()];

    const frag = document.createDocumentFragment();
    if (rec) {
      const sel = `${rec.countryCode}|${rec.cityCode}` === selectedKey;
      const recRow = h('button', {
        class: 'rec-row' + (sel ? ' is-selected' : ''),
        type: 'button',
        'aria-pressed': sel ? 'true' : 'false',
      }, [
        h('span', { class: 'rec-badge', html: icon('bolt', { size: 19 }) }),
        h('span', { class: 'rec-text grow' }, [
          h('span', { class: 'rec-name', text: t('loc.recommended') }),
          h('span', { class: 'rec-sub', text: t('loc.recommendedSub') }),
        ]),
        h('span', { class: 'rec-action', text: sel ? t('loc.selected') : t('loc.use') }),
      ]);
      recRow.addEventListener('click', () => app.selectLocation(rec));
      frag.append(recRow);
    }

    if (!pool.length) {
      // "No favourites" is only true when the favourites are gone. A search
      // that matched nothing emptied the list, and the message said the
      // favourites were gone while the badge counted them.
      const favEmpty = filter === 'favorites' && beforeSearch === 0;
      frag.append(emptyState({
        iconName: favEmpty ? 'star' : 'search',
        title: t(favEmpty ? 'loc.noFavorites.title' : 'loc.noResults.title'),
        text: t(favEmpty ? 'loc.noFavorites.text' : 'loc.noResults.text'),
      }));
      list.replaceChildren(frag);
      // The badge counts what the list shows, so an empty list counts
      // nothing. It used to keep the number it had before.
      countBadge.textContent = t('loc.count', { n: 0 });
      return;
    }

    const best = bestLatency(countries);
    const groups = new Map();
    for (const loc of countries) {
      const region = REGION[String(loc.countryCode).toUpperCase()] || 'region.other';
      if (!groups.has(region)) groups.set(region, []);
      groups.get(region).push(loc);
    }
    const ordered = [...groups.keys()].sort((a, b) => REGION_ORDER.indexOf(a) - REGION_ORDER.indexOf(b));

    if (filter === 'recent') {
      // "Recent" means the most recently used first. The region headings and
      // the alphabetical order within them threw that away, and the country
      // used last was the last thing in the list.
      frag.append(h('div', { class: 'loc-group' }, [
        h('div', { class: 'loc-grid' }, countries.map((loc) => row(loc, selectedCc, favorites, best))),
      ]));
    } else {
      for (const region of ordered) {
        // The rows are sorted by the name they show. Sorting by the
        // server's English name put "South Korea" where "Korea" belongs,
        // because the row reads "South Korea".
        const items = groups
          .get(region)
          .sort((a, b) => displayName(a).localeCompare(displayName(b)));
        const group = h('div', { class: 'loc-group' }, [
          h('div', { class: 'loc-group-title', text: t(region) }),
          h('div', { class: 'loc-grid' }, items.map((loc) => row(loc, selectedCc, favorites, best))),
        ]);
        frag.append(group);
      }
    }

    flip(list, () => list.replaceChildren(frag));
    stagger(list.querySelectorAll('.loc-row'), 18, 300);
    // English counts one country in the singular. Persian does not, so its
    // two forms are the same string.
    countBadge.textContent = t(
      countries.length === 1 ? 'loc.countOne' : 'loc.count',
      { n: countries.length },
    );
    segmentedSet(filterSeg, filter);
  }

  // Roving arrow-key navigation across the responsive grid.
  list.addEventListener('keydown', (e) => {
    if (!['ArrowRight', 'ArrowLeft', 'ArrowDown', 'ArrowUp'].includes(e.key)) return;
    const rows = Array.from(list.querySelectorAll('.loc-row'));
    const i = rows.indexOf(document.activeElement);
    if (i < 0) return;
    e.preventDefault();
    const rect = rows[i].getBoundingClientRect();
    let best = -1;
    let bestScore = Infinity;
    const dirX = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    const dirY = e.key === 'ArrowDown' ? 1 : e.key === 'ArrowUp' ? -1 : 0;
    rows.forEach((el, j) => {
      if (j === i) return;
      const r = el.getBoundingClientRect();
      const dx = r.left - rect.left;
      const dy = r.top - rect.top;
      if (dirX && Math.sign(dx) !== dirX) return;
      if (dirY && Math.sign(dy) !== dirY) return;
      if (dirY && Math.abs(dx) > r.width) return;
      const score = Math.abs(dx) * (dirY ? 3 : 1) + Math.abs(dy) * (dirX ? 3 : 1);
      if (score < bestScore) { bestScore = score; best = j; }
    });
    if (best >= 0) rows[best].focus();
  });

  function preferred(settings, locations) {
    if (!settings || !locations) return null;
    const cc = String(settings.countryCode || '').toUpperCase();
    if (!cc) return null;
    const matches = locations.filter((l) => String(l.countryCode).toUpperCase() === cc);
    if (!matches.length) return null;
    const city = String(settings.cityCode || '').toLowerCase();
    return matches.find((l) => String(l.cityCode || '').toLowerCase() === city) || matches[0];
  }

  function update(state) {
    if (!state) return;
    if (Array.isArray(state.locations) && state.locations.length) {
      lastLocations = state.locations;
      if (mode === 'loading' || mode === 'empty' || mode === 'error') mode = 'ready';
    } else if (mode === 'loading') {
      // The boot fetch can fail before this view is ever built (the app
      // starts on Home). Without this the skeleton would stay up forever;
      // locationsError is the only record that the fetch already failed.
      if (app.locationsError && app.locationsError()) mode = 'error';
    } else if (mode !== 'error') {
      lastLocations = state.locations || [];
      // A failed fetch is an error, not an empty list. The empty state
      // says "sign in first", which is the wrong story when the problem
      // was the network.
      mode = lastLocations.length ? 'ready' : (app.locationsError && app.locationsError() ? 'error' : 'empty');
    }
    // Avoid rebuilding the grid when nothing it shows has changed.
    const sig = [
      mode,
      lastLocations ? lastLocations.length : 0,
      query,
      filter,
      (state.settings && (state.settings.favorites || []).join(',')) || '',
      (state.settings && (state.settings.recent || []).join(',')) || '',
      state.status.location ? state.status.location.countryCode + state.status.location.cityCode : '',
      state.settings ? state.settings.countryCode + state.settings.cityCode : '',
    ].join('|');
    if (sig === list.dataset.sig) return;
    list.dataset.sig = sig;
    render();
  }

  function setLoading() { mode = 'loading'; list.dataset.sig = ''; render(); }
  // setError(false) is called after every successful refresh. This used to
  // ignore the argument and switch to the error mode anyway, so a good reload
  // flashed "could not load locations" until the next poll restored the list.
  function setError(on = true) {
    if (on) {
      // A failed manual refresh keeps the rows already on screen; the toast
      // reports it. The full-page error is only for when there is no list.
      if (lastLocations && lastLocations.length) return;
      mode = 'error';
    } else if (mode === 'error' || mode === 'loading') {
      // Success can also arrive before the view saw the new list (an empty
      // result emits no usable update), so settle the mode explicitly.
      const list = (app.state() && app.state().locations) || [];
      lastLocations = list;
      mode = list.length ? 'ready' : (app.locationsError && app.locationsError() ? 'error' : 'empty');
    } else {
      return;
    }
    list.dataset.sig = '';
    render();
  }
  function setRefreshBusy(busy) {
    refreshBtn.disabled = busy;
    refreshBtn.classList.toggle('is-spinning', busy);
    refreshBtn.querySelector('svg').style.animation = busy ? 'spin .7s linear infinite' : '';
  }

  return { root, update, setLoading, setError, setRefreshBusy, focusSearch: () => { search.focus(); search.select(); } };
}
