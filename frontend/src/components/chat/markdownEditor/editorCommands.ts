import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import { editorViewCtx } from '@milkdown/core'

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

/**
 * Replace the href on an existing link run.
 *
 * Removes the old mark before adding the new one: `addMark` alone leaves the
 * original mark in the set for a different attribute value, and the serializer
 * then emits whichever it finds first.
 */
export function applyLinkHref(
  editor: Editor | undefined,
  range: { from: number, to: number },
  href: string,
): void {
  const trimmed = href.trim()
  if (!editor || !trimmed)
    return
  editor.action((ctx: Ctx) => {
    const view = ctx.get(editorViewCtx)
    const { state } = view
    const linkType = state.schema.marks.link
    if (!linkType)
      return
    view.dispatch(
      state.tr
        .removeMark(range.from, range.to, linkType)
        .addMark(range.from, range.to, linkType.create({ href: trimmed }))
        .removeStoredMark(linkType),
    )
    view.focus()
  })
}

/**
 * Strip the link mark from a run, leaving its text.
 *
 * `removeStoredMark` matters as much as the range: the link mark is inclusive,
 * so without it the mark stays in the stored set and the next character the user
 * types re-applies the URL they just removed.
 */
export function removeLinkRange(
  editor: Editor | undefined,
  range: { from: number, to: number },
): void {
  if (!editor)
    return
  editor.action((ctx: Ctx) => {
    const view = ctx.get(editorViewCtx)
    const { state } = view
    const linkType = state.schema.marks.link
    if (!linkType)
      return
    view.dispatch(
      state.tr
        .removeMark(range.from, range.to, linkType)
        .removeStoredMark(linkType),
    )
    view.focus()
  })
}
