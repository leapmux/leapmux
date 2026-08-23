import { createHandler, StartServer } from '@solidjs/start/server'
import { bootSplashDark, bootSplashLight } from '~/lib/bootSplashTheme'
import { frontendBuildInfo } from '~/lib/buildEnv'

/**
 * Blocking head script: paint `data-theme` from the device tier before first
 * paint, matching `themeStore.resolvedMode`. Reads the same
 * `leapmux:browser-prefs` TTL envelope `~/lib/browserStorage` writes. Keep the
 * key name and `{ v, e }` shape in sync with that module — this script cannot
 * import it.
 */
function bootThemeScript(): string {
  const lightBg = bootSplashLight.background
  const darkBg = bootSplashDark.background
  return `(function(){try{var mode="system";var raw=localStorage.getItem("leapmux:browser-prefs");if(raw){var wrap=JSON.parse(raw);if(wrap&&typeof wrap.e==="number"&&wrap.e>Date.now()&&wrap.v&&wrap.v.theme&&typeof wrap.v.theme.mode==="string")mode=wrap.v.theme.mode;}var dark=mode==="dark"||(mode!=="light"&&window.matchMedia("(prefers-color-scheme: dark)").matches);var root=document.documentElement;root.setAttribute("data-theme",dark?"dark":"light");root.style.colorScheme=dark?"dark":"light";var meta=document.querySelector('meta[name="theme-color"]:not([media])')||document.querySelector('meta[name="theme-color"]');if(meta)meta.setAttribute("content",dark?"${darkBg}":"${lightBg}");}catch(e){}})();`
}

export default createHandler(() => (
  <StartServer
    document={({ assets, children, scripts }) => (
      <html
        lang="en"
        data-version={frontendBuildInfo.version || undefined}
        data-commit-hash={frontendBuildInfo.commitHash || undefined}
        data-commit-time={frontendBuildInfo.commitTime || undefined}
        data-build-time={frontendBuildInfo.buildTime || undefined}
        data-branch={frontendBuildInfo.branch || undefined}
      >
        <head>
          <meta charset="utf-8" />
          {/*
            `interactive-widget=resizes-content` is what makes the software
            keyboard shrink the LAYOUT viewport on Chromium (Android, and a
            touch-first Windows device), so `100dvh` reports the region above
            the keyboard and the composer stays visible with no JS. Without
            it Chromium defaults to `resizes-visual`, which leaves the layout
            viewport at full height and hides the composer behind the
            keyboard.             WebKit does not implement the key yet and ignores it.
            iOS does not shrink `dvh` for the keyboard either -- WebKit
            moves part of the viewport out of sight instead of resizing
            it -- and `~/hooks/useVisualViewportInset` publishes
            `--vvh` from `visualViewport.height` so the body covers the
            visible region.
          */}
          <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover, interactive-widget=resizes-content" />
          <link rel="icon" href="/icons/leapmux-icon.ico" sizes="48x48" />
          <link rel="icon" href="/icons/leapmux-icon.svg" type="image/svg+xml" />
          <link rel="manifest" href="/manifest.webmanifest" />
          {/*
            Dual theme-color metas so the browser chrome matches OS polarity
            before any JS runs. The blocking script below rewrites the
            non-media tag when the device tier pins light or dark.
          */}
          <meta name="theme-color" media="(prefers-color-scheme: light)" content={bootSplashLight.background} />
          <meta name="theme-color" media="(prefers-color-scheme: dark)" content={bootSplashDark.background} />
          <meta name="theme-color" content={bootSplashLight.background} />
          <link rel="apple-touch-icon" href="/icons/leapmux-icon-square-apple-touch.png" />
          <meta name="apple-mobile-web-app-capable" content="yes" />
          <meta name="apple-mobile-web-app-status-bar-style" content="black-translucent" />
          {/*
            Zero-JS splash polarity. Falls back to Default light/dark, follows
            prefers-color-scheme, then yields to html[data-theme] once the
            blocking script (or themeStore) has spoken. Keep in lockstep with
            ~/components/common/BootSplash.css.ts.
          */}
          <style>
            {`
#boot-splash,[data-testid="boot-splash"]{
  min-height:100dvh;display:flex;align-items:center;justify-content:center;
  flex-direction:column;gap:1rem;font-family:system-ui,sans-serif;
  background:${bootSplashLight.background};
  color:${bootSplashLight.foreground};
}
@media (prefers-color-scheme: dark){
  #boot-splash,[data-testid="boot-splash"]{
    background:${bootSplashDark.background};
    color:${bootSplashDark.foreground};
  }
}
html[data-theme="light"] #boot-splash,html[data-theme="light"] [data-testid="boot-splash"]{
  background:${bootSplashLight.background};
  color:${bootSplashLight.foreground};
}
html[data-theme="dark"] #boot-splash,html[data-theme="dark"] [data-testid="boot-splash"]{
  background:${bootSplashDark.background};
  color:${bootSplashDark.foreground};
}
#boot-splash p,[data-testid="boot-splash"] p{margin:0;font-size:.95rem}
`}
          </style>
          {/*
            Do NOT preload Hack NF faces here. Each face is ~1.1 MB; the LTE
            cold-start tracer ranked even a single Regular preload as ~50% of
            bytes before shell_visible. The @font-face rules in
            ~/styles/global.css.ts still fetch a face when a code surface first
            needs it — after the shell is up.
          */}
          {/*
            Runs before first paint so a device-tier dark pin is not a light
            flash. Cannot import browserStorage; see bootThemeScript().
          */}
          <script>{bootThemeScript()}</script>
          {assets}
        </head>
        <body>
          {/*
            Static boot splash (no SSR): Go serves this HTML as-is. Solid's
            client mount replaces `#app` contents. Keep in lockstep with
            `~/components/common/BootSplash`.
          */}
          <div id="app">
            <div
              id="boot-splash"
              data-testid="boot-splash"
              role="status"
              aria-live="polite"
            >
              <img src="/icons/leapmux-icon.svg" width="64" height="64" alt="" />
              <p>Loading LeapMux…</p>
            </div>
            {children}
          </div>
          {scripts}
        </body>
      </html>
    )}
  />
))
