import type { CachedToken } from './tokenCache'

import themeGithubDark from '@shikijs/themes/github-dark'
import themeGithubLight from '@shikijs/themes/github-light'
import { createHighlighterCore } from 'shiki/core'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import langBash from 'shiki/langs/bash.mjs'
import langC from 'shiki/langs/c.mjs'
import langCpp from 'shiki/langs/cpp.mjs'
import langCss from 'shiki/langs/css.mjs'
import langDiff from 'shiki/langs/diff.mjs'
import langGo from 'shiki/langs/go.mjs'
import langHtml from 'shiki/langs/html.mjs'
import langJava from 'shiki/langs/java.mjs'
import langJavascript from 'shiki/langs/javascript.mjs'
import langJson from 'shiki/langs/json.mjs'
import langJsx from 'shiki/langs/jsx.mjs'
import langMarkdown from 'shiki/langs/markdown.mjs'
import langPython from 'shiki/langs/python.mjs'
import langRust from 'shiki/langs/rust.mjs'
import langSql from 'shiki/langs/sql.mjs'
import langToml from 'shiki/langs/toml.mjs'
import langTsx from 'shiki/langs/tsx.mjs'
import langTypescript from 'shiki/langs/typescript.mjs'
import langXml from 'shiki/langs/xml.mjs'
import langYaml from 'shiki/langs/yaml.mjs'

export interface TokenizeRequest {
  type: 'tokenize'
  id: number
  lang: string
  code: string
}

export interface TokenizeResponse {
  type: 'tokenize-result'
  id: number
  tokens: CachedToken[][] | null
}

let highlighter: Awaited<ReturnType<typeof createHighlighterCore>> | null = null
let initPromise: Promise<void> | null = null

async function ensureHighlighter(): Promise<void> {
  if (highlighter)
    return
  if (!initPromise) {
    initPromise = createHighlighterCore({
      themes: [themeGithubLight, themeGithubDark],
      langs: [
        langTypescript,
        langJavascript,
        langPython,
        langRust,
        langGo,
        langJava,
        langBash,
        langJson,
        langHtml,
        langCss,
        langYaml,
        langToml,
        langSql,
        langMarkdown,
        langDiff,
        langC,
        langCpp,
        langJsx,
        langTsx,
        langXml,
      ],
      engine: createJavaScriptRegexEngine(),
    }).then((h) => { highlighter = h })
  }
  await initPromise
}

globalThis.onmessage = async (e: MessageEvent<TokenizeRequest>) => {
  const msg = e.data
  if (msg.type === 'tokenize') {
    try {
      await ensureHighlighter()
      const result = highlighter!.codeToTokens(msg.code, {
        lang: msg.lang,
        themes: { light: 'github-light', dark: 'github-dark' },
        defaultColor: false,
      })
      const tokens: CachedToken[][] = result.tokens.map(line =>
        line.map(t => ({ content: t.content, htmlStyle: t.htmlStyle })),
      )
      const response: TokenizeResponse = { type: 'tokenize-result', id: msg.id, tokens }
      globalThis.postMessage(response)
    }
    catch {
      const response: TokenizeResponse = { type: 'tokenize-result', id: msg.id, tokens: null }
      globalThis.postMessage(response)
    }
  }
}
