// Lazy-loading, reduced-motion-aware controller for cli-cast shortcodes.
(function () {
  if (window.__lmCliCastWired) return;
  window.__lmCliCastWired = true;

  let assetsPromise = null;

  function loadPlayerAssets(cssUrl, jsUrl) {
    if (window.AsciinemaPlayer) return Promise.resolve(window.AsciinemaPlayer);
    if (assetsPromise) return assetsPromise;
    assetsPromise = new Promise((resolve, reject) => {
      if (cssUrl && !document.querySelector(`link[href="${cssUrl}"]`)) {
        const link = document.createElement('link');
        link.rel = 'stylesheet';
        link.href = cssUrl;
        document.head.appendChild(link);
      }
      const script = document.createElement('script');
      script.src = jsUrl;
      script.async = true;
      script.onload = () => {
        if (window.AsciinemaPlayer) resolve(window.AsciinemaPlayer);
        else reject(new Error('AsciinemaPlayer global not found after script load'));
      };
      script.onerror = () => {
        assetsPromise = null;
        reject(new Error(`Failed to load ${jsUrl}`));
      };
      document.head.appendChild(script);
    });
    return assetsPromise;
  }

  function safeCall(promise) {
    if (promise && typeof promise.catch === 'function') {
      promise.catch(() => {});
    }
  }

  function mountPlayer(mount, mq) {
    if (mount.dataset.initialized === 'true') return;
    mount.dataset.initialized = 'true';

    const cssUrl = mount.dataset.playerCss || '';
    const jsUrl = mount.dataset.playerJs || '';
    const src = mount.dataset.castSrc;
    if (!src) return;

    loadPlayerAssets(cssUrl, jsUrl)
      .then((AsciinemaPlayer) => {
        const reduced = Boolean(mq && mq.matches);
        const opts = {
          theme: 'leapmux',
          fit: 'width',
          autoPlay: false,
          loop: true,
          controls: 'auto',
          terminalFontFamily:
            '"Hack NF", "Hack", "SF Mono", Consolas, ui-monospace, Menlo, Monaco, monospace',
        };
        if (mount.dataset.castCols) {
          const cols = Number.parseInt(mount.dataset.castCols, 10);
          if (Number.isFinite(cols) && cols > 0) opts.cols = cols;
        }
        if (mount.dataset.castRows) {
          const rows = Number.parseInt(mount.dataset.castRows, 10);
          if (Number.isFinite(rows) && rows > 0) opts.rows = rows;
        }
        if (reduced && mount.dataset.castPoster) {
          opts.poster = mount.dataset.castPoster;
        }

        const player = AsciinemaPlayer.create(src, mount, opts);
        let intersecting = true;
        let userPaused = false;

        // Track explicit user pause/play clicks so scrolling back into view
        // respects a manual pause.
        if (typeof player.addEventListener === 'function') {
          player.addEventListener('pause', () => {
            if (intersecting && !(mq && mq.matches)) userPaused = true;
          });
          player.addEventListener('play', () => {
            userPaused = false;
          });
        }

        const syncPlayback = () => {
          const nowReduced = Boolean(mq && mq.matches);
          if (!nowReduced && intersecting && !userPaused) {
            safeCall(player.play());
          } else {
            safeCall(player.pause());
          }
        };

        if (typeof IntersectionObserver !== 'undefined') {
          const playObserver = new IntersectionObserver(
            (entries) => {
              entries.forEach((entry) => {
                intersecting = entry.isIntersecting;
                syncPlayback();
              });
            },
            { threshold: 0.2 }
          );
          playObserver.observe(mount);
        } else {
          syncPlayback();
        }

        if (mq) {
          const onChange = () => syncPlayback();
          if (mq.addEventListener) mq.addEventListener('change', onChange);
          else if (mq.addListener) mq.addListener(onChange);
        }
      })
      .catch(() => {
        mount.dataset.initialized = 'false';
      });
  }

  function initAll() {
    const mounts = Array.from(document.querySelectorAll('.lm-cli-cast-mount'));
    if (!mounts.length) return;
    const mq = window.matchMedia ? window.matchMedia('(prefers-reduced-motion: reduce)') : null;

    if (typeof IntersectionObserver !== 'undefined') {
      const lazyObserver = new IntersectionObserver(
        (entries) => {
          entries.forEach((entry) => {
            if (!entry.isIntersecting) return;
            lazyObserver.unobserve(entry.target);
            mountPlayer(entry.target, mq);
          });
        },
        { rootMargin: '300px 0px', threshold: 0.01 }
      );
      mounts.forEach((mount) => lazyObserver.observe(mount));
    } else {
      mounts.forEach((mount) => mountPlayer(mount, mq));
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initAll, { once: true });
  } else {
    initAll();
  }
})();
