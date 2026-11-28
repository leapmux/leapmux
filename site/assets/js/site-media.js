// Unified theme-aware, viewport-aware, reduced-motion-aware video & hero-showcase controller.
(function () {
  if (window.__lmSiteMediaWired) return;
  window.__lmSiteMediaWired = true;

  const mq = window.matchMedia ? window.matchMedia('(prefers-reduced-motion: reduce)') : null;
  const pauseTimers = new WeakMap();
  const inViewport = new WeakMap();
  const FADE_FALLBACK_MS = 380;

  function isDarkTheme() {
    return document.documentElement.classList.contains('dark');
  }

  function isReducedMotion() {
    return Boolean(mq && mq.matches);
  }

  function hydrateVideo(video) {
    if (!video.getAttribute('poster') && video.dataset.posterSrc) {
      video.setAttribute('poster', video.dataset.posterSrc);
    }
    if (!video.dataset.srcLoaded && video.dataset.videoSrc) {
      video.dataset.srcLoaded = 'true';
      const source = document.createElement('source');
      source.src = video.dataset.videoSrc;
      source.type = 'video/mp4';
      video.appendChild(source);
      video.load();
    }
  }

  function clearPauseTimer(video) {
    const timer = pauseTimers.get(video);
    if (timer) {
      clearTimeout(timer);
      pauseTimers.delete(video);
    }
  }

  function syncVideo(video, visible, restart) {
    const matchesTheme = (video.dataset.themeVariant === 'dark') === isDarkTheme();
    const reduced = isReducedMotion();
    clearPauseTimer(video);

    // Expose native controls under reduced motion so users can manually play clips.
    video.controls = reduced;

    if (visible && matchesTheme) {
      hydrateVideo(video);
      if (!reduced) {
        if (restart && video.readyState >= 1) {
          try {
            video.currentTime = 0;
          } catch {
            // Ignore seek errors before metadata is ready.
          }
        }
        video.play().catch(() => {});
      } else {
        video.pause();
      }
      return;
    }

    if (!video.paused) {
      const panel = video.closest('.lm-hero-panel');
      if (panel && !reduced && matchesTheme) {
        // Keep outgoing hero tab video moving during its CSS opacity crossfade.
        const finishPause = () => {
          clearPauseTimer(video);
          panel.removeEventListener('transitionend', onEnd);
          if (!panel.classList.contains('is-active') || (video.dataset.themeVariant === 'dark') !== isDarkTheme()) {
            video.pause();
          }
        };
        const onEnd = (event) => {
          if (event.target === panel && event.propertyName === 'opacity') finishPause();
        };
        panel.addEventListener('transitionend', onEnd);
        pauseTimers.set(video, setTimeout(finishPause, FADE_FALLBACK_MS));
      } else {
        video.pause();
      }
    }
  }

  function initAll() {
    const showcases = Array.from(document.querySelectorAll('.lm-hero-showcase'));
    const standaloneClips = Array.from(
      document.querySelectorAll('video.lm-managed-video[data-video-role="clip"]')
    );

    const syncShowcase = (root, restartActive) => {
      const rootVisible = inViewport.get(root) !== false;
      root.querySelectorAll('.lm-hero-panel').forEach((panel) => {
        const panelActive = panel.classList.contains('is-active');
        panel.querySelectorAll('video.lm-managed-video').forEach((video) => {
          syncVideo(video, rootVisible && panelActive, restartActive && panelActive);
        });
      });
    };

    const syncAll = () => {
      showcases.forEach((root) => syncShowcase(root, false));
      standaloneClips.forEach((video) => {
        const figure = video.closest('figure') || video;
        syncVideo(video, inViewport.get(figure) === true, false);
      });
    };

    showcases.forEach((root) => {
      if (root.dataset.wired === 'true') return;
      root.dataset.wired = 'true';
      inViewport.set(root, true);

      const tabs = Array.from(root.querySelectorAll('[role="tab"]'));
      const panels = Array.from(root.querySelectorAll('[role="tabpanel"]'));

      const activate = (nextTab, focusTab) => {
        const targetId = nextTab.getAttribute('aria-controls');
        tabs.forEach((tab) => {
          const active = tab === nextTab;
          tab.setAttribute('aria-selected', active ? 'true' : 'false');
          tab.tabIndex = active ? 0 : -1;
        });
        panels.forEach((panel) => {
          const active = panel.id === targetId;
          panel.classList.toggle('is-active', active);
          if (active) {
            panel.removeAttribute('aria-hidden');
            panel.removeAttribute('inert');
          } else {
            panel.setAttribute('aria-hidden', 'true');
            panel.setAttribute('inert', '');
          }
        });
        if (focusTab) nextTab.focus();
        syncShowcase(root, true);
      };

      tabs.forEach((tab, index) => {
        tab.addEventListener('click', () => activate(tab, false));
        tab.addEventListener('keydown', (event) => {
          let nextIndex = null;
          if (event.key === 'ArrowRight') nextIndex = (index + 1) % tabs.length;
          else if (event.key === 'ArrowLeft') nextIndex = (index - 1 + tabs.length) % tabs.length;
          else if (event.key === 'Home') nextIndex = 0;
          else if (event.key === 'End') nextIndex = tabs.length - 1;
          if (nextIndex === null) return;
          event.preventDefault();
          activate(tabs[nextIndex], true);
        });
      });
    });

    if (typeof IntersectionObserver !== 'undefined') {
      const observer = new IntersectionObserver(
        (entries) => {
          entries.forEach((entry) => {
            inViewport.set(entry.target, entry.isIntersecting);
            if (entry.target.classList.contains('lm-hero-showcase')) {
              syncShowcase(entry.target, false);
            } else {
              entry.target.querySelectorAll('video.lm-managed-video[data-video-role="clip"]').forEach((video) => {
                syncVideo(video, entry.isIntersecting, false);
              });
            }
          });
        },
        { rootMargin: '150px 0px', threshold: 0.05 }
      );
      showcases.forEach((root) => observer.observe(root));
      const clipFigures = new Set(standaloneClips.map((v) => v.closest('figure') || v));
      clipFigures.forEach((fig) => observer.observe(fig));
    } else {
      standaloneClips.forEach((v) => inViewport.set(v.closest('figure') || v, true));
    }

    if (mq) {
      if (mq.addEventListener) mq.addEventListener('change', syncAll);
      else if (mq.addListener) mq.addListener(syncAll);
    }

    if (typeof MutationObserver !== 'undefined') {
      let lastIsDark = isDarkTheme();
      new MutationObserver(() => {
        const nextIsDark = isDarkTheme();
        if (nextIsDark === lastIsDark) return;
        lastIsDark = nextIsDark;
        syncAll();
      }).observe(document.documentElement, {
        attributes: true,
        attributeFilter: ['class'],
      });
    }

    syncAll();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initAll, { once: true });
  } else {
    initAll();
  }
})();
