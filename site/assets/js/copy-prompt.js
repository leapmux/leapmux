// Delegated click handler for copy-prompt shortcodes with iOS Safari fallback.
(function () {
  if (window.__lmCopyPromptWired) return;
  window.__lmCopyPromptWired = true;

  const timers = new WeakMap();

  function markCopied(btn) {
    const orig = btn.dataset.origLabel || btn.textContent;
    btn.dataset.origLabel = orig;
    btn.textContent = btn.dataset.copiedLabel || 'Copied';
    btn.classList.add('is-copied');
    const prev = timers.get(btn);
    if (prev) clearTimeout(prev);
    timers.set(
      btn,
      setTimeout(() => {
        btn.textContent = orig;
        btn.classList.remove('is-copied');
        timers.delete(btn);
      }, 1800)
    );
  }

  function fallbackCopy(text, btn) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.top = '0';
    ta.style.left = '0';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.focus({ preventScroll: true });
    ta.select();
    ta.setSelectionRange(0, text.length);
    try {
      if (document.execCommand('copy')) markCopied(btn);
    } catch {
      // Ignore copy errors when clipboard permissions are denied.
    }
    document.body.removeChild(ta);
  }

  document.addEventListener('click', (event) => {
    const btn = event.target && event.target.closest ? event.target.closest('.copy-prompt-btn') : null;
    if (!btn) return;
    const card = btn.closest('.copy-prompt');
    const codeEl = card && card.querySelector('.copy-prompt-body code');
    if (!codeEl) return;
    const text = codeEl.textContent || '';
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(
        () => markCopied(btn),
        () => fallbackCopy(text, btn)
      );
    } else {
      fallbackCopy(text, btn);
    }
  });
})();
