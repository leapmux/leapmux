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
      const codeBlockType = state.schema.nodes.code_block
      if (!codeBlockType) {
        console.error('editor schema has no code_block node; cannot set a language')
        return
      }
      // The position was resolved against an EARLIER document: a draft swap
      // replaces the whole document, and `nodeAt` past the end throws.
      if (pos >= state.doc.content.size)
        return
      const node = state.doc.nodeAt(pos)
      // Compared by resolved TYPE, not by name string. A name literal that stops
      // matching fails silently -- the user picks a language, the popover
      // closes, and the block keeps none.
      if (node && node.type === codeBlockType) {
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
  range: { from: number, to: number, attrs?: Record<string, unknown> },
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
    // The range was resolved against an EARLIER document. The persistent editor
    // replaces its whole document on a draft swap, so a popover left open across
    // one holds positions that no longer describe anything: past the end they
    // throw out of the submit handler, and inside it they would write the href
    // onto unrelated text.
    if (range.to > state.doc.content.size || range.from >= range.to)
      return
    // Carry the mark's other attributes. The schema declares `title` beside
    // `href`, so rebuilding from the href alone silently dropped the title of a
    // link pasted as `[docs](url "The docs")`.
    view.dispatch(
      state.tr
        .removeMark(range.from, range.to, linkType)
        .addMark(range.from, range.to, linkType.create({ ...range.attrs, href: trimmed }))
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
