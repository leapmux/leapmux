{{- $providers := partialCached "agent-providers.html" . -}}
{{- $providerCount := len $providers -}}
{{- $sectionLabels := partial "section-label.html" (dict "mode" "map") -}}
{{- $popularSpecs := slice
  (dict "path" "/docs/getting-started/quick-start" "title" "Quick Start" "subtitle" "Install LeapMux and run your first parallel agent session")
  (dict "path" "/docs/reference/supported-agents" "title" "Supported Agents & Feature Matrix" "subtitle" (printf "Compare all %d coding agent providers across capabilities" $providerCount))
  (dict "path" "/docs/using/worktrees-and-branches" "title" "Git Worktrees & Branches" "subtitle" "Isolate parallel agents in linked worktrees with safe cleanup")
  (dict "path" "/docs/recipes/remote-devbox" "title" "Connect a Remote Devbox Behind NAT" "subtitle" "Outbound WebSockets with zero inbound ports open")
  (dict "path" "/docs/using/cli-guide" "title" "CLI Guide & Automation" "subtitle" "Drive agents with --wait, --output-schema, and events checkpoints")
  (dict "path" "/docs/admin/security-and-auth" "title" "Security & Authentication" "subtitle" "Hybrid post-quantum end-to-end encryption (E2EE), passkeys, OAuth, and step-up")
  (dict "path" "/docs/admin/reverse-proxy" "title" "Reverse Proxy Configurations" "subtitle" "Production nginx, Caddy, Traefik, HAProxy, and Apache configs")
-}}
{{- $popularItems := slice -}}
{{- range $popularSpecs -}}
  {{- $p := site.GetPage .path -}}
  {{- if not $p -}}
    {{- errorf "pagefind-search.js: popular search target %q not found" .path -}}
  {{- end -}}
  {{- $popularItems = $popularItems | append (dict "title" .title "subtitle" .subtitle "route" $p.RelPermalink) -}}
{{- end -}}
// Pagefind search adapter for Hextra's native search command palette.
// Implements `window.hextraSearch` so Hextra's search-dialog.js owns keyboard
// navigation, ARIA, and animations while Pagefind serves chunked binary search
// instead of downloading a 1 MB monolithic JSON index.
(function () {
  const PAGEFIND_URL = '{{ "pagefind/pagefind.js" | relURL }}';
  const MAX_RESULTS = 10;
  const MAX_SUB_RESULTS = 3;
  const DEBOUNCE_MS = 120;

  const POPULAR_SEARCHES = {{ $popularItems | jsonify | safeJS }};
  const SECTION_LABELS = Object.assign(Object.create(null), {{ $sectionLabels | jsonify | safeJS }});

  const SYNONYMS = Object.assign(Object.create(null), {
    e2ee: 'end-to-end encrypted',
    nat: 'remote worker outbound websocket',
    lmx: 'leapmux cli',
    pty: 'terminal',
    tofu: 'worker public key changed',
    pq: 'post-quantum',
    fsl: 'license',
    apple: 'sign in with apple',
  });

  let pagefindPromise = null;
  let searchSeq = 0;
  let debounceTimer = null;
  let pendingResolve = null;
  const domParser = typeof DOMParser !== 'undefined' ? new DOMParser() : null;

  async function loadPagefind() {
    if (!pagefindPromise) {
      pagefindPromise = import(PAGEFIND_URL)
        .then(async (mod) => {
          if (typeof mod.init === 'function') {
            await mod.init();
          }
          return mod;
        })
        .catch((err) => {
          pagefindPromise = null;
          throw err;
        });
    }
    return pagefindPromise;
  }

  function stripHtml(html) {
    if (!html) return '';
    if (domParser) {
      const doc = domParser.parseFromString(html, 'text/html');
      return (doc.body && doc.body.textContent ? doc.body.textContent : '').replace(/\s+/g, ' ').trim();
    }
    return html.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim();
  }

  function breadcrumbsFromUrl(url) {
    const clean = url.replace(/^https?:\/\/[^/]+/, '').split('#')[0].replace(/^\/|\/$/g, '');
    const parts = clean.split('/').filter(Boolean);
    if (parts.length <= 1) return [];
    return parts
      .slice(0, -1)
      .map((seg) => (Object.hasOwn(SECTION_LABELS, seg) ? SECTION_LABELS[seg] : seg.replace(/-/g, ' ')));
  }

  function lookupSynonym(raw) {
    const key = raw.trim().toLowerCase();
    return Object.hasOwn(SYNONYMS, key) ? SYNONYMS[key] : null;
  }

  async function runMergedSearch(pf, query) {
    const syn = lookupSynonym(query);
    if (!syn) {
      const res = await pf.search(query);
      return res && Array.isArray(res.results) ? res.results.slice(0, MAX_RESULTS) : [];
    }
    // Pagefind joins whitespace terms with AND, so run the literal query and
    // its synonym query in parallel and merge unique hits (primary matches first).
    const [primary, expanded] = await Promise.all([pf.search(query), pf.search(syn)]);
    const merged = [];
    const seen = new Set();
    const pushList = (resp) => {
      const list = resp && Array.isArray(resp.results) ? resp.results : [];
      for (let i = 0; i < list.length && merged.length < MAX_RESULTS; i++) {
        const entry = list[i];
        if (!entry || seen.has(entry.id)) continue;
        seen.add(entry.id);
        merged.push(entry);
      }
    };
    pushList(primary);
    pushList(expanded);
    return merged;
  }

  function waitDebounce(seq) {
    if (debounceTimer) clearTimeout(debounceTimer);
    if (pendingResolve) {
      pendingResolve(false);
      pendingResolve = null;
    }
    return new Promise((resolve) => {
      pendingResolve = resolve;
      debounceTimer = setTimeout(() => {
        debounceTimer = null;
        pendingResolve = null;
        resolve(seq === searchSeq);
      }, DEBOUNCE_MS);
    });
  }

  window.hextraSearch = {
    preload: async function () {
      await loadPagefind();
    },
    search: async function (query) {
      const seq = ++searchSeq;
      const active = await waitDebounce(seq);
      if (!active) return [];
      const pf = await loadPagefind();
      if (seq !== searchSeq) return [];
      const slice = await runMergedSearch(pf, query);
      if (seq !== searchSeq) return [];
      const items = await Promise.all(slice.map((r) => r.data()));
      if (seq !== searchSeq) return [];
      return items.map((item, idx) => {
        const title = (item.meta && item.meta.title) || item.url;
        const route = item.url;
        const subResults = Array.isArray(item.sub_results) ? item.sub_results : [];
        const matches = [];
        for (let j = 0; j < subResults.length && matches.length < MAX_SUB_RESULTS; j++) {
          const sub = subResults[j];
          const excerpt = stripHtml(sub.excerpt);
          if (!excerpt && sub.title === title) continue;
          matches.push({
            id: `pf-sub-${idx}-${j}`,
            route: sub.url || route,
            title: sub.title || title,
            content: excerpt,
          });
        }
        if (matches.length === 0 && item.excerpt) {
          matches.push({
            id: `pf-excerpt-${idx}`,
            route,
            title,
            content: stripHtml(item.excerpt),
          });
        }
        return {
          id: `pf-res-${idx}`,
          route,
          title,
          breadcrumbs: breadcrumbsFromUrl(route),
          matches,
        };
      });
    },
  };

  // Show curated popular searches whenever the search modal opens with an empty query.
  document.addEventListener('DOMContentLoaded', () => {
    const dialog = document.getElementById('hextra-search-dialog');
    if (!dialog) return;
    const input = dialog.querySelector('.hextra-search-input');
    const resultsEl = dialog.querySelector('.hextra-search-results');
    const emptyEl = dialog.querySelector('[data-search-empty]');
    const viewportEl = dialog.querySelector('[data-search-viewport]');
    const innerEl = dialog.querySelector('[data-search-inner]');
    if (!input || !resultsEl) return;

    let popularTimer = null;
    let renderingPopular = false;

    function isDialogActive() {
      return dialog.open && dialog.dataset.state !== 'closing';
    }

    function hasPopularRendered() {
      return Boolean(resultsEl.querySelector('.lm-search-popular-heading'));
    }

    function cancelPopularTimer() {
      if (popularTimer) {
        clearTimeout(popularTimer);
        popularTimer = null;
      }
    }

    function renderPopular() {
      popularTimer = null;
      if (!isDialogActive() || input.value.trim() !== '') return;
      if (hasPopularRendered()) return;
      renderingPopular = true;
      try {
        while (resultsEl.firstChild) resultsEl.removeChild(resultsEl.firstChild);
        if (emptyEl) emptyEl.hidden = true;

        const headingLi = document.createElement('li');
        headingLi.setAttribute('role', 'presentation');
        headingLi.className = 'lm-search-popular-heading';
        headingLi.textContent = 'Popular searches';
        resultsEl.appendChild(headingLi);

        POPULAR_SEARCHES.forEach((item, idx) => {
          const li = document.createElement('li');
          li.setAttribute('role', 'presentation');
          const link = document.createElement('a');
          link.id = `lm-popular-${idx}`;
          link.href = item.route;
          link.setAttribute('role', 'option');
          link.setAttribute('aria-selected', idx === 0 ? 'true' : 'false');

          const titleEl = document.createElement('div');
          titleEl.className = 'hextra-search-title';
          titleEl.textContent = item.title;
          link.appendChild(titleEl);

          const subEl = document.createElement('div');
          subEl.className = 'hextra-search-excerpt';
          subEl.textContent = item.subtitle;
          link.appendChild(subEl);

          li.appendChild(link);
          resultsEl.appendChild(li);
        });
        if (POPULAR_SEARCHES.length > 0) {
          input.setAttribute('aria-activedescendant', 'lm-popular-0');
        }
        if (innerEl) innerEl.scrollTop = 0;
        if (viewportEl) viewportEl.dataset.expanded = 'true';
      } finally {
        renderingPopular = false;
      }
    }

    function schedulePopular(delayMs) {
      cancelPopularTimer();
      if (!isDialogActive() || input.value.trim() !== '') return;
      popularTimer = setTimeout(renderPopular, delayMs);
    }

    const dialogObserver = new MutationObserver(() => {
      if (isDialogActive() && input.value.trim() === '') {
        schedulePopular(40);
      } else {
        cancelPopularTimer();
      }
    });
    dialogObserver.observe(dialog, { attributes: true, attributeFilter: ['open', 'data-state'] });

    // Re-populate deterministically if Hextra's private 250 ms collapseTimer
    // empties resultsEl while the dialog is still open with an empty input.
    const resultsObserver = new MutationObserver(() => {
      if (renderingPopular) return;
      if (resultsEl.children.length === 0 && isDialogActive() && input.value.trim() === '') {
        renderPopular();
      }
    });
    resultsObserver.observe(resultsEl, { childList: true });

    input.addEventListener('focus', () => {
      if (isDialogActive() && input.value.trim() === '' && !hasPopularRendered()) {
        schedulePopular(40);
      }
    });

    input.addEventListener('input', () => {
      if (input.value.trim() === '') {
        schedulePopular(40);
      } else {
        cancelPopularTimer();
      }
    });
  });
})();
