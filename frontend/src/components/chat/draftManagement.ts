import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import { editorViewCtx, serializerCtx } from '@milkdown/core'
import { TextSelection } from '@milkdown/prose/state'
import { clearDraft, saveDraft } from '~/lib/editor/draftPersistence'
import { safeGetString, safeSetString } from '~/lib/safeStorage'

export type EnterKeyMode = 'enter-sends' | 'cmd-enter-sends'

export const ENTER_KEY_MODE_KEY = 'leapmux-enter-key-mode'

export function getEnterKeyMode(): EnterKeyMode {
  const stored = safeGetString(ENTER_KEY_MODE_KEY)
  return stored === 'enter-sends' ? 'enter-sends' : 'cmd-enter-sends'
}

export function setEnterKeyMode(mode: EnterKeyMode): void {
  safeSetString(ENTER_KEY_MODE_KEY, mode)
}

/**
 * Restore a saved cursor position in a ProseMirror editor view.  If the saved
 * position is beyond the document (e.g. the cursor was in a trailing empty
 * paragraph that the markdown parser didn't recreate), re-insert an empty
 * paragraph after a trailing blockquote so the cursor lands outside it.
 */
export function restoreCursor(editor: Editor, savedCursor: number): void {
  editor.action((ctx: Ctx) => {
    const view = ctx.get(editorViewCtx)
    const { doc, schema } = view.state
    const maxPos = doc.content.size - 1

    if (savedCursor > maxPos && doc.lastChild?.type.name === 'blockquote') {
      const insertPos = doc.content.size
      const paragraph = schema.nodes.paragraph.create()
      const tr = view.state.tr.insert(insertPos, paragraph)
      tr.setSelection(TextSelection.create(tr.doc, insertPos + 1))
      view.dispatch(tr)
      return
    }

    const pos = savedCursor >= 0 ? Math.min(savedCursor, maxPos) : maxPos
    if (pos > 0) {
      const tr = view.state.tr.setSelection(TextSelection.create(doc, pos))
      view.dispatch(tr)
    }
  })
}

/**
 * Serialize the current ProseMirror document and save it as a draft.
 * The saved cursor position allows {@link restoreCursor} to reconstruct
 * trailing empty paragraphs that the markdown parser strips.
 */
export function saveDraftFromEditor(editor: Editor, draftKey: string): void {
  editor.action((ctx: Ctx) => {
    const serializer = ctx.get(serializerCtx)
    const view = ctx.get(editorViewCtx)
    const text = serializer(view.state.doc).trim()
    const cursor = view.state.selection.from
    saveDraft(draftKey, text, cursor)
  })
}

export { clearDraft }
