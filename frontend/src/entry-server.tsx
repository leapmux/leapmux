import { createHandler, StartServer } from '@solidjs/start/server'

export default createHandler(() => (
  <StartServer
    document={({ assets, children, scripts }) => (
      <html lang="en">
        <head>
          <meta charset="utf-8" />
          <meta name="viewport" content="width=device-width, initial-scale=1" />
          {/* Disable Wails auto-injection so we can inject explicitly.
              This ensures bindings survive WKWebView content process
              termination (macOS sleep/wake). In non-Wails contexts the
              meta tag is ignored and the scripts 404 harmlessly. */}
          <meta name="wails-options" content="noautoinject" />
          <script src="/wails/ipc.js" />
          <script src="/wails/runtime.js" />
          <link rel="icon" href="/leapmux-icon-corners.ico" sizes="48x48" />
          <link rel="icon" href="/leapmux-icon-corners.svg" type="image/svg+xml" />
          <link rel="manifest" href="/manifest.webmanifest" />
          <meta name="theme-color" content="#F7F5F2" />
          <link rel="apple-touch-icon" href="/icons/leapmux-icon-apple-touch.png" />
          <meta name="apple-mobile-web-app-capable" content="yes" />
          <meta name="apple-mobile-web-app-status-bar-style" content="black-translucent" />
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
