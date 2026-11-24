// Pagefind search adapter for Hextra's native search command palette.
// Implements `window.hextraSearch` so Hextra's search-dialog.js owns keyboard
// navigation, ARIA, and animations while Pagefind serves chunked binary search
// instead of downloading a 1 MB monolithic JSON index.
(function () {
  const PAGEFIND_URL = '{{ "pagefind/pagefind.js" | relURL }}';
  const MAX_RESULTS = 10;
  const MAX_SUB_RESULTS = 3;

  const POPULAR_SEARCHES = [
    {
      title: 'Quick Start',
      subtitle: 'Install LeapMux and run your first parallel agent session',
      route: '{{ "/docs/getting-started/quick-start/" | relURL }}',
      breadcrumbs: ['Getting Started'],
    },
    {
      title: 'Supported Agents & Feature Matrix',
      subtitle: 'Compare all 31 coding agent providers across capabilities',
      route: '{{ "/docs/reference/supported-agents/" | relURL }}',
      breadcrumbs: ['Reference'],
    },
    {
      title: 'Git Worktrees & Branches',
      subtitle: 'Isolate parallel agents in linked worktrees with safe cleanup',
      route: '{{ "/docs/using/worktrees-and-branches/" | relURL }}',
      breadcrumbs: ['Using LeapMux'],
    },
    {
      title: 'Connect a Remote Devbox Behind NAT',
      subtitle: 'Outbound WebSockets with zero inbound ports open',
      route: '{{ "/docs/recipes/remote-devbox/" | relURL }}',
      breadcrumbs: ['Recipes'],
    },
    {
      title: 'CLI Guide & Automation',
      subtitle: 'Drive agents with --wait, --output-schema, and events checkpoints',
      route: '{{ "/docs/using/cli-guide/" | relURL }}',
      breadcrumbs: ['Using LeapMux'],
    },
    {
      title: 'Security, E2EE & Authentication',
      subtitle: 'Hybrid post-quantum channels, passkeys, OAuth, and step-up',
      route: '{{ "/docs/admin/security-and-auth/" | relURL }}',
      breadcrumbs: ['Administration'],
    },
    {
      title: 'Reverse Proxy Configurations',
      subtitle: 'Production nginx, Caddy, Traefik, HAProxy, and Apache configs',
      route: '{{ "/docs/admin/reverse-proxy/" | relURL }}',
      breadcrumbs: ['Administration'],
    },
  ];

  const SYNONYMS = {
    e2ee: 'end-to-end encrypted',
    nat: 'remote worker outbound websocket',
    lmx: 'leapmux cli',
    pty: 'terminal',
    tofu: 'worker public key changed',
    pq: 'post-quantum',
    fsl: 'license',
    apple: 'sign in with apple',
  };

  let pagefindPromise = null;

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
    const tmp = document.createElement('div');
    tmp.innerHTML = html;
    return (tmp.textContent || '').replace(/\s+/g, ' ').trim();
  }

  function breadcrumbsFromUrl(url) {
    const clean = url.replace(/^https?:\/\/[^/]+/, '').split('#')[0].replace(/^\/|\/$/g, '');
    const parts = clean.split('/').filter(Boolean);
    if (parts.length <= 1) return [];
    const labels = {
      docs: 'Docs',
      'getting-started': 'Getting Started',
      recipes: 'Recipes',
      using: 'Using LeapMux',
      admin: 'Administration',
      reference: 'Reference',
      commands: 'Commands',
      troubleshooting: 'Troubleshooting',
      download: 'Downloads',
    };
    return parts.slice(0, -1).map((seg) => labels[seg] || seg.replace(/-/g, ' '));
  }

  function expandQuery(raw) {
    const key = raw.trim().toLowerCase();
    return SYNONYMS[key] ? `${raw} ${SYNONYMS[key]}` : raw;
  }

  window.hextraSearch = {
    preload: async function () {
      await loadPagefind();
    },
    search: async function (query) {
      const pf = await loadPagefind();
      const response = await pf.search(expandQuery(query));
      const slice = (response && response.results ? response.results : []).slice(0, MAX_RESULTS);
      const items = await Promise.all(slice.map((r) => r.data()));
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
    if (!input || !resultsEl) return;

    function renderPopular() {
      if (input.value.trim() !== '') return;
      while (resultsEl.firstChild) resultsEl.removeChild(resultsEl.firstChild);
      if (emptyEl) emptyEl.hidden = true;

      const headingLi = document.createElement('li');
      headingLi.setAttribute('role', 'presentation');
      headingLi.className = 'lm-search-popular-heading lm-popular-searches';
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
      if (viewportEl) viewportEl.dataset.expanded = 'true';
    }

    const observer = new MutationObserver(() => {
      if (dialog.open && input.value.trim() === '') {
        setTimeout(renderPopular, 40);
      }
    });
    observer.observe(dialog, { attributes: true, attributeFilter: ['open'] });

    input.addEventListener('focus', () => {
      if (dialog.open && input.value.trim() === '') {
        setTimeout(renderPopular, 40);
      }
    });

    input.addEventListener('input', () => {
      if (input.value.trim() === '') {
        setTimeout(renderPopular, 260);
      }
    });
  });
})();
