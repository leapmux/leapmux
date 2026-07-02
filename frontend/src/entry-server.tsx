import { createHandler, StartServer } from '@solidjs/start/server'
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
          <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
          <link rel="icon" href="/icons/leapmux-icon.ico" sizes="48x48" />
          <link rel="icon" href="/icons/leapmux-icon.svg" type="image/svg+xml" />
          <link rel="manifest" href="/manifest.webmanifest" />
          <meta name="theme-color" content="#F7F5F2" />
          <link rel="apple-touch-icon" href="/icons/leapmux-icon-square-apple-touch.png" />
          <meta name="apple-mobile-web-app-capable" content="yes" />
          <meta name="apple-mobile-web-app-status-bar-style" content="black-translucent" />
          {/*
            Preload the Hack NF faces so code blocks lay out with the real font
            on first paint. Without this the woff2 fetch starts only when the
            first code block matches the @font-face, and the late swap changes
            code-block heights — every one of which the chat virtualizer must
            re-measure and re-anchor. `crossorigin` is required: font preloads
            without it use a different fetch mode and the browser re-downloads.
          */}
          <link rel="preload" href="/fonts/HackNerdFont-3.003-Regular.woff2" as="font" type="font/woff2" crossorigin="anonymous" />
          <link rel="preload" href="/fonts/HackNerdFont-3.003-Bold.woff2" as="font" type="font/woff2" crossorigin="anonymous" />
          <link rel="preload" href="/fonts/HackNerdFont-3.003-Italic.woff2" as="font" type="font/woff2" crossorigin="anonymous" />
          <link rel="preload" href="/fonts/HackNerdFont-3.003-BoldItalic.woff2" as="font" type="font/woff2" crossorigin="anonymous" />
          {assets}
        </head>
        <body>
          <div id="app">{children}</div>
          {scripts}
        </body>
      </html>
    )}
  />
))
