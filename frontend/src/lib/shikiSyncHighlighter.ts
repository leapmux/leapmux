import themeGithubDark from '@shikijs/themes/github-dark'
import themeGithubLight from '@shikijs/themes/github-light'
import { createHighlighterCoreSync } from 'shiki/core'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import { withTransparentBg } from './syntaxThemes'

/**
 * The synchronous Shiki highlighter of the main thread, on the JavaScript regex
 * engine. It holds no grammar.
 *
 * The browser uses it for ANSI output only (`renderAnsi`, `ansiSyncTokenize`), and
 * Shiki colours ANSI without a grammar. Code in every other language tokenizes in a
 * worker: Markdown in the Markdown worker, and code views in the Shiki token worker.
 * Twenty grammars here cost every page load about 60 ms (21 ms to parse them, 37 ms to
 * compile them) and 1.78 MB, for code that no browser path highlighted here.
 *
 * The initial pair is the Default theme's, GitHub's, imported STATICALLY: a
 * synchronous highlighter cannot await a chunk, so the one pair the app starts on has
 * to sit in this module. Every other pair arrives lazily: `setSyntaxTheme` registers
 * it here before it points the shared options at it, so a synchronous call site never
 * specifies an unregistered theme.
 */
export const shikiHighlighter = createHighlighterCoreSync({
  themes: [
    withTransparentBg(themeGithubLight),
    withTransparentBg(themeGithubDark),
  ],
  langs: [],
  engine: createJavaScriptRegexEngine(),
})
