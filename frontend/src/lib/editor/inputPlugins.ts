import type { PluginRefs } from './keyboardPlugins'

import { parserCtx, schemaCtx, serializerCtx } from '@milkdown/core'
import { DOMParser, DOMSerializer } from '@milkdown/prose/model'
import { Plugin, PluginKey, TextSelection } from '@milkdown/prose/state'
import { $prose } from '@milkdown/utils'

/** Bracket pair map: opening -> closing, closing -> opening (for wrap detection). */
const BRACKET_PAIRS: Record<string, string> = {
  '(': ')',
  ')': '(',
  '[': ']',
  ']': '[',
  '{': '}',
  '}': '{',
}

/** Characters that toggle marks when typed with a selection (outside code_block). */
const MARK_TOGGLE_CHARS: Record<string, string> = {
  '`': 'inlineCode',
  '*': 'strong',
  '_': 'emphasis',
  '~': 'strike_through',
}

/**
 * When text is selected and the user types a bracket or mark-toggle character,
 * wrap the selection with brackets or toggle the corresponding mark.
 */
export function createSelectionWrapPlugin() {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('selection-wrap'),
      props: {
        handleTextInput(view, _from, _to, text) {
          const { state } = view
          const { from, to, empty } = state.selection
          if (empty)
            return false

          const inCodeBlock = state.selection.$from.parent.type.name === 'code_block'

          // Bracket wrapping — works everywhere including code blocks
          if (BRACKET_PAIRS[text]) {
            const open = text === ')' || text === ']' || text === '}' ? BRACKET_PAIRS[text] : text
            const close = BRACKET_PAIRS[open]
            // Insert close bracket first (higher pos) to avoid mapping issues
            const tr = state.tr
            tr.insertText(close, to)
            tr.insertText(open, from)
            // Select the original text between the brackets
            tr.setSelection(TextSelection.create(tr.doc, from + 1, to + 1))
            view.dispatch(tr)
            return true
          }

          // Mark toggles — do NOT work inside code blocks
          const markName = MARK_TOGGLE_CHARS[text]
          if (markName && !inCodeBlock) {
            const markType = state.schema.marks[markName]
            if (!markType)
              return false

            const hasMark = state.doc.rangeHasMark(from, to, markType)
            const tr = state.tr
            if (hasMark) {
              tr.removeMark(from, to, markType)
            }
            else {
              // For inline code, remove other marks first (matches toggleInlineCodeCommand)
              if (markName === 'inlineCode') {
                for (const name of Object.keys(state.schema.marks)) {
                  if (name !== 'inlineCode') {
                    tr.removeMark(from, to, state.schema.marks[name])
                  }
                }
              }
              tr.addMark(from, to, markType.create())
            }
            // Keep selection on the text
            tr.setSelection(TextSelection.create(tr.doc, from, to))
            view.dispatch(tr)
            return true
          }

          return false
        },
      },
    })
  })
}

/**
 * Strip code delimiters (fenced code blocks, inline backticks, surrounding `<br />` tags)
 * from pasted text so that raw content can be inserted into code contexts.
 */
function stripCodeDelimiters(text: string): string {
  let result = text

  // Strip leading <br />\n and trailing \n<br />
  result = result.replace(/^<br \/>\n/, '').replace(/\n<br \/>$/, '')

  // Strip fenced code blocks: ```lang\n...\n``` -> content between fences
  // Handle multiple fences by replacing each pair
  result = result.replace(/^```[^\n]*\n([\s\S]*?)```$/gm, '$1')
  // Trim trailing newline left by fence stripping
  if (result !== text) {
    result = result.replace(/\n$/, '')
  }

  // Strip inline backtick pairs: `content` -> content (including double-backtick syntax)
  result = result.replace(/``([^`].*?)``|`([^`]+)`/g, (_m, g1, g2) => g1 ?? g2)

  return result
}

/**
 * Override paste to always parse plain text as markdown.
 * Milkdown's clipboard plugin only uses its markdown parser when
 * `text/html` is empty, but many apps put HTML in the clipboard
 * even for plain-text content, bypassing the markdown path.
 * This plugin always tries the markdown parser first when the plain
 * text contains markdown block-level syntax.
 */
export function createMarkdownPastePlugin() {
  return $prose((ctx) => {
    const schema = ctx.get(schemaCtx)
    return new Plugin({
      key: new PluginKey('markdown-paste'),
      props: {
        // Override milkdown's clipboardTextSerializer so that inline marks
        // (bold, italic, etc.) are preserved as markdown in text/plain.
        // Milkdown's built-in serializer treats single text nodes as "pure
        // text" even when they have marks, stripping formatting.
        clipboardTextSerializer: (slice) => {
          const serializer = ctx.get(serializerCtx)
          const doc = schema.topNodeType.createAndFill(undefined, slice.content)
          if (!doc)
            return ''
          return serializer(doc)
        },
        handlePaste: (view, event) => {
          const { clipboardData } = event
          if (!clipboardData)
            return false

          const editable = view.props.editable?.(view.state)
          if (!editable)
            return false

          // Inside code blocks or inline code: strip code delimiters from pasted text
          const { $from } = view.state.selection
          const inCodeBlock = $from.parent.type.spec.code
          const codeMarkType = schema.marks.inlineCode
          const inInlineCode = codeMarkType
            && ((view.state.storedMarks ?? $from.marks()).some(m => m.type === codeMarkType))
          if (inCodeBlock || inInlineCode) {
            const plain = clipboardData.getData('text/plain')
            if (!plain)
              return false
            const stripped = stripCodeDelimiters(plain)
            if (stripped === plain)
              return false
            event.preventDefault()
            view.dispatch(view.state.tr.insertText(stripped))
            return true
          }

          const text = clipboardData.getData('text/plain')
          if (!text)
            return false

          // Only intercept if the plain text contains markdown syntax that would
          // be lost in HTML paste. This includes block-level syntax (lists,
          // headings, code fences, blockquotes) and inline formatting (bold,
          // strikethrough, inline code) which may be stripped when the clipboard
          // loses text/html (e.g. headless browsers, cross-app paste).
          const hasMarkdownSyntax
            = /^(?:\s*[-+*]\s|#{1,6}\s|\d+\.\s|```|>\s)/m.test(text)
              || /\*\*\S[^*]*\*\*|__\S[^_]*__|~~\S[^~]*~~|`[^`\n]+`/.test(text)
          if (!hasMarkdownSyntax)
            return false

          try {
            const parser = ctx.get(parserCtx)
            const slice = parser(text)
            if (!slice || typeof slice === 'string')
              return false

            const dom = DOMSerializer.fromSchema(schema).serializeFragment(slice.content)
            const domParser = DOMParser.fromSchema(schema)
            const parsedSlice = domParser.parseSlice(dom)

            view.dispatch(view.state.tr.replaceSelection(parsedSlice))
            return true
          }
          catch {
            return false
          }
        },
      },
    })
  })
}

/**
 * Intercept Enter in empty trailing paragraphs of list items to create
 * a new list item rather than exiting the list (default ProseMirror
 * liftEmptyBlock behaviour).
 */
export function createListItemEnterPlugin(refs: Pick<PluginRefs, 'getEnterMode'>) {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('list-item-enter'),
      props: {
        handleKeyDown: (view, event) => {
          if (event.key !== 'Enter' || event.shiftKey || event.metaKey || event.ctrlKey)
            return false

          // In enter-sends mode, Enter sends the message — let sendPlugin handle it
          if (refs.getEnterMode() === 'enter-sends')
            return false

          const { state } = view
          const { $from, empty } = state.selection
          if (!empty || $from.parent.content.size !== 0)
            return false

          // Find enclosing list_item
          let listItemDepth = -1
          for (let d = $from.depth; d >= 1; d--) {
            if ($from.node(d).type.name === 'list_item') {
              listItemDepth = d
              break
            }
          }
          if (listItemDepth < 0)
            return false

          const listItem = $from.node(listItemDepth)
          const childIndex = $from.index(listItemDepth)

          // Only act on the last child paragraph that isn't the first child
          if (childIndex === 0 || childIndex !== listItem.childCount - 1)
            return false

          // Delete the empty trailing paragraph and insert a new list_item after
          const paragraphStart = $from.before($from.depth)
          const paragraphEnd = $from.after($from.depth)
          const tr = state.tr.delete(paragraphStart, paragraphEnd)

          const afterListItem = tr.mapping.map($from.after(listItemDepth))
          const newItem = state.schema.nodes.list_item.create(
            null,
            state.schema.nodes.paragraph.create(),
          )
          tr.insert(afterListItem, newItem)
          // Position cursor inside the new list_item's paragraph: afterListItem + 1 (list_item open) + 1 (paragraph open)
          tr.setSelection(TextSelection.create(tr.doc, afterListItem + 2))
          view.dispatch(tr)
          return true
        },
      },
    })
  })
}

/**
 * Prevent typing at the end of a link from extending the link mark.
 *
 * Milkdown's link mark is inclusive by default (ProseMirror default),
 * so typing at the trailing edge of a link extends the mark to cover
 * the new text. This plugin uses appendTransaction to detect when the
 * cursor is at the right boundary of a link and removes the link from
 * stored marks, so subsequent typing is not linked.
 */
export function createLinkBoundaryPlugin() {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('link-boundary'),
      appendTransaction(_transactions, _oldState, newState) {
        const { selection } = newState
        if (!selection.empty)
          return null

        const linkMarkType = newState.schema.marks.link
        if (!linkMarkType)
          return null

        // Check if cursor has the link mark via stored marks or position marks
        const marks = newState.storedMarks ?? selection.$from.marks()
        if (!marks.some(m => m.type === linkMarkType))
          return null

        // Check if we're at the right boundary of a link:
        // nodeBefore has link mark, but nodeAfter does not
        const { $from } = selection
        const nodeBefore = $from.nodeBefore
        const nodeAfter = $from.nodeAfter
        if (!nodeBefore || !nodeBefore.marks.some(m => m.type === linkMarkType))
          return null
        if (nodeAfter && nodeAfter.marks.some(m => m.type === linkMarkType))
          return null

        // At the trailing edge of a link — remove stored link mark
        return newState.tr.removeStoredMark(linkMarkType)
      },
    })
  })
}
