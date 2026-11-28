// Progressive enhancement for agent-ticker: shuffles marquee order identically
// across both tracks, pauses off-screen animation via IntersectionObserver,
// and syncs scroll-edge fade indicators only under prefers-reduced-motion.
(function () {
  if (window.__lmAgentTickerWired) return;
  window.__lmAgentTickerWired = true;

  function initTicker(ticker) {
    if (ticker.dataset.wired === 'true') return;
    ticker.dataset.wired = 'true';

    const lists = ticker.querySelectorAll('.agent-ticker-list');
    if (lists.length === 2) {
      const a = Array.from(lists[0].children);
      const b = Array.from(lists[1].children);
      const n = a.length;
      if (n >= 2 && b.length === n) {
        const order = new Array(n);
        for (let i = 0; i < n; i++) order[i] = i;
        for (let j = n - 1; j > 0; j--) {
          const k = Math.floor(Math.random() * (j + 1));
          const tmp = order[j];
          order[j] = order[k];
          order[k] = tmp;
        }
        const f0 = document.createDocumentFragment();
        const f1 = document.createDocumentFragment();
        for (let m = 0; m < n; m++) {
          f0.appendChild(a[order[m]]);
          f1.appendChild(b[order[m]]);
        }
        lists[0].appendChild(f0);
        lists[1].appendChild(f1);
      }
    }

    if (typeof IntersectionObserver !== 'undefined') {
      const observer = new IntersectionObserver(
        (entries) => {
          entries.forEach((entry) => {
            ticker.dataset.offscreen = entry.isIntersecting ? 'false' : 'true';
          });
        },
        { threshold: 0.01 }
      );
      observer.observe(ticker);
    }

    const vp = ticker.querySelector('.agent-ticker-viewport');
    if (!vp) return;

    const mq = window.matchMedia ? window.matchMedia('(prefers-reduced-motion: reduce)') : null;
    let rafId = 0;
    let listening = false;

    const syncEdges = () => {
      rafId = 0;
      const max = vp.scrollWidth - vp.clientWidth;
      ticker.dataset.scrolledLeft = vp.scrollLeft > 1 ? 'true' : 'false';
      ticker.dataset.scrolledEnd = max <= 1 || vp.scrollLeft >= max - 1 ? 'true' : 'false';
    };

    const scheduleSync = () => {
      if (!rafId) rafId = requestAnimationFrame(syncEdges);
    };

    const updateReducedMode = () => {
      const reduced = Boolean(mq && mq.matches);
      if (reduced && !listening) {
        listening = true;
        vp.addEventListener('scroll', scheduleSync, { passive: true });
        window.addEventListener('resize', scheduleSync, { passive: true });
        scheduleSync();
      } else if (!reduced && listening) {
        listening = false;
        vp.removeEventListener('scroll', scheduleSync);
        window.removeEventListener('resize', scheduleSync);
        if (rafId) {
          cancelAnimationFrame(rafId);
          rafId = 0;
        }
      }
    };

    if (mq) {
      if (mq.addEventListener) mq.addEventListener('change', updateReducedMode);
      else if (mq.addListener) mq.addListener(updateReducedMode);
    }
    updateReducedMode();
  }

  function initAll() {
    document.querySelectorAll('.agent-ticker').forEach(initTicker);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initAll, { once: true });
  } else {
    initAll();
  }
})();
