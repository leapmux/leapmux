// Footer "Generated X ago" enhancement.
//
// The footer renders an absolute build date server-side (see
// layouts/_partials/footer.html). This script rewrites it into a relative
// "Generated X ago" string computed against the *visitor's* clock, and sets the
// element's tooltip to the visitor's full local time. It is progressive
// enhancement: if this script never runs, the absolute build date stays visible.
//
// The relative phrase and tooltip are formatted in the *site's* language (read
// from <html lang>), not the visitor's browser language, so the page reads in
// one consistent language. Falls back to the browser default if <html lang> is
// missing (an empty locale would make the Intl constructors throw).
(function () {
  "use strict";

  function siteLocale() {
    return document.documentElement.lang || undefined;
  }

  // Pick the largest sensible unit for the elapsed time and format it with the
  // platform's Intl.RelativeTimeFormat (e.g. "3 minutes ago", "yesterday").
  function relativePhrase(then, locale) {
    var seconds = Math.round((Date.now() - then.getTime()) / 1000);
    var abs = Math.abs(seconds);
    if (abs < 5) return "just now";

    var rtf = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
    // RelativeTimeFormat treats negative values as the past, so negate seconds
    // (which is positive when the build is in the past).
    if (abs < 45) return rtf.format(-seconds, "second");
    if (abs < 45 * 60) return rtf.format(-Math.round(seconds / 60), "minute");
    if (abs < 22 * 3600) return rtf.format(-Math.round(seconds / 3600), "hour");
    if (abs < 26 * 86400) return rtf.format(-Math.round(seconds / 86400), "day");
    if (abs < 320 * 86400) return rtf.format(-Math.round(seconds / 2629800), "month");
    return rtf.format(-Math.round(seconds / 31557600), "year");
  }

  function render(el) {
    var iso = el.getAttribute("data-generated");
    if (!iso) return;
    var then = new Date(iso);
    if (isNaN(then.getTime())) return;
    var locale = siteLocale();
    el.textContent = "Generated " + relativePhrase(then, locale);
    el.title = then.toLocaleString(locale);
  }

  function init() {
    var els = document.querySelectorAll(".lm-generated[data-generated]");
    for (var i = 0; i < els.length; i++) render(els[i]);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
