import { createHandler, StartServer } from '@solidjs/start/server'
import { frontendBuildInfo } from '~/lib/buildEnv'
import { paletteColorToHex, resolveVariant } from '~/styles/themes'
import { defaultTheme } from '~/styles/themes/default'

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
            The pre-hydration chrome colour, taken from the palette rather than
            restated. ~/lib/themeStore rewrites it from the RESOLVED theme once
            it runs; until then :root carries the default light palette, so this
            tag has to agree with it. Three literals used to disagree here.
          */}
          <meta name="theme-color" content={paletteColorToHex(resolveVariant(defaultTheme, undefined, 'light').palette['--background']!)} />
          <link rel="apple-touch-icon" href="/icons/leapmux-icon-square-apple-touch.png" />
          <meta name="apple-mobile-web-app-capable" content="yes" />
          <meta name="apple-mobile-web-app-status-bar-style" content="black-translucent" />
          {/*
            Do NOT preload Hack NF faces here. Each face is ~1.1 MB; the LTE
            cold-start tracer ranked even a single Regular preload as ~50% of
            bytes before shell_visible. The @font-face rules in
            ~/styles/global.css.ts still fetch a face when a code surface first
            needs it — after the shell is up.
          */}
          {assets}
        </head>
        <body>
          {/*
            Static boot splash (no SSR): Go serves this HTML as-is. Solid's
            client mount replaces `#app` contents. Keep in lockstep with
            `~/components/common/BootSplash` (Suspense / AuthGuard fallback).
            Inline styles only — the CSS chunk may not have arrived yet.
          */}
          <div id="app">
            <div
              id="boot-splash"
              data-testid="boot-splash"
              role="status"
              aria-live="polite"
              style={{
                'min-height': '100dvh',
                'display': 'flex',
                'align-items': 'center',
                'justify-content': 'center',
                'flex-direction': 'column',
                'gap': '1rem',
                'font-family': 'system-ui, sans-serif',
                'color': 'var(--foreground, #1a1917)',
                'background': 'var(--background, #fffefc)',
              }}
            >
              <img src="/icons/leapmux-icon.svg" width="64" height="64" alt="" />
              <p style={{ 'margin': '0', 'font-size': '0.95rem' }}>Loading LeapMux…</p>
            </div>
            {children}
          </div>
          {scripts}
        </body>
      </html>
    )}
  />
))
