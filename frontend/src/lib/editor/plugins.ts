import { parserCtx, schemaCtx, serializerCtx } from '@milkdown/core'
import { lift, wrapIn } from '@milkdown/prose/commands'
import { DOMParser, DOMSerializer } from '@milkdown/prose/model'
import { liftListItem, sinkListItem } from '@milkdown/prose/schema-list'
import { Plugin, PluginKey, TextSelection } from '@milkdown/prose/state'
import { Decoration, DecorationSet } from '@milkdown/prose/view'
import { $prose } from '@milkdown/utils'

/** Shared refs accessed by plugins via getter functions (closures over mutable refs). */
export interface PluginRefs {
  getDisabled: () => boolean
  getEnterMode: () => 'enter-sends' | 'cmd-enter-sends'
  getPlaceholder: () => string
  onSend: () => void
}

/** Shows placeholder text when the editor is empty. */
export function createPlaceholderPlugin(refs: Pick<PluginRefs, 'getDisabled' | 'getPlaceholder'>) {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('placeholder'),
      props: {
        decorations(state) {
          const doc = state.doc
          if (doc.childCount === 1 && doc.firstChild?.isTextblock && doc.firstChild.content.size === 0) {
            return DecorationSet.create(doc, [
              Decoration.node(0, doc.content.size, {
                'class': 'is-editor-empty',
                'data-placeholder': refs.getDisabled() ? 'Connection to the agent was lost.' : refs.getPlaceholder(),
              }),
            ])
          }
          return DecorationSet.empty
        },
      },
    })
  })
}

/** Sends message on Enter (default mode) or Cmd/Ctrl+Enter (alt mode). */
export function createSendOnEnterPlugin(refs: Pick<PluginRefs, 'getDisabled' | 'getEnterMode' | 'onSend'>) {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('send-on-enter'),
      props: {
        handleKeyDown: (view, event) => {
          if (refs.getDisabled())
            return false

          if (refs.getEnterMode() === 'enter-sends') {
            if (event.key === 'Enter' && !event.shiftKey && !event.metaKey && !event.ctrlKey) {
              // Don't intercept Enter if the current line looks like a code fence
              // or horizontal rule so that input rules can fire
              const { state } = view
              const { $from } = state.selection
              const textBefore = $from.parent.textContent.slice(0, $from.parentOffset)
              if (/^```\w*$/.test(textBefore) || /^---$/.test(textBefore)) {
                return false
              }
              event.preventDefault()
              refs.onSend()
              return true
            }
          }
          else {
            if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
              event.preventDefault()
              refs.onSend()
              return true
            }
          }
          return false
        },
      },
    })
  })
}

/** Handles Backspace at the start of a blockquote to lift content out, or converts empty code_block to paragraph. */
export function createBlockquoteBackspacePlugin() {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('blockquote-backspace'),
      props: {
        handleKeyDown: (view, event) => {
          if (event.key !== 'Backspace')
            return false
          const { state } = view
          const { $from, empty } = state.selection
          if (!empty || $from.parentOffset !== 0)
            return false
          // Empty code_block: convert to paragraph on Backspace
          if ($from.parent.type.name === 'code_block' && $from.parent.content.size === 0) {
            const pos = $from.before($from.depth)
            const tr = state.tr.setNodeMarkup(pos, state.schema.nodes.paragraph)
            view.dispatch(tr)
            return true
          }
          // Empty paragraph right after an HR: revert to "---" text
          if ($from.parent.type.name === 'paragraph' && $from.parent.content.size === 0) {
            const parentIndex = $from.index($from.depth - 1)
            if (parentIndex > 0) {
              const grandParent = $from.node($from.depth - 1)
              const prevNode = grandParent.child(parentIndex - 1)
              if (prevNode.type.name === 'hr') {
                // Delete both the HR and the empty paragraph, replace with a paragraph containing "---"
                const hrPos = $from.before($from.depth) - 1 // HR is 1 node size, positioned right before the paragraph
                const paraEnd = $from.after($from.depth)
                const dashText = state.schema.text('---')
                const dashPara = state.schema.nodes.paragraph.create(null, dashText)
                const tr = state.tr.replaceWith(hrPos, paraEnd, dashPara)
                // Place cursor at the end of "---" inside the new paragraph.
                // The new paragraph starts at hrPos, so content starts at hrPos+1,
                // and the end of "---" (3 chars) is at hrPos+1+3 = hrPos+4.
                // Use tr.mapping to map the original hrPos through the replace step
                const mappedHrPos = tr.mapping.map(hrPos)
                const endPos = mappedHrPos + 1 + dashPara.content.size
                tr.setSelection(TextSelection.create(tr.doc, endPos)).scrollIntoView()
                view.dispatch(tr)
                return true
              }
            }
          }
          // Walk up to find a blockquote ancestor
          for (let d = $from.depth; d >= 1; d--) {
            if ($from.node(d).type.name === 'blockquote') {
              return lift(state, view.dispatch)
            }
          }
          return false
        },
      },
    })
  })
}

/**
 * Unified Tab / Shift+Tab handler.
 * Context priority: code_block > list_item > blockquote > heading > paragraph
 */
export function createTabKeyPlugin(refs: { onShiftTabInParagraph: () => void }) {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('tab-key'),
      props: {
        handleKeyDown: (view, event) => {
          if (event.key !== 'Tab' || event.metaKey || event.ctrlKey || event.altKey)
            return false

          const { state } = view
          const { $from } = state.selection
          const isShift = event.shiftKey
          const tabWidth = 2

          // 1. Code block (immediate parent)
          if ($from.parent.type.name === 'code_block') {
            event.preventDefault()
            const offset = $from.parentOffset
            const text = $from.parent.textContent
            const lineStart = text.lastIndexOf('\n', offset - 1) + 1
            const cursorColumn = offset - lineStart
            const blockStart = $from.start($from.depth)

            if (!isShift) {
              // Insert spaces to reach next tab stop
              const spacesToInsert = tabWidth - (cursorColumn % tabWidth)
              const tr = state.tr.insertText(' '.repeat(spacesToInsert), state.selection.from)
              view.dispatch(tr)
            }
            else {
              // Remove leading spaces to snap to previous tab stop
              let leadingSpaces = 0
              for (let i = lineStart; i < text.length && text[i] === ' '; i++) {
                leadingSpaces++
              }
              if (leadingSpaces > 0) {
                const prevStop = leadingSpaces - (leadingSpaces % tabWidth || tabWidth)
                const spacesToRemove = leadingSpaces - prevStop
                if (spacesToRemove > 0) {
                  const deleteFrom = blockStart + lineStart
                  const deleteTo = deleteFrom + spacesToRemove
                  const tr = state.tr.delete(deleteFrom, deleteTo)
                  view.dispatch(tr)
                }
              }
            }
            return true
          }

          // 2. List item (nearest ancestor)
          let listItemDepth = -1
          for (let d = $from.depth; d >= 1; d--) {
            if ($from.node(d).type.name === 'list_item') {
              listItemDepth = d
              break
            }
          }
          if (listItemDepth >= 0) {
            event.preventDefault()
            const listItemType = state.schema.nodes.list_item
            if (!isShift) {
              sinkListItem(listItemType)(state, view.dispatch)
            }
            else {
              liftListItem(listItemType)(state, view.dispatch)
            }
            return true
          }

          // 3. Blockquote (nearest ancestor)
          for (let d = $from.depth; d >= 1; d--) {
            if ($from.node(d).type.name === 'blockquote') {
              event.preventDefault()
              if (!isShift) {
                wrapIn(state.schema.nodes.blockquote)(state, view.dispatch)
              }
              else {
                lift(state, view.dispatch)
              }
              return true
            }
          }

          // 4. Heading (immediate parent)
          if ($from.parent.type.name === 'heading') {
            event.preventDefault()
            const level = $from.parent.attrs.level as number
            const pos = $from.before($from.depth)
            if (!isShift) {
              if (level < 6) {
                const tr = state.tr.setNodeMarkup(pos, undefined, { ...$from.parent.attrs, level: level + 1 })
                view.dispatch(tr)
              }
            }
            else {
              if (level > 1) {
                const tr = state.tr.setNodeMarkup(pos, undefined, { ...$from.parent.attrs, level: level - 1 })
                view.dispatch(tr)
              }
              else {
                const tr = state.tr.setNodeMarkup(pos, state.schema.nodes.paragraph)
                view.dispatch(tr)
              }
            }
            return true
          }

          // 5. Plain paragraph — Tab converts to H1, Shift+Tab toggles plan mode
          if ($from.parent.type.name === 'paragraph') {
            event.preventDefault()
            if (!isShift) {
              const pos = $from.before($from.depth)
              const tr = state.tr.setNodeMarkup(pos, state.schema.nodes.heading, { level: 1 })
              view.dispatch(tr)
            }
            else {
              refs.onShiftTabInParagraph()
            }
            return true
          }

          return false
        },
      },
    })
  })
}

/** In a code block, Backspace deletes back to the previous tab stop when the characters are all spaces. */
export function createCodeBlockBackspacePlugin() {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('code-block-backspace'),
      props: {
        handleKeyDown: (view, event) => {
          if (event.key !== 'Backspace' || event.shiftKey || event.metaKey || event.ctrlKey || event.altKey)
            return false
          const { state } = view
          const { $from, empty } = state.selection
          if (!empty || $from.parent.type.name !== 'code_block')
            return false

          const tabWidth = 2
          const offset = $from.parentOffset
          if (offset === 0)
            return false

          const text = $from.parent.textContent
          const lineStart = text.lastIndexOf('\n', offset - 1) + 1
          const cursorColumn = offset - lineStart

          if (cursorColumn === 0)
            return false

          // How many chars back to previous tab stop?
          const charsToStop = cursorColumn % tabWidth || tabWidth
          // All characters from previous tab stop to cursor must be spaces
          for (let i = 0; i < charsToStop; i++) {
            if (text[offset - 1 - i] !== ' ')
              return false
          }

          event.preventDefault()
          const from = state.selection.from - charsToStop
          const tr = state.tr.delete(from, state.selection.from)
          view.dispatch(tr)
          return true
        },
      },
    })
  })
}

/**
 * When the cursor is on the last line of a code_block that is the last
 * child of its parent, Down-arrow inserts an empty paragraph sibling
 * so the user can escape the code block.
 */
export function createCodeBlockEscapePlugin() {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('code-block-escape'),
      props: {
        handleKeyDown: (view, event) => {
          if (event.key !== 'ArrowDown')
            return false
          if (event.shiftKey || event.metaKey || event.ctrlKey || event.altKey)
            return false

          const { state } = view
          const { $from, empty } = state.selection
          if (!empty || $from.parent.type.name !== 'code_block')
            return false

          // Cursor must be on the last line (no newline after cursor)
          const textAfter = $from.parent.textContent.slice($from.parentOffset)
          if (textAfter.includes('\n'))
            return false

          // Code block must be the last child of its parent
          const parentDepth = $from.depth - 1
          const parent = $from.node(parentDepth)
          if ($from.index(parentDepth) !== parent.childCount - 1)
            return false

          event.preventDefault()
          const afterCodeBlock = $from.after($from.depth)
          const para = state.schema.nodes.paragraph.create()
          const tr = state.tr.insert(afterCodeBlock, para)
          tr.setSelection(TextSelection.create(tr.doc, afterCodeBlock + 1))
          view.dispatch(tr)
          return true
        },
      },
    })
  })
}

/**
 * Suppress macOS double-space-to-period text substitution.
 * macOS bypasses keydown entirely and sends a beforeinput with
 * inputType "insertText" and data ". " — we intercept this and
 * insert a space instead of the period+space.
 */
export function createSuppressTextSubstitutionPlugin() {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('suppress-text-substitution'),
      props: {
        handleDOMEvents: {
          beforeinput(view, event) {
            if (event.inputType === 'insertText' && event.data === '. ') {
              event.preventDefault()
              view.dispatch(view.state.tr.insertText(' '))
              return true
            }
            return false
          },
        },
      },
    })
  })
}

/**
 * Fix Delete key at the start of a list item's text — delete the next
 * character instead of unwrapping the list item into a paragraph.
 */
export function createListDeleteFixPlugin() {
  return $prose(() => {
    return new Plugin({
      key: new PluginKey('list-delete-fix'),
      props: {
        handleKeyDown: (view, event) => {
          if (event.key !== 'Delete' || event.shiftKey || event.metaKey || event.ctrlKey || event.altKey)
            return false

          const { state } = view
          const { $from, empty } = state.selection
          if (!empty || $from.parentOffset !== 0)
            return false

          // Check if we're inside a list_item
          let inListItem = false
          for (let d = $from.depth; d >= 1; d--) {
            if ($from.node(d).type.name === 'list_item') {
              inListItem = true
              break
            }
          }
          if (!inListItem)
            return false

          // If there's text content ahead, perform a normal forward delete
          if ($from.parent.content.size > 0) {
            event.preventDefault()
            const tr = state.tr.delete($from.pos, $from.pos + 1)
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
 * Find the contiguous range of `inlineCode`-marked text containing `pos`
 * within the same parent text block. Returns `null` if the position is not
 * inside a code span.
 */
function findCodeMarkRange(
  state: import('@milkdown/prose/state').EditorState,
  pos: number,
  codeMarkType: import('@milkdown/prose/model').MarkType,
): { from: number, to: number } | null {
  const $pos = state.doc.resolve(pos)
  const parent = $pos.parent
  const parentStart = $pos.start($pos.depth)

  let rangeFrom = -1
  let rangeTo = -1
  let found = false

  parent.forEach((child, offset) => {
    if (found) {
      return
    }
    const childStart = parentStart + offset
    const childEnd = childStart + child.nodeSize
    if (child.isText && codeMarkType.isInSet(child.marks)) {
      // Start or extend a range
      if (rangeFrom < 0) {
        rangeFrom = childStart
      }
      rangeTo = childEnd
      // Check if pos falls inside this range
      if (pos >= rangeFrom && pos <= rangeTo) {
        // Continue to extend
      }
    }
    else {
      // Non-code node: if we had a range that contains pos, we found it
      if (rangeFrom >= 0 && pos >= rangeFrom && pos <= rangeTo) {
        found = true
        return
      }
      // Reset for next potential code span
      rangeFrom = -1
      rangeTo = -1
    }
  })

  if (!found && rangeFrom >= 0 && pos >= rangeFrom && pos <= rangeTo) {
    found = true
  }

  return found ? { from: rangeFrom, to: rangeTo } : null
}

/**
 * Handle ArrowRight/ArrowLeft at inlineCode mark boundaries so the user
 * can move in and out of code spans predictably.
 *
 * ProseMirror's inlineCode mark is "inclusive" (default), meaning at the
 * right boundary of a code span, `$from.marks()` includes the code mark.
 * This makes it impossible for the user to type plain text right after a
 * code span without explicitly exiting it.
 *
 * This plugin intercepts arrow keys at code/plain boundaries:
 *
 * - ArrowRight at the right edge of code span: exit code (remove stored mark)
 *   so subsequent typing is plain text.
 *
 * - ArrowLeft arriving at the right boundary from plain text: stay outside code
 *   (remove stored mark) so typing inserts plain text.
 *
 * - ArrowLeft when already at the right boundary with stored marks cleared
 *   (outside code): toggle back to inside code at the same position (add
 *   stored code mark), so the next ArrowLeft moves normally within the code span.
 *
 * - ArrowLeft at the left boundary of a code span (inside code): toggle to
 *   outside code at the same position, so the next ArrowLeft moves into
 *   plain text.
 *
 * Also handles:
 * - Backspace/Delete: When the last character of a code span is deleted,
 *   automatically clear the inlineCode stored mark so the cursor exits code mode.
 * - Mod-e: Toggle inlineCode mark with empty selection (add/remove stored mark).
 *
 * State tracking:
 * - 'outside': cursor is at a boundary but explicitly outside code
 * - null: default behavior
 */
export function createCodeSpanEscapePlugin() {
  const pluginKey = new PluginKey('code-span-escape')

  return $prose(() => {
    return new Plugin({
      key: pluginKey,
      state: {
        init: () => null as string | null,
        apply: (tr, value) => {
          const meta = tr.getMeta(pluginKey)
          if (meta !== undefined)
            return meta
          // Any doc change or selection change resets the state
          if (tr.docChanged || tr.selectionSet)
            return null
          return value
        },
      },
      props: {
        handleKeyDown: (view, event) => {
          const { state } = view
          const { $from, empty } = state.selection

          // --- Backspace/Delete: clear code mark when deleting last char ---
          const isBackspaceOrDelete = (event.key === 'Backspace' || event.key === 'Delete')
            && !event.shiftKey && !event.metaKey && !event.ctrlKey && !event.altKey
            && empty
          if (isBackspaceOrDelete) {
            const codeMarkType = state.schema.marks.inlineCode
            if (!codeMarkType)
              return false

            // Cursor must have the inlineCode mark
            const marks = state.storedMarks ?? $from.marks()
            if (!marks.some(m => m.type === codeMarkType))
              return false

            const range = findCodeMarkRange(state, $from.pos, codeMarkType)
            if (!range)
              return false

            // Only intercept when exactly 1 character remains in the code span
            if (range.to - range.from !== 1)
              return false

            // For Backspace, cursor must be at the right end of the range
            // For Delete, cursor must be at the left end of the range
            if (event.key === 'Backspace' && $from.pos !== range.to)
              return false
            if (event.key === 'Delete' && $from.pos !== range.from)
              return false

            event.preventDefault()
            const tr = state.tr.delete(range.from, range.to)
            tr.removeStoredMark(codeMarkType)
            tr.setMeta(pluginKey, null)
            view.dispatch(tr)
            return true
          }

          // --- Mod-e: toggle inlineCode with empty selection ---
          const isModE = event.key === 'e'
            && (event.metaKey || event.ctrlKey)
            && !event.shiftKey && !event.altKey
          if (isModE) {
            if (!empty)
              return false

            const codeMarkType = state.schema.marks.inlineCode
            if (!codeMarkType)
              return false

            const marks = state.storedMarks ?? $from.marks()
            const hasCode = marks.some(m => m.type === codeMarkType)

            if (hasCode) {
              // Inside code: remove the code mark
              const range = findCodeMarkRange(state, $from.pos, codeMarkType)
              const tr = state.tr
              if (range && range.to - range.from > 0) {
                tr.removeMark(range.from, range.to, codeMarkType)
              }
              tr.removeStoredMark(codeMarkType)
              tr.setMeta(pluginKey, null)
              view.dispatch(tr)
            }
            else {
              // Outside code: add code mark
              const tr = state.tr.addStoredMark(codeMarkType.create())
              tr.setMeta(pluginKey, null)
              view.dispatch(tr)
            }

            event.preventDefault()
            return true
          }

          // --- Arrow key handling ---
          const isArrowKey = event.key === 'ArrowRight' || event.key === 'ArrowLeft'
          if (!isArrowKey || event.shiftKey || event.metaKey || event.ctrlKey || event.altKey)
            return false

          if (!empty)
            return false

          const codeMarkType = state.schema.marks.inlineCode
          if (!codeMarkType)
            return false

          const pos = $from.pos
          const $pos = state.doc.resolve(pos)

          const pluginState = pluginKey.getState(state)

          // Helper: check if a position is a code/plain right boundary
          // (nodeBefore has inlineCode, nodeAfter doesn't)
          const isCodeRightBoundary = (checkPos: number) => {
            if (checkPos < 1)
              return false
            const $check = state.doc.resolve(checkPos)
            const nb = $check.nodeBefore
            if (!nb || !nb.marks.some(m => m.type.name === 'inlineCode'))
              return false
            const na = $check.nodeAfter
            if (na && na.marks.some(m => m.type.name === 'inlineCode'))
              return false
            return true
          }

          // Helper: check if a position is a code/plain left boundary
          // (nodeAfter has inlineCode, nodeBefore exists but doesn't have it).
          // Returns false if nodeBefore is null (start of block) since there's
          // no plain text to transition into.
          const isCodeLeftBoundary = (checkPos: number) => {
            const $check = state.doc.resolve(checkPos)
            const na = $check.nodeAfter
            if (!na || !na.marks.some(m => m.type.name === 'inlineCode'))
              return false
            const nb = $check.nodeBefore
            if (!nb)
              return false
            if (nb.marks.some(m => m.type.name === 'inlineCode'))
              return false
            return true
          }

          // At a right boundary, check if we're effectively "outside" code:
          // either the plugin state is 'outside', or storedMarks explicitly
          // excludes inlineCode while $from.marks() includes it (e.g., after
          // the backtick input rule which sets storedMarks to []).
          const effectivelyOutside = pluginState === 'outside'
            || (state.storedMarks !== null
              && !state.storedMarks.some(m => m.type.name === 'inlineCode')
              && $from.marks().some(m => m.type.name === 'inlineCode'))

          // === ArrowRight ===
          if (event.key === 'ArrowRight') {
            if (pluginState === 'outside') {
              // At the left boundary "outside": toggle back to inside code
              const na = $pos.nodeAfter
              if (na && na.marks.some(m => m.type.name === 'inlineCode')) {
                event.preventDefault()
                const tr = state.tr
                tr.addStoredMark(codeMarkType.create())
                tr.setMeta(pluginKey, null)
                view.dispatch(tr)
                return true
              }
              // At the right boundary "outside": clear state, let default proceed
              const tr = state.tr.setMeta(pluginKey, null)
              view.dispatch(tr)
              return false
            }

            // At the left boundary from plain text (no code marks):
            // create a virtual stop and toggle to inside code.
            const isAtLeftBoundaryFromPlain = isCodeLeftBoundary(pos)
              && !$from.marks().some(m => m.type.name === 'inlineCode')
            if (isAtLeftBoundaryFromPlain) {
              event.preventDefault()
              const tr = state.tr
              tr.addStoredMark(codeMarkType.create())
              tr.setMeta(pluginKey, null)
              view.dispatch(tr)
              return true
            }

            // Check if cursor currently has the inlineCode mark
            const codeMark = $from.marks().find(m => m.type.name === 'inlineCode')
            if (!codeMark)
              return false

            // Check if we're at the right boundary: node after cursor
            // does NOT have the code mark (or nothing after)
            const nodeAfter = $pos.nodeAfter
            if (nodeAfter && codeMark.isInSet(nodeAfter.marks))
              return false

            // Exit the code mark — stay at the same position, remove stored mark
            event.preventDefault()
            const tr = state.tr.setSelection(TextSelection.create(state.doc, pos))
            tr.removeStoredMark(codeMarkType)
            tr.setMeta(pluginKey, 'outside')
            view.dispatch(tr)
            return true
          }

          // === ArrowLeft ===

          // If effectively "outside" at the right boundary, toggle to
          // inside code at the same position (no cursor movement).
          if (effectivelyOutside && isCodeRightBoundary(pos)) {
            event.preventDefault()
            const tr = state.tr
            tr.addStoredMark(codeMarkType.create())
            tr.setMeta(pluginKey, null)
            view.dispatch(tr)
            return true
          }

          // If at the left boundary of a code span and explicitly inside
          // code (storedMarks includes inlineCode), toggle to outside code
          // so the next ArrowLeft moves into plain text.
          // Only fires when storedMarks was explicitly set (e.g., by the
          // pos-1 left boundary handler or ArrowRight inside toggle),
          // preventing false triggers after typing at the boundary.
          const isInsideAtLeftBoundary = pluginState !== 'outside'
            && isCodeLeftBoundary(pos)
            && state.storedMarks !== null
            && state.storedMarks.some(m => m.type.name === 'inlineCode')
          if (isInsideAtLeftBoundary) {
            event.preventDefault()
            const tr = state.tr.setSelection(TextSelection.create(state.doc, pos))
            tr.removeStoredMark(codeMarkType)
            tr.setMeta(pluginKey, 'outside')
            view.dispatch(tr)
            return true
          }

          // Moving left from inside code toward the left boundary:
          // if pos-1 is a left boundary and cursor currently has inlineCode,
          // move there and explicitly set stored marks to stay in code.
          const isMovingToLeftBoundary = isCodeLeftBoundary(pos - 1)
            && $from.marks().some(m => m.type.name === 'inlineCode')
          if (isMovingToLeftBoundary) {
            event.preventDefault()
            const tr = state.tr.setSelection(TextSelection.create(state.doc, pos - 1))
            tr.addStoredMark(codeMarkType.create())
            tr.setMeta(pluginKey, null)
            view.dispatch(tr)
            return true
          }

          // If pos-1 is a right boundary (ArrowLeft would land there from
          // plain text), move there and set "outside" mode in one step.
          if (isCodeRightBoundary(pos - 1)) {
            event.preventDefault()
            const tr = state.tr.setSelection(TextSelection.create(state.doc, pos - 1))
            tr.removeStoredMark(codeMarkType)
            tr.setMeta(pluginKey, 'outside')
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

          // Don't handle paste inside code blocks
          const currentNode = view.state.selection.$from.node()
          if (currentNode.type.spec.code)
            return false

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
