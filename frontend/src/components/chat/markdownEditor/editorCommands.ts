import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import { editorViewCtx } from '@milkdown/core'
import { createCodeBlockCommand, toggleInlineCodeCommand } from '@milkdown/preset-commonmark'
import { TextSelection } from '@milkdown/prose/state'
import { callCommand } from '@milkdown/utils'

/**
 * Submit the link popover form: apply a link mark to the current selection,
 * or insert the URL as new text and apply a link mark to it when the
 * selection is empty.
 */
export function applyLinkSubmit(
  editor: Editor | undefined,
  href: string,
  closePopover: () => void,
): void {
  const trimmed = href.trim()
  if (!trimmed || !editor) {
    closePopover()
    return
  }
  editor.action((ctx: Ctx) => {
    const view = ctx.get(editorViewCtx)
    const { state } = view
    const { from, to } = state.selection
    const linkMark = state.schema.marks.link.create({ href: trimmed })
    if (from === to) {
      const tr = state.tr
        .insertText(trimmed, from)
        .addMark(from, from + trimmed.length, linkMark)
        .removeStoredMark(state.schema.marks.link)
      view.dispatch(tr)
    }
    else {
      const tr = state.tr
        .addMark(from, to, linkMark)
        .removeStoredMark(state.schema.marks.link)
      view.dispatch(tr)
    }
    view.focus()
  })
  closePopover()
}

/** Remove the link mark spanning the current selection. */
export function removeLinkAtSelection(editor: Editor | undefined): void {
  if (!editor)
    return
  editor.action((ctx: Ctx) => {
    const view = ctx.get(editorViewCtx)
    const { state } = view
    const { from, to, $from } = state.selection
    const linkMark = $from.marks().find(m => m.type.name === 'link')
    if (linkMark) {
      let linkStart = from
      let linkEnd = to
      state.doc.nodesBetween($from.start(), from, (node, pos) => {
        if (node.isText && linkMark.isInSet(node.marks)) {
          linkStart = pos
        }
      })
      state.doc.nodesBetween(from, $from.end(), (node, pos) => {
        if (node.isText && linkMark.isInSet(node.marks)) {
          linkEnd = pos + node.nodeSize
        }
      })
      const tr = state.tr.removeMark(linkStart, linkEnd, state.schema.marks.link)
      view.dispatch(tr)
    }
    view.focus()
  })
}

/**
 * Toggle a code block at the current selection.  Behavior:
 *  - Inside an empty code block: convert it back to a paragraph.
 *  - Inside a list item: insert a new code block after the paragraph (do not
 *    transform the list item itself).
 *  - Otherwise: dispatch Milkdown's createCodeBlock command.
 */
export function toggleCodeBlock(
  editor: Editor | undefined,
  focusEditor: () => void,
): void {
  if (!editor)
    return

  let inCodeBlock = false
  try {
    editor.action((ctx: Ctx) => {
      const view = ctx.get(editorViewCtx)
      const { state } = view
      const { $from } = state.selection
      if ($from.parent.type.name === 'code_block') {
        inCodeBlock = true
        if ($from.parent.content.size === 0) {
          const pos = $from.before($from.depth)
          const tr = state.tr.setNodeMarkup(pos, state.schema.nodes.paragraph)
          view.dispatch(tr)
        }
        view.focus()
      }
    })
  }
  catch {
    // ignore
  }
  if (inCodeBlock)
    return

  let listItemDepth = -1
  try {
    editor.action((ctx: Ctx) => {
      const view = ctx.get(editorViewCtx)
      const { $from } = view.state.selection
      for (let d = $from.depth; d >= 1; d--) {
        if ($from.node(d).type.name === 'list_item') {
          listItemDepth = d
          break
        }
      }
    })
  }
  catch {
    // ignore
  }
  if (listItemDepth < 0) {
    editor.action(callCommand(createCodeBlockCommand.key))
    focusEditor()
    return
  }
  editor.action((ctx: Ctx) => {
    const view = ctx.get(editorViewCtx)
    const { state } = view
    const { $from } = state.selection
    const codeBlock = state.schema.nodes.code_block.create()
    const afterParagraph = $from.after($from.depth)
    const tr = state.tr.insert(afterParagraph, codeBlock)
    tr.setSelection(TextSelection.create(tr.doc, afterParagraph + 1))
    view.dispatch(tr)
    view.focus()
  })
}

/**
 * Toggle the inline code mark at the current selection.  Handles two cases:
 *   1. Range selection: delegate to Milkdown's toggle command.
 *   2. Empty selection: toggle the stored mark; when removing, also strip the
 *      mark from the surrounding code-marked range so the cursor exits cleanly.
 */
export function toggleInlineCode(editor: Editor | undefined): void {
  if (!editor)
    return
  editor.action((ctx: Ctx) => {
    const view = ctx.get(editorViewCtx)
    const { state } = view
    const { from, empty } = state.selection

    if (!empty) {
      editor.action(callCommand(toggleInlineCodeCommand.key))
      view.focus()
      return
    }

    const codeMarkType = state.schema.marks.inlineCode
    if (!codeMarkType)
      return

    const marks = state.storedMarks ?? state.selection.$from.marks()
    const hasCode = marks.some(m => m.type === codeMarkType)

    if (hasCode) {
      const $from = state.selection.$from
      const parent = $from.parent
      const parentStart = $from.start($from.depth)
      let rangeFrom = -1
      let rangeTo = -1
      let found = false

      parent.forEach((child, offset) => {
        if (found)
          return
        const childStart = parentStart + offset
        const childEnd = childStart + child.nodeSize
        if (child.isText && codeMarkType.isInSet(child.marks)) {
          if (rangeFrom < 0)
            rangeFrom = childStart
          rangeTo = childEnd
        }
        else {
          if (rangeFrom >= 0 && from >= rangeFrom && from <= rangeTo) {
            found = true
            return
          }
          rangeFrom = -1
          rangeTo = -1
        }
      })
      if (!found && rangeFrom >= 0 && from >= rangeFrom && from <= rangeTo) {
        found = true
      }

      const tr = state.tr
      if (found && rangeTo - rangeFrom > 0) {
        tr.removeMark(rangeFrom, rangeTo, codeMarkType)
      }
      tr.removeStoredMark(codeMarkType)
      view.dispatch(tr)
    }
    else {
      const tr = state.tr.addStoredMark(codeMarkType.create())
      view.dispatch(tr)
    }
    view.focus()
  })
}

/**
 * Apply a language attribute to the code block at the given position.
 * Closes the language popover after applying.
 */
export function applyCodeBlockLanguage(
  editor: Editor | undefined,
  pos: number,
  langId: string,
  closePopover: () => void,
): void {
  if (editor && pos >= 0) {
    editor.action((ctx: Ctx) => {
      const view = ctx.get(editorViewCtx)
      const { state } = view
      const node = state.doc.nodeAt(pos)
      if (node && node.type.name === 'code_block') {
        const tr = state.tr.setNodeMarkup(pos, undefined, { ...node.attrs, language: langId || null })
        view.dispatch(tr)
      }
      view.focus()
    })
  }
  closePopover()
}
