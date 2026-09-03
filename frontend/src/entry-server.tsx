import { createHandler, StartServer } from '@solidjs/start/server'
import { BootSplashIcon, BootSplashProgress } from '~/components/common/BootSplash'
import {
  BOOT_SPLASH_FAIL_TITLE,
  BOOT_SPLASH_LABEL,
  BOOT_SPLASH_RELOAD_LABEL,
  BOOT_SPLASH_STATIC_ID,
  BOOT_SPLASH_TEST_ID,
  bootFailureWatchdogScript,
  bootPhaseScript,
  bootSplashDark,
  bootSplashDocumentCss,
  bootSplashLight,
  bootThemeScript,
} from '~/lib/bootSplashTheme'
import { frontendBuildInfo } from '~/lib/buildEnv'

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
            before any JS runs. The OS picks between them on its own;
            `bootThemeScript` only rewrites the third, media-less tag, for
            engines that ignore `media` here. No pin can arrive this early --
            a theme is stored per account, and no identity has resolved yet.

            They answer only until the app does. `themeStore`'s DOM effect
            rewrites all three from the resolved palette and strips `media`, so
            the OS stops choosing the moment the app has a palette of its own.
          */}
          <meta name="theme-color" media="(prefers-color-scheme: light)" content={bootSplashLight.background} />
          <meta name="theme-color" media="(prefers-color-scheme: dark)" content={bootSplashDark.background} />
          <meta name="theme-color" content={bootSplashLight.background} />
          <link rel="apple-touch-icon" href="/icons/leapmux-icon-square-apple-touch.png" />
          <meta name="apple-mobile-web-app-capable" content="yes" />
          <meta name="apple-mobile-web-app-status-bar-style" content="black-translucent" />
          {/*
            Zero-JS splash polarity + html/body fill until the app stylesheet
            takes over. `bootSplashDocumentCss` is the only splash stylesheet
            — Solid's `BootSplash` matches via `[data-testid]`.
          */}
          <style>{bootSplashDocumentCss()}</style>
          {/*
            Do NOT preload Hack NF faces here. Each face is ~1.1 MB; the LTE
            cold-start tracer ranked even a single Regular preload as ~50% of
            bytes before shell_visible. The @font-face rules in
            ~/styles/global.css.ts still fetch a face when a code surface first
            needs it — after the shell is up.
          */}
          {/*
            Runs before first paint, so `data-theme` is set for the
            `bootSplashDocumentCss` rules above rather than arriving with the
            app bundle. It reads no storage: every stored theme is scoped to an
            account, and this script runs before there is one. Logic lives in
            `bootThemeScript`.
          */}
          <script>{bootThemeScript()}</script>
          {assets}
        </head>
        <body>
          {/*
            Static boot splash (no SSR): Go serves this HTML as-is. The SPA
            mount appends into `#app` and clears nothing, so the client entry
            removes this node after mount — see `removeStaticBootSplash` in
            `~/lib/bootSplashTheme` (same module as `BootSplash`).

            `id="boot-splash"` is the success signal for
            `bootFailureWatchdogScript`: the client entry removes it. Solid's
            fallback splash keeps only `data-testid`.
          */}
          <div id="app">
            <div
              id={BOOT_SPLASH_STATIC_ID}
              data-testid={BOOT_SPLASH_TEST_ID}
              role="status"
              aria-live="polite"
            >
              <div class="boot-splash-loading">
                <BootSplashIcon />
                <p class="boot-splash-label">{BOOT_SPLASH_LABEL}</p>
                <BootSplashProgress />
              </div>
              <div class="boot-splash-error" hidden>
                <p data-boot-fail-title>{BOOT_SPLASH_FAIL_TITLE}</p>
                <pre data-boot-fail-detail />
                <button type="button" data-boot-reload>{BOOT_SPLASH_RELOAD_LABEL}</button>
              </div>
            </div>
            {children}
          </div>
          {/*
            Advance the splash checklist to "Loading bundles" the moment the
            splash markup is parsed; the stylesheet reads the phase attribute,
            and entry-client is the next advance. Placed with the watchdog —
            after `#app`, inline, dependent on nothing.
          */}
          <script>{bootPhaseScript()}</script>
          {/*
            After `#app` so a failed entry chunk still leaves the splash in the
            DOM for the watchdog to rewrite. Inline — must not depend on
            a module that may already have failed to load.
          */}
          <script>{bootFailureWatchdogScript()}</script>
          {scripts}
        </body>
      </html>
    )}
  />
))
