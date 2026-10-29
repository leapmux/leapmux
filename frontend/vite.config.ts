import { createRequire } from 'node:module'
import process from 'node:process'
import { solidStart } from '@solidjs/start/config'
import { vanillaExtractPlugin } from '@vanilla-extract/vite-plugin'
import { nitro } from 'nitro/vite'
import { defineConfig } from 'vite'
import { VITE_SOURCE_DIRECTORIES } from './viteAssetAccess.ts'

const require = createRequire(import.meta.url)

// Force the DOM-free build of decode-named-character-reference (a micromark/
// remark-parse dependency used by the markdown pipeline). Its `browser` export
// condition resolves to index.dom.js, which calls `document.createElement` at
// module load -- fine on the main thread, but a `ReferenceError: document is not
// defined` inside the markdown render worker (markdownWorker.ts), which has no DOM.
// The default `index.js` is a lookup-table implementation that works in both, so
// alias the bare specifier to its resolved path (the package exposes no `./index.js`
// subpath to import directly).
const decodeNamedCharacterReferenceNodeBuild = require.resolve('decode-named-character-reference')

export default defineConfig({
  // 4096 kB: the (app) chunk (the whole one-route SPA) and the two largest
  // Shiki grammar chunks (cpp, emacs-lisp) legitimately exceed the 500 kB
  // default. The grammars are single lazy-loaded files that cannot be split
  // further, and the one-route SPA has no route boundaries to split along,
  // so the default limit only reports noise.
  build: { sourcemap: true, chunkSizeWarningLimit: 4096 },
  plugins: [
    vanillaExtractPlugin({ identifiers: 'debug' }),
    solidStart({ ssr: false }),
    nitro({
      // The hub embeds .output/public and serves it with spautil, so the
      // production build is static files and runs no server of its own.
      preset: 'static',
      prerender: {
        // The one-route SPA has one document. spautil answers every route
        // path with this index.html, and the client router takes over.
        routes: ['/'],
        // The static preset crawls by default. The shell links only to
        // files, so crawling finds nothing and only costs build time.
        crawlLinks: false,
        // Without this flag, a shell that throws while rendering still
        // builds green with no index.html, and the hub then has no page to
        // serve for any route.
        failOnError: true,
      },
      // spautil serves the precompressed .br and .gz sibling of a file and
      // never compresses a response itself.
      compressPublicAssets: { gzip: true, brotli: true },
    }),
  ],
  envPrefix: ['VITE_', 'LEAPMUX_'],
  resolve: {
    // Maps tsconfig.json's `paths` for Vite, so a mapping is declared once
    // for tsc, Vite and vitest rather than three times over. SolidStart's
    // `solid-start:app-root-alias` plugin resolves `~` to `src` before Vite
    // reads this option, so today `~` resolves through SolidStart here and
    // through this option under vitest. A second entry in `paths` would need
    // this option in both.
    tsconfigPaths: true,
    alias: {
      // See decodeNamedCharacterReferenceNodeBuild above: keep the markdown
      // worker from importing the document-dependent browser build.
      'decode-named-character-reference': decodeNamedCharacterReferenceNodeBuild,
    },
  },
  optimizeDeps: {
    include: [
      // Shiki syntax highlighting — many deep sub-path imports that the
      // initial dep scan misses because they live behind lazy-loaded routes.
      'shiki/core',
      'shiki/engine/javascript',
      // Oniguruma WASM engine (workers + editor) + inlined WASM + the lazily
      // loaded bundled-languages map. The ~332 per-language grammar chunks are
      // intentionally NOT listed: they must stay dynamic so Vite code-splits
      // each into an on-demand chunk (lazy loading is the whole point).
      'shiki/engine/oniguruma',
      'shiki/wasm',
      'shiki/langs',
      'shiki/langs/bash.mjs',
      'shiki/langs/c.mjs',
      'shiki/langs/cpp.mjs',
      'shiki/langs/css.mjs',
      'shiki/langs/diff.mjs',
      'shiki/langs/go.mjs',
      'shiki/langs/html.mjs',
      'shiki/langs/java.mjs',
      'shiki/langs/javascript.mjs',
      'shiki/langs/json.mjs',
      'shiki/langs/jsx.mjs',
      'shiki/langs/markdown.mjs',
      'shiki/langs/python.mjs',
      'shiki/langs/rust.mjs',
      'shiki/langs/sql.mjs',
      'shiki/langs/toml.mjs',
      'shiki/langs/tsx.mjs',
      'shiki/langs/typescript.mjs',
      'shiki/langs/xml.mjs',
      'shiki/langs/yaml.mjs',
      '@shikijs/rehype/core',
      '@shikijs/themes/github-dark',
      '@shikijs/themes/github-light',
      // Milkdown markdown editor
      '@milkdown/core',
      '@milkdown/ctx',
      '@milkdown/plugin-clipboard',
      '@milkdown/plugin-highlight',
      '@milkdown/plugin-history',
      '@milkdown/plugin-listener',
      '@milkdown/preset-commonmark',
      '@milkdown/preset-gfm',
      '@milkdown/prose/commands',
      '@milkdown/prose/inputrules',
      '@milkdown/prose/model',
      '@milkdown/prose/schema-list',
      '@milkdown/prose/state',
      '@milkdown/prose/view',
      '@milkdown/utils',
      // Remark / Rehype / Unified markdown pipeline
      'rehype-stringify',
      'remark-gfm',
      'remark-parse',
      'remark-rehype',
      'unified',
      'unist-util-visit',
      // Terminal
      '@xterm/xterm',
      '@xterm/addon-fit',
      '@xterm/addon-serialize',
      '@xterm/addon-webgl',
      // Protobuf / ConnectRPC
      '@bufbuild/protobuf',
      '@bufbuild/protobuf/codegenv2',
      '@bufbuild/protobuf/wkt',
      '@connectrpc/connect',
      '@connectrpc/connect-web',
      // Encryption
      '@noble/ciphers/chacha.js',
      '@noble/curves/ed25519.js',
      '@noble/hashes/blake2.js',
      '@noble/hashes/hmac.js',
      '@noble/hashes/utils.js',
      '@noble/post-quantum/ml-kem.js',
      '@noble/post-quantum/slh-dsa.js',
      // Persistence (IndexedDB). Reached through lazily-imported render-cache
      // paths, so the initial dep scan can miss it and re-optimize
      // mid-session, which costs a full page reload. Its `exports` map also
      // carries development and production conditions; pre-bundling pins one.
      'dexie',
      // UI / misc
      '@knadh/oat/oat.min.js',
      '@tauri-apps/api/core',
      '@tauri-apps/api/dpi',
      '@tauri-apps/api/event',
      '@tauri-apps/api/webviewWindow',
      '@tauri-apps/api/window',
      '@tauri-apps/plugin-clipboard-manager',
      '@tauri-apps/plugin-notification',
      '@tauri-apps/plugin-opener',
      'diff',
      'fracturedjsonjs',
      'fzstd',
      'nanoid',
      'random-word-slugs',
      'tinykeys',
    ],
  },
  server: {
    fs: { allow: VITE_SOURCE_DIRECTORIES },
    proxy: {
      '/leapmux.v1': {
        target: process.env.LEAPMUX_HUB_URL || 'http://localhost:4327',
        changeOrigin: true,
      },
    },
  },
})
