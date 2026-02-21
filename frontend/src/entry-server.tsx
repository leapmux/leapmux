import { createHandler, StartServer } from '@solidjs/start/server'

export default createHandler(() => (
  <StartServer
    document={({ assets, children, scripts }) => (
      <html lang="en">
        <head>
          <meta charset="utf-8" />
          <meta name="viewport" content="width=device-width, initial-scale=1" />
          <link rel="icon" href="/favicon.ico" sizes="48x48" />
          <link rel="icon" href="/icons/icon-192.png" type="image/png" sizes="192x192" />
          <link rel="icon" href="/icons/icon-512.png" type="image/png" sizes="512x512" />
          <link rel="manifest" href="/manifest.webmanifest" />
          <meta name="theme-color" content="#ffffff" />
          <link rel="apple-touch-icon" href="/icons/apple-touch-icon.png" />
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
