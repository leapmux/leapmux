import type { Setter } from 'solid-js'
import { Plugin, PluginKey } from '@milkdown/prose/state'
import { Decoration, DecorationSet } from '@milkdown/prose/view'
import { $prose } from '@milkdown/utils'
import { resolveBundledLang } from '~/lib/bundledLanguage'
import { afterLineIndent } from '~/lib/regexp'

/** Handlers for the code language label plugin. */
export interface CodeLangHandlers {
  setCodeLangNodePos: Setter<number>
  setCodeLangAnchorEl: Setter<HTMLElement | undefined>
  setCodeLangPopoverOpen: Setter<boolean>
  /** Current popover open state + anchored node position -- for toggle-on-reclick. */
  getCodeLangPopoverOpen: () => boolean
  getCodeLangNodePos: () => number
}

// Simple heuristic-based language detection patterns. Each one looks for a keyword at
// the start of a line, after its indentation; afterLineIndent keeps that scan linear
// in a run of blank lines, which detection crosses on every edit of a code block.
const LANG_PATTERNS: readonly (readonly [RegExp, string])[] = ([
  [/import\s[^\n]+?from\s+['"]|export\s+(?:default\s+)?|const\s+\w+\s*[=:]|let\s+\w+\s*[=:]|interface\s+\w+|type\s+\w+\s*=/, 'typescript'],
  [/function\s+\w+|var\s+\w+\s*=|===|!==|console\.(log|error|warn)|document\.|window\./, 'javascript'],
  [/def\s+\w+|class\s+\w.*:|import\s+\w+|from\s+\w+\s+import|if\s+__name__\s*==/, 'python'],
  [/fn\s+\w+|let\s+mut\s+|impl\s+|pub\s+(fn|struct|enum|mod)|use\s+\w+::|#\[derive/, 'rust'],
  [/func\s+\w+|package\s+\w+|import\s+"[^"]+"|:=\s|fmt\.(Print|Sprint|Fprint)/, 'go'],
  [/public\s+(class|interface|static|void|abstract)|private\s+|protected\s+|@Override|System\.out\.print/, 'java'],
  [/#include\s*[<"]|int\s+main\s*\(|void\s+\w+\s*\(|printf\s*\(|std::/, 'cpp'],
  [/SELECT\s+|INSERT\s+|UPDATE\s+|DELETE\s+|CREATE\s+(TABLE|INDEX|DATABASE)|ALTER\s+TABLE|FROM\s+\w+\s+WHERE/i, 'sql'],
  [/<\?xml|<html|<div|<span|<body|<!DOCTYPE/i, 'html'],
  [/\{\s*"[^"]+"\s*:|\[\s*\{/, 'json'],
  [/\w+:\s*\n|\w+:\s*[|>]/, 'yaml'],
  [/\[\w+\]|\w+\s*=\s*/, 'toml'],
  [/#!\s*\/bin\/(ba)?sh|#!\/usr\/bin\/env\s+(ba)?sh|\$\s+\w+|echo\s+|export\s+\w+=/, 'bash'],
  [/@import|@media|@keyframes|\.\w+\s*\{|#\w+\s*\{/, 'css'],
  [/diff\s+--git|---\s+a\/|@@\s+-\d+,\d+\s+\+\d+,\d+\s+@@|[+-]{3}\s+/, 'diff'],
] satisfies [RegExp, string][]).map(([body, lang]) => {
  const pattern = afterLineIndent(body)
  return [pattern, lang] as const
})

/**
 * Detect a programming language from code content using heuristics.
 * Returns a language ID string or null if no confident match. Only returns a
 * language Shiki actually bundles a grammar for (the lazy highlighter loads it
 * on demand); unsupported matches fall through to plain.
 */
function detectLanguage(code: string): string | null {
  for (const [pattern, lang] of LANG_PATTERNS) {
    if (pattern.test(code) && resolveBundledLang(lang)) {
      return lang
    }
  }
  return null
}

/**
 * Creates a languageExtractor for the code highlight plugin that auto-detects
 * the language of code blocks via heuristics when no explicit language is set.
 * This does NOT modify the code_block node's `language` attribute — it only
 * provides the detected language to the syntax highlighter. An explicit language
 * is passed through verbatim. `createCodeHighlightPlugin` resolves it with
 * `resolveBundledLang` and leaves a block plain when Shiki has no grammar for it,
 * and the token worker loads the grammar.
 */
export function createAutoDetectLanguageExtractor() {
  return (node: { attrs: Record<string, unknown>, textContent: string }): string | undefined => {
    const lang = node.attrs.language as string
    if (lang && lang !== 'plaintext')
      return lang
    const code = node.textContent
    if (code.length < 10)
      return undefined
    return detectLanguage(code) ?? undefined
  }
}

/**
 * Code block language label — shows a clickable label at the top-right of code blocks.
 * Also adds a clickable "+" area after each code block to insert a paragraph.
 */
export function createCodeLangPlugin(handlers: CodeLangHandlers) {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('code-lang-label'),
      props: {
        decorations(state) {
          const decorations: Decoration[] = []
          state.doc.descendants((node, pos) => {
            if (node.type.name === 'code_block') {
              const lang = (node.attrs.language as string) || ''
              const label = lang || 'plaintext'
              // Language label at top-right
              decorations.push(
                Decoration.widget(pos + 1, () => {
                  const span = document.createElement('span')
                  span.className = 'code-lang-label'
                  span.textContent = label
                  span.setAttribute('data-testid', 'code-lang-label')
                  span.setAttribute('contenteditable', 'false')
                  // Capture whether the popover was already open for THIS label on
                  // pointerdown -- before the popover's light-dismiss (which runs on
                  // pointerdown) closes it -- so the click toggles closed instead of
                  // unconditionally reopening.
                  let wasOpenForThisLabel = false
                  span.addEventListener('pointerdown', () => {
                    wasOpenForThisLabel = handlers.getCodeLangPopoverOpen()
                      && handlers.getCodeLangNodePos() === pos
                  })
                  span.addEventListener('click', (e) => {
                    e.preventDefault()
                    e.stopPropagation()
                    if (wasOpenForThisLabel) {
                      handlers.setCodeLangPopoverOpen(false)
                      handlers.setCodeLangNodePos(-1)
                    }
                    else {
                      handlers.setCodeLangNodePos(pos)
                      handlers.setCodeLangAnchorEl(span)
                      handlers.setCodeLangPopoverOpen(true)
                    }
                  })
                  return span
                }, { side: -1, key: `lang-${pos}-${lang}` }),
              )
            }
          })
          return DecorationSet.create(state.doc, decorations)
        },
      },
    })
  })
}
