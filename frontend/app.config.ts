import { resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { defineConfig } from '@solidjs/start/config'
import { vanillaExtractPlugin } from '@vanilla-extract/vite-plugin'

const __dirname = fileURLToPath(new URL('.', import.meta.url))

export default defineConfig({
  ssr: false,
  server: { static: true },
  vite: {
    build: { sourcemap: true },
    plugins: [
      vanillaExtractPlugin({ identifiers: 'debug' }),
      {
        // Workaround: vinxi leaks absolute paths into $component.src in the
        // client bundle, but window.manifest keys are relative. Strip the
        // absolute prefix so asset preloading can match manifest entries.
        name: 'strip-absolute-paths',
        renderChunk(code: string) {
          const prefix = `${process.cwd()}/`
          if (code.includes(prefix)) {
            return code.replaceAll(prefix, '')
          }
          return null
        },
      },
    ],
    envPrefix: ['VITE_', 'LEAPMUX_'],
    resolve: { alias: { '~': resolve(__dirname, 'src') } },
    optimizeDeps: {
      include: [
        // Shiki syntax highlighting — many deep sub-path imports that the
        // initial dep scan misses because they live behind lazy-loaded routes.
        'shiki/core',
        'shiki/engine/javascript',
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
        '@milkdown/plugin-highlight/shiki',
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
        '@xterm/addon-webgl',
        // Protobuf / ConnectRPC
        '@bufbuild/protobuf',
        '@bufbuild/protobuf/codegenv2',
        '@connectrpc/connect',
        '@connectrpc/connect-web',
        // UI / misc
        '@knadh/oat/oat.min.js',
        'diff',
        'fracturedjsonjs',
        'fzstd',
        'random-word-slugs',
      ],
    },
    server: {
      proxy: {
        '/leapmux.v1': {
          target: process.env.LEAPMUX_HUB_URL || 'http://localhost:4327',
          changeOrigin: true,
        },
      },
    },
  },
})
