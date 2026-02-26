import type { HighlighterCore } from 'shiki/core'
import type { Setter } from 'solid-js'
import { Plugin, PluginKey } from '@milkdown/prose/state'
import { Decoration, DecorationSet } from '@milkdown/prose/view'
import { $prose } from '@milkdown/utils'

/** Setters for active-format signals driven by the toolbar state plugin. */
export interface ToolbarStateSetters {
  setActiveBold: Setter<boolean>
  setActiveItalic: Setter<boolean>
  setActiveStrikethrough: Setter<boolean>
  setActiveCode: Setter<boolean>
  setActiveCodeBlock: Setter<boolean>
  setActiveBlockquote: Setter<boolean>
  setActiveLink: Setter<boolean>
  setActiveHeadingLevel: Setter<number>
  setActiveBulletList: Setter<boolean>
  setActiveOrderedList: Setter<boolean>
  setActiveTaskList: Setter<boolean>
}

/** Tracks the active formatting state of the cursor and updates SolidJS signals. */
export function createToolbarStatePlugin(setters: ToolbarStateSetters) {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('toolbar-state'),
      view() {
        return {
          update(view) {
            const { state } = view
            const marks = state.storedMarks || state.selection.$from.marks()
            setters.setActiveBold(marks.some(m => m.type.name === 'strong'))
            setters.setActiveItalic(marks.some(m => m.type.name === 'em'))
            setters.setActiveStrikethrough(marks.some(m => m.type.name === 'strike_through'))
            setters.setActiveCode(marks.some(m => m.type.name === 'inlineCode'))
            setters.setActiveLink(marks.some(m => m.type.name === 'link'))

            const parentType = state.selection.$from.parent.type.name
            setters.setActiveCodeBlock(parentType === 'code_block')

            // Check blockquote by walking up the resolved position's depth
            const $from = state.selection.$from
            let inBlockquote = false
            for (let d = $from.depth; d >= 0; d--) {
              if ($from.node(d).type.name === 'blockquote') {
                inBlockquote = true
                break
              }
            }
            setters.setActiveBlockquote(inBlockquote)

            if (parentType === 'heading') {
              setters.setActiveHeadingLevel(state.selection.$from.parent.attrs.level as number)
            }
            else {
              setters.setActiveHeadingLevel(0)
            }

            // Detect list type by walking up ancestors
            let activeBulletList = false
            let activeOrderedList = false
            let activeTaskList = false
            for (let d = $from.depth; d >= 1; d--) {
              const node = $from.node(d)
              if (node.type.name === 'bullet_list') {
                // Check if task list: list items have a non-null `checked` attribute
                const listItemIdx = $from.index(d)
                const listItem = node.child(listItemIdx)
                if (listItem.type.name === 'list_item' && listItem.attrs.checked != null) {
                  activeTaskList = true
                }
                else {
                  activeBulletList = true
                }
                break
              }
              if (node.type.name === 'ordered_list') {
                activeOrderedList = true
                break
              }
            }
            setters.setActiveBulletList(activeBulletList)
            setters.setActiveOrderedList(activeOrderedList)
            setters.setActiveTaskList(activeTaskList)
          },
        }
      },
    })
  })
}

/** Handlers for the code language label plugin. */
export interface CodeLangHandlers {
  setCodeLangNodePos: Setter<number>
  setCodeLangAnchorEl: Setter<HTMLElement | undefined>
  setCodeLangPopoverOpen: Setter<boolean>
}

// Simple heuristic-based language detection patterns
const LANG_PATTERNS: [RegExp, string][] = [
  [/^\s*(?:import\s[^\n]+?from\s+['"]|export\s+(?:default\s+)?|const\s+\w+\s*[=:]|let\s+\w+\s*[=:]|interface\s+\w+|type\s+\w+\s*=)/m, 'typescript'],
  [/^\s*(function\s+\w+|var\s+\w+\s*=|===|!==|console\.(log|error|warn)|document\.|window\.)/m, 'javascript'],
  [/^\s*(def\s+\w+|class\s+\w.*:|import\s+\w+|from\s+\w+\s+import|if\s+__name__\s*==)/m, 'python'],
  [/^\s*(fn\s+\w+|let\s+mut\s+|impl\s+|pub\s+(fn|struct|enum|mod)|use\s+\w+::|#\[derive)/m, 'rust'],
  [/^\s*(func\s+\w+|package\s+\w+|import\s+"[^"]+"|:=\s|fmt\.(Print|Sprint|Fprint))/m, 'go'],
  [/^\s*(public\s+(class|interface|static|void|abstract)|private\s+|protected\s+|@Override|System\.out\.print)/m, 'java'],
  [/^\s*(#include\s*[<"]|int\s+main\s*\(|void\s+\w+\s*\(|printf\s*\(|std::)/m, 'cpp'],
  [/^\s*(SELECT\s+|INSERT\s+|UPDATE\s+|DELETE\s+|CREATE\s+(TABLE|INDEX|DATABASE)|ALTER\s+TABLE|FROM\s+\w+\s+WHERE)/im, 'sql'],
  [/^\s*(<\?xml|<html|<div|<span|<body|<!DOCTYPE)/im, 'html'],
  [/^\s*(?:\{\s*"[^"]+"\s*:|\[\s*\{)/m, 'json'],
  [/^\s*(\w+:\s*\n|\w+:\s*[|>])/m, 'yaml'],
  [/^\s*(\[\w+\]|\w+\s*=\s*)/m, 'toml'],
  [/^\s*(#!\s*\/bin\/(ba)?sh|#!\/usr\/bin\/env\s+(ba)?sh|\$\s+\w+|echo\s+|export\s+\w+=)/m, 'bash'],
  [/^\s*(@import|@media|@keyframes|\.\w+\s*\{|#\w+\s*\{)/m, 'css'],
  [/^\s*(diff\s+--git|---\s+a\/|@@\s+-\d+,\d+\s+\+\d+,\d+\s+@@|[+-]{3}\s+)/m, 'diff'],
]

/**
 * Detect a programming language from code content using heuristics.
 * Returns a language ID string or null if no confident match.
 */
function detectLanguage(code: string, highlighter: HighlighterCore): string | null {
  const loadedLangs = highlighter.getLoadedLanguages()
  for (const [pattern, lang] of LANG_PATTERNS) {
    if (pattern.test(code) && loadedLangs.includes(lang)) {
      return lang
    }
  }
  return null
}

/**
 * Creates a languageExtractor for prosemirror-highlight that auto-detects
 * the language of code blocks via heuristics when no explicit language is set.
 * This does NOT modify the code_block node's `language` attribute — it only
 * provides the detected language to the syntax highlighter.
 */
export function createAutoDetectLanguageExtractor(highlighter: HighlighterCore) {
  return (node: { attrs: Record<string, unknown>, textContent: string }): string | undefined => {
    const lang = node.attrs.language as string
    if (lang && lang !== 'plaintext')
      return lang
    const code = node.textContent
    if (code.length < 10)
      return undefined
    return detectLanguage(code, highlighter) ?? undefined
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
                  span.addEventListener('click', (e) => {
                    e.preventDefault()
                    e.stopPropagation()
                    handlers.setCodeLangNodePos(pos)
                    handlers.setCodeLangAnchorEl(span)
                    handlers.setCodeLangPopoverOpen(true)
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
