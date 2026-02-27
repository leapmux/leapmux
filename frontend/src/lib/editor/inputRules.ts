import type { Attrs, Mark, MarkType } from '@milkdown/prose/model'
import type { EditorState, Transaction } from '@milkdown/prose/state'

import { schemaCtx } from '@milkdown/core'
import { InputRule } from '@milkdown/prose/inputrules'
import { TextSelection } from '@milkdown/prose/state'
import { $inputRule } from '@milkdown/utils'

/**
 * Input rule for "- " (or "+ " or "* ") typed after a hard break (Shift+Enter).
 * Splits the paragraph at the hard break, wrapping the new paragraph in a bullet list.
 */
export function createBulletListAfterHardBreakInputRule() {
  return $inputRule(() => {
    return new InputRule(
      /\uFFFC[-+*]\s$/,
      (state, _match, start, end) => {
        const bulletListType = state.schema.nodes.bullet_list
        const listItemType = state.schema.nodes.list_item
        const paragraphType = state.schema.nodes.paragraph
        if (!bulletListType || !listItemType || !paragraphType)
          return null

        const $start = state.doc.resolve(start)
        // Delete the matched content (hard_break + marker + space)
        const tr = state.tr.delete(start, end)
        // Insert a new bullet_list > list_item > paragraph after the current paragraph
        const afterParagraph = tr.mapping.map($start.after($start.depth))
        const emptyPara = paragraphType.create()
        const listItem = listItemType.create(null, emptyPara)
        const bulletList = bulletListType.create(null, listItem)
        tr.insert(afterParagraph, bulletList)
        // Position cursor inside the new list_item's paragraph
        tr.setSelection(TextSelection.create(tr.doc, afterParagraph + 3))
        return tr
      },
    )
  })
}

/**
 * Input rule for "1. " (or any number) typed after a hard break (Shift+Enter).
 * Splits the paragraph at the hard break, wrapping the new paragraph in an ordered list.
 */
export function createOrderedListAfterHardBreakInputRule() {
  return $inputRule(() => {
    return new InputRule(
      /\uFFFC(\d+)\.\s$/,
      (state, match, start, end) => {
        const orderedListType = state.schema.nodes.ordered_list
        const listItemType = state.schema.nodes.list_item
        const paragraphType = state.schema.nodes.paragraph
        if (!orderedListType || !listItemType || !paragraphType)
          return null

        const order = Number(match[1])
        const $start = state.doc.resolve(start)
        // Delete the matched content (hard_break + number + dot + space)
        const tr = state.tr.delete(start, end)
        // Insert a new ordered_list > list_item > paragraph after the current paragraph
        const afterParagraph = tr.mapping.map($start.after($start.depth))
        const emptyPara = paragraphType.create()
        const listItem = listItemType.create(null, emptyPara)
        const orderedList = orderedListType.create({ order }, listItem)
        tr.insert(afterParagraph, orderedList)
        // Position cursor inside the new list_item's paragraph
        tr.setSelection(TextSelection.create(tr.doc, afterParagraph + 3))
        return tr
      },
    )
  })
}

/** Input rule to convert [text](url) to a link. */
export function createLinkInputRule() {
  return $inputRule(() => {
    return new InputRule(
      /\[(?<text>[^\]]+)\]\((?<href>[^)]+)\)$/,
      (state, match, start, end) => {
        const linkMarkType = state.schema.marks.link
        if (!linkMarkType)
          return null
        const text = match.groups?.text ?? ''
        const href = match.groups?.href ?? ''
        const mark = linkMarkType.create({ href })
        return state.tr
          .delete(start, end)
          .insertText(text, start)
          .addMark(start, start + text.length, mark)
          .removeStoredMark(linkMarkType)
      },
    )
  })
}

/**
 * Input rule for --- that converts to a horizontal rule.
 * Handles regular paragraphs, list items, and hard breaks (Shift+Enter).
 * The optional \ufffc consumes a preceding hard_break so that the <br>
 * is removed together with the dashes.
 */
export function createHrInputRule() {
  return $inputRule(() => {
    return new InputRule(
      /\uFFFC?---$/,
      (state, _match, start, end) => {
        const $start = state.doc.resolve(start)
        // Don't trigger inside inline code marks (document marks or stored marks)
        const inlineCodeMark = state.schema.marks.inlineCode
        if (inlineCodeMark) {
          // Check stored marks (set when user clicks code button before typing)
          if (state.storedMarks?.some(m => m.type === inlineCodeMark))
            return null
          let hasCodeMark = false
          state.doc.nodesBetween(start, end, (node) => {
            if (node.isInline && node.marks.some(m => m.type === inlineCodeMark))
              hasCodeMark = true
          })
          if (hasCodeMark)
            return null
        }
        const hr = state.schema.nodes.hr.create()
        const paragraphWillBeEmpty = $start.parent.content.size === end - start

        // Text (or a hard break) precedes the dashes — delete the
        // matched content and insert an HR after the paragraph.
        if (!paragraphWillBeEmpty) {
          const tr = state.tr.delete(start, end)
          const afterParagraph = tr.mapping.map($start.after($start.depth))
          tr.insert(afterParagraph, hr)
          // Insert a paragraph after the HR for continued editing
          const para = state.schema.nodes.paragraph.create()
          tr.insert(afterParagraph + 1, para)
          tr.setSelection(TextSelection.create(tr.doc, afterParagraph + 2))
          return tr
        }

        // Paragraph will be empty after removing dashes.
        // Check if we're inside a list item for special handling.
        let listItemDepth = -1
        for (let d = $start.depth; d >= 1; d--) {
          if ($start.node(d).type.name === 'list_item') {
            listItemDepth = d
            break
          }
        }

        if (listItemDepth >= 0) {
          const isFirstChild = $start.index(listItemDepth) === 0

          if (!isFirstChild) {
            // Non-first paragraph in list item: replace whole paragraph
            const paragraphStart = $start.before($start.depth)
            const paragraphEnd = $start.after($start.depth)
            const tr = state.tr.replaceWith(paragraphStart, paragraphEnd, hr)
            return tr
          }

          const tr = state.tr.delete(start, end)
          const afterParagraph = tr.mapping.map($start.after($start.depth))
          tr.insert(afterParagraph, hr)
          return tr
        }

        // Regular paragraph: replace with HR and add a paragraph after
        const paragraphStart = $start.before($start.depth)
        const paragraphEnd = $start.after($start.depth)
        const para = state.schema.nodes.paragraph.create()
        const tr = state.tr.replaceWith(paragraphStart, paragraphEnd, [hr, para])
        tr.setSelection(TextSelection.create(tr.doc, paragraphStart + 2))
        return tr
      },
    )
  })
}

/**
 * Input rule for ``` that triggers immediately on the third backtick.
 * Handles list items and regular paragraphs, and also fires when ```
 * is typed after existing text (e.g. "foo```") or after a hard break
 * (Shift+Enter). The optional \ufffc consumes a preceding hard_break
 * so that the <br> is removed together with the backticks.
 */
export function createCodeBlockInputRule() {
  return $inputRule(() => {
    return new InputRule(
      /\uFFFC?```$/,
      (state, _match, start, end) => {
        const $start = state.doc.resolve(start)
        // Don't trigger inside inline code marks (document marks or stored marks)
        const inlineCodeMark = state.schema.marks.inlineCode
        if (inlineCodeMark) {
          // Check stored marks (set when user clicks code button before typing)
          if (state.storedMarks?.some(m => m.type === inlineCodeMark))
            return null
          let hasCodeMark = false
          state.doc.nodesBetween(start, end, (node) => {
            if (node.isInline && node.marks.some(m => m.type === inlineCodeMark))
              hasCodeMark = true
          })
          if (hasCodeMark)
            return null
        }
        const codeBlock = state.schema.nodes.code_block.create()
        const paragraphWillBeEmpty = $start.parent.content.size === end - start

        // Text (or a hard break) precedes the backticks — delete the
        // matched content and insert a code block after the paragraph.
        if (!paragraphWillBeEmpty) {
          const tr = state.tr.delete(start, end)
          const afterParagraph = tr.mapping.map($start.after($start.depth))
          tr.insert(afterParagraph, codeBlock)
          tr.setSelection(TextSelection.create(tr.doc, afterParagraph + 1))
          return tr
        }

        // Paragraph will be empty after removing backticks.
        // Check if we're inside a list item for special handling.
        let listItemDepth = -1
        for (let d = $start.depth; d >= 1; d--) {
          if ($start.node(d).type.name === 'list_item') {
            listItemDepth = d
            break
          }
        }

        if (listItemDepth >= 0) {
          const isFirstChild = $start.index(listItemDepth) === 0

          if (!isFirstChild) {
            // Non-first paragraph in list item: replace whole paragraph
            const paragraphStart = $start.before($start.depth)
            const paragraphEnd = $start.after($start.depth)
            const tr = state.tr.replaceWith(paragraphStart, paragraphEnd, codeBlock)
            tr.setSelection(TextSelection.create(tr.doc, paragraphStart + 1))
            return tr
          }

          const tr = state.tr.delete(start, end)
          const afterParagraph = tr.mapping.map($start.after($start.depth))
          tr.insert(afterParagraph, codeBlock)
          tr.setSelection(TextSelection.create(tr.doc, afterParagraph + 1))
          return tr
        }

        // Regular paragraph: convert to code block
        const tr = state.tr.delete(start, end)
        tr.setBlockType(start, start, state.schema.nodes.code_block)
        return tr
      },
    )
  })
}

// ---------------------------------------------------------------------------
// Code-aware mark input rules
//
// These re-implement Milkdown's built-in mark input rules with an inline-code
// guard prepended so that typing e.g. `*foo*` inside an inline code span does
// NOT trigger formatting.
// ---------------------------------------------------------------------------

interface CapturedMarkRule {
  group: string | undefined
  fullMatch: string
  start: number
  end: number
}

interface MarkRuleOptions {
  getAttr?: (match: RegExpMatchArray) => Attrs
  updateCaptured?: (captured: CapturedMarkRule) => Partial<CapturedMarkRule>
  beforeDispatch?: (options: { match: RegExpMatchArray, start: number, end: number, tr: Transaction }) => void
}

/**
 * Like `markRule` from `@milkdown/prose` but with an inline-code guard:
 * if the matched range has an `inlineCode` mark (either via storedMarks or
 * on document nodes), the rule returns `null` so the text is left as-is.
 */
function codeAwareMarkRule(
  regexp: RegExp,
  markType: MarkType,
  options: MarkRuleOptions = {},
): InputRule {
  return new InputRule(regexp, (state: EditorState, match: RegExpMatchArray, start: number, end: number) => {
    // Guard: do not fire inside inline code
    const inlineCodeMark = state.schema.marks.inlineCode
    if (inlineCodeMark) {
      if (state.storedMarks?.some(m => m.type === inlineCodeMark))
        return null
      let hasCodeMark = false
      state.doc.nodesBetween(start, end, (node) => {
        if (node.isInline && node.marks.some(m => m.type === inlineCodeMark))
          hasCodeMark = true
      })
      if (hasCodeMark)
        return null
    }

    // --- markRule logic (reimplemented from @milkdown/prose) ---
    const { tr } = state
    const matchLength = match.length

    let group = match[matchLength - 1]
    let fullMatch = match[0]
    let initialStoredMarks: readonly Mark[] = []

    const captured: CapturedMarkRule = { group, fullMatch, start, end }
    const result = options.updateCaptured?.(captured)
    Object.assign(captured, result)
    ;({ group, fullMatch, start, end } = captured)

    if (fullMatch === null)
      return null
    if (group?.trim() === '')
      return null

    if (group) {
      const startSpaces = fullMatch.search(/\S/)
      const textStart = start + fullMatch.indexOf(group)
      const textEnd = textStart + group.length

      initialStoredMarks = tr.storedMarks ?? []

      if (textEnd < end)
        tr.delete(textEnd, end)
      if (textStart > start)
        tr.delete(start + startSpaces, textStart)

      const markEnd = start + startSpaces + group.length
      const attrs = options.getAttr?.(match)
      tr.addMark(start, markEnd, markType.create(attrs))
      tr.setStoredMarks(initialStoredMarks)

      options.beforeDispatch?.({ match, start, end, tr })
    }

    return tr
  })
}

/** Code-aware replacement for Milkdown's `strongInputRule`. */
export function createStrongInputRule() {
  return $inputRule((ctx) => {
    return codeAwareMarkRule(
      // eslint-disable-next-line regexp/no-useless-lazy,regexp/no-useless-assertions -- mirrors Milkdown's original regex
      /(?<![\w:/])(?:\*\*|__)([^*_]+?)(?:\*\*|__)(?![\w/])$/,
      ctx.get(schemaCtx).marks.strong,
      {
        getAttr: match => ({
          marker: match[0].startsWith('*') ? '*' : '_',
        }),
      },
    )
  })
}

/** Code-aware replacement for Milkdown's `emphasisStarInputRule`. */
export function createEmphasisStarInputRule() {
  return $inputRule((ctx) => {
    return codeAwareMarkRule(
      /(?:^|[^*])\*([^*]+)\*$/,
      ctx.get(schemaCtx).marks.emphasis,
      {
        getAttr: () => ({ marker: '*' }),
        updateCaptured: ({ fullMatch, start }) =>
          !fullMatch.startsWith('*')
            ? { fullMatch: fullMatch.slice(1), start: start + 1 }
            : {},
      },
    )
  })
}

/** Code-aware replacement for Milkdown's `emphasisUnderscoreInputRule`. */
export function createEmphasisUnderscoreInputRule() {
  return $inputRule((ctx) => {
    return codeAwareMarkRule(
      /\b_(?![_\s])(.*?[^_\s])_\b/,
      ctx.get(schemaCtx).marks.emphasis,
      {
        getAttr: () => ({ marker: '_' }),
        updateCaptured: ({ fullMatch, start }) =>
          !fullMatch.startsWith('_')
            ? { fullMatch: fullMatch.slice(1), start: start + 1 }
            : {},
      },
    )
  })
}

/** Code-aware replacement for Milkdown's `inlineCodeInputRule`. */
export function createInlineCodeInputRule() {
  return $inputRule((ctx) => {
    return codeAwareMarkRule(
      // eslint-disable-next-line regexp/no-useless-non-capturing-group -- mirrors Milkdown's original regex
      /(?:`)([^`]+)(?:`)$/,
      ctx.get(schemaCtx).marks.inlineCode,
    )
  })
}

/** Code-aware replacement for Milkdown's `strikethroughInputRule`. */
export function createStrikethroughInputRule() {
  return $inputRule((ctx) => {
    return codeAwareMarkRule(
      // eslint-disable-next-line regexp/no-misleading-capturing-group -- mirrors Milkdown's original regex
      /(?<![\w:/])(~{1,2})(.+?)\1(?!\w|\/)/,
      ctx.get(schemaCtx).marks.strike_through,
    )
  })
}
