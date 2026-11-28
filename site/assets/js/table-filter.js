// Progressive-enhancement table filter and section-pill controller.
(function () {
  if (window.__lmTableFilterLoaded) return;
  window.__lmTableFilterLoaded = true;

  const URL_DEBOUNCE_MS = 250;

  function headingTitle(el) {
    const clone = el.cloneNode(true);
    clone.querySelectorAll('.subheading-anchor').forEach((a) => a.remove());
    return (clone.textContent || '').replace(/\s+/g, ' ').trim();
  }

  function extractRows(table) {
    const bodyRows = Array.from(table.querySelectorAll('tbody > tr'));
    if (bodyRows.length > 0) return bodyRows;
    return Array.from(table.querySelectorAll('tr')).filter(
      (tr) => !tr.closest('thead') && tr.querySelector('td') !== null
    );
  }

  // Only hide a wrapper around a table when the wrapper contains no sibling
  // prose or other elements beside that table.
  function isTableOnlyWrapper(wrapper, table) {
    if (!wrapper || wrapper === table) return false;
    if (wrapper.children.length !== 1 || !wrapper.contains(table)) return false;
    for (let i = 0; i < wrapper.childNodes.length; i++) {
      const node = wrapper.childNodes[i];
      if (node.nodeType === Node.TEXT_NODE && node.textContent.trim() !== '') {
        return false;
      }
    }
    return true;
  }

  function setHidden(el, hidden) {
    if (el.hidden !== hidden) el.hidden = hidden;
  }

  function initWidget(root, widgetIndex) {
    if (root.dataset.tableFilterReady === '1') return;
    root.dataset.tableFilterReady = '1';

    const scopeSelector = root.getAttribute('data-table-filter-scope') || '';
    let container = null;
    if (scopeSelector) {
      try {
        container = document.querySelector(scopeSelector);
      } catch {
        root.hidden = true;
        return;
      }
    } else {
      container = root.closest('.content') || root.closest('main') || document.body;
    }
    if (!container) {
      root.hidden = true;
      return;
    }

    const children = Array.from(container.children);
    let startIdx = 0;
    if (!scopeSelector) {
      for (let i = 0; i < children.length; i++) {
        if (children[i] === root || children[i].contains(root)) {
          startIdx = i + 1;
          break;
        }
      }
    }

    const topSections = [];
    let currentH2 = null;
    let currentH3 = null;
    const allTables = [];
    const tableSet = new Set();
    let secCounter = 0;

    function createSection(title, headingEl) {
      return {
        id: `sec-${++secCounter}`,
        title,
        nodes: [headingEl],
        tables: [],
        children: [],
      };
    }

    function registerTable(tableEl, wrapperEl, h2Sec, h3Sec) {
      if (tableSet.has(tableEl)) return;
      const trs = extractRows(tableEl);
      if (trs.length === 0) return;
      tableSet.add(tableEl);
      const catSec = h2Sec || h3Sec || null;
      const sectionContext = `${h2Sec ? h2Sec.title : ''} ${h3Sec ? h3Sec.title : ''}`.trim();
      const rows = trs.map((tr) => ({
        el: tr,
        text: `${sectionContext} ${tr.textContent || ''}`.replace(/\s+/g, ' ').toLowerCase(),
      }));
      const entry = {
        el: tableEl,
        wrapperEl: isTableOnlyWrapper(wrapperEl, tableEl) ? wrapperEl : null,
        rows,
        categoryId: catSec ? catSec.id : 'uncategorized',
        hasMatch: true,
      };
      allTables.push(entry);
      if (h3Sec) h3Sec.tables.push(entry);
      else if (h2Sec) h2Sec.tables.push(entry);
    }

    for (let idx = startIdx; idx < children.length; idx++) {
      const child = children[idx];
      // Stop before a sibling table-filter widget when unscoped so widgets do not claim each other's tables.
      if (!scopeSelector && child.classList && child.classList.contains('lm-table-filter')) break;
      const tag = child.tagName;
      if (tag === 'H2') {
        currentH2 = createSection(headingTitle(child), child);
        currentH3 = null;
        topSections.push(currentH2);
        continue;
      }
      if (tag === 'H3') {
        currentH3 = createSection(headingTitle(child), child);
        if (currentH2) currentH2.children.push(currentH3);
        else topSections.push(currentH3);
        continue;
      }
      if (tag === 'TABLE') {
        registerTable(child, null, currentH2, currentH3);
      } else {
        const nested = child.querySelectorAll ? Array.from(child.querySelectorAll('table')) : [];
        if (nested.length > 0) {
          const singleWrap = nested.length === 1 ? child : null;
          nested.forEach((tbl) => registerTable(tbl, singleWrap, currentH2, currentH3));
        } else if (currentH3) {
          currentH3.nodes.push(child);
        } else if (currentH2) {
          currentH2.nodes.push(child);
        }
      }
    }

    function sectionHasTables(sec) {
      return sec.tables.length > 0 || sec.children.some(sectionHasTables);
    }

    const filterableSections = topSections.filter(sectionHasTables);
    const totalRows = allTables.reduce((sum, tbl) => sum + tbl.rows.length, 0);
    if (totalRows === 0) {
      root.hidden = true;
      return;
    }

    const input = root.querySelector('.lm-table-filter-input');
    const clearBtn = root.querySelector('.lm-table-filter-clear');
    const countEl = root.querySelector('.lm-table-filter-count');
    const pillsEl = root.querySelector('.lm-table-filter-pills');
    const emptyEl = root.querySelector('.lm-table-filter-empty');
    if (!input || !clearBtn || !countEl || !pillsEl || !emptyEl) return;

    const queryParam =
      root.getAttribute('data-table-filter-param') || (widgetIndex === 0 ? 'q' : `q${widgetIndex + 1}`);
    let activeCategory = 'all';
    const pillButtons = [];
    let urlTimer = null;

    function syncPills() {
      pillButtons.forEach((btn) => {
        btn.setAttribute('aria-pressed', btn.dataset.category === activeCategory ? 'true' : 'false');
      });
    }

    function addPill(id, label) {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'lm-table-filter-pill';
      btn.setAttribute('aria-pressed', id === 'all' ? 'true' : 'false');
      btn.dataset.category = id;
      btn.textContent = label;
      btn.addEventListener('click', () => {
        activeCategory = id !== 'all' && activeCategory === id ? 'all' : id;
        syncPills();
        applyFilter(false);
      });
      pillsEl.appendChild(btn);
      pillButtons.push(btn);
    }

    if (filterableSections.length > 1) {
      addPill('all', 'All');
      filterableSections.forEach((sec) => addPill(sec.id, sec.title));
    } else {
      pillsEl.hidden = true;
    }

    function writeUrlQuery(rawQuery) {
      try {
        const url = new URL(window.location.href);
        if (rawQuery) url.searchParams.set(queryParam, rawQuery);
        else url.searchParams.delete(queryParam);
        window.history.replaceState(window.history.state, '', url.pathname + url.search + url.hash);
      } catch {
        // Ignore SecurityError if history.replaceState is restricted.
      }
    }

    function scheduleUrlSync(rawQuery, immediate) {
      if (urlTimer) {
        clearTimeout(urlTimer);
        urlTimer = null;
      }
      if (immediate) {
        writeUrlQuery(rawQuery);
      } else {
        urlTimer = setTimeout(() => {
          urlTimer = null;
          writeUrlQuery(rawQuery);
        }, URL_DEBOUNCE_MS);
      }
    }

    function syncSectionVisibility(sec, hasTokens) {
      let anyChildVisible = false;
      sec.children.forEach((sub) => {
        if (sectionHasTables(sub)) {
          if (syncSectionVisibility(sub, hasTokens)) anyChildVisible = true;
        }
      });
      const visible = anyChildVisible || sec.tables.some((tb) => tb.hasMatch);
      sec.nodes.forEach((node) => setHidden(node, !visible));
      sec.children.forEach((sub) => {
        if (!sectionHasTables(sub)) {
          const showSub = visible && !hasTokens;
          sub.nodes.forEach((node) => setHidden(node, !showSub));
        }
      });
      return visible;
    }

    function applyFilter(updateUrl, immediateUrl) {
      const raw = input.value || '';
      const trimmed = raw.trim();
      const tokens = trimmed ? trimmed.toLowerCase().split(/\s+/) : [];
      let visibleRows = 0;

      allTables.forEach((tbl) => {
        const catOk = activeCategory === 'all' || tbl.categoryId === activeCategory;
        let tableVisible = 0;
        tbl.rows.forEach((row) => {
          let matched = catOk;
          if (matched && tokens.length > 0) {
            for (let k = 0; k < tokens.length; k++) {
              if (row.text.indexOf(tokens[k]) === -1) {
                matched = false;
                break;
              }
            }
          }
          setHidden(row.el, !matched);
          if (matched) tableVisible++;
        });
        tbl.hasMatch = tableVisible > 0;
        setHidden(tbl.el, !tbl.hasMatch);
        if (tbl.wrapperEl) setHidden(tbl.wrapperEl, !tbl.hasMatch);
        visibleRows += tableVisible;
      });

      const hasTokens = tokens.length > 0;
      filterableSections.forEach((sec) => syncSectionVisibility(sec, hasTokens));

      countEl.textContent = `Showing ${visibleRows} of ${totalRows} ${totalRows === 1 ? 'entry' : 'entries'}`;
      setHidden(clearBtn, !raw && activeCategory === 'all');
      setHidden(emptyEl, visibleRows > 0);
      if (updateUrl) scheduleUrlSync(trimmed, Boolean(immediateUrl));
    }

    input.addEventListener('input', () => applyFilter(true, false));

    input.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        if (input.value) {
          input.value = '';
          applyFilter(true, true);
        } else if (activeCategory !== 'all') {
          activeCategory = 'all';
          syncPills();
          applyFilter(false, false);
        }
        e.preventDefault();
      }
    });

    clearBtn.addEventListener('click', () => {
      input.value = '';
      activeCategory = 'all';
      syncPills();
      applyFilter(true, true);
      input.focus();
    });

    try {
      const initialQ = new URLSearchParams(window.location.search).get(queryParam);
      if (initialQ) input.value = initialQ;
    } catch {
      // Ignore malformed search strings.
    }

    applyFilter(false, false);
  }

  function initAll() {
    document.querySelectorAll('[data-table-filter]').forEach((root, idx) => {
      try {
        initWidget(root, idx);
      } catch {
        root.hidden = true;
      }
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initAll, { once: true });
  } else {
    initAll();
  }
})();
