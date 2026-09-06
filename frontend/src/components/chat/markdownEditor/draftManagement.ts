import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import type { Draft } from '~/lib/editor/draftPersistence'
import { editorViewCtx, serializerCtx } from '@milkdown/core'
import { TextSelection } from '@milkdown/prose/state'
import { clearDraft, loadDraft, saveDraft } from '~/lib/editor/draftPersistence'

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
    const raw = serializer(view.state.doc)
    const text = typeof raw === 'string' ? raw.trim() : ''
    const cursor = view.state.selection.from
    saveDraft(draftKey, text, cursor)
  })
}

/** The document an editor shows for a key that has no saved draft. */
const EMPTY_DRAFT: Draft = { content: '', cursor: -1 }

/**
 * Build the draft-key swapper for ONE editor.
 *
 * A swap reads the incoming key's draft and then replaces the document with it.
 * The read is ASYNCHRONOUS -- drafts are arbitrary user prose, so the family is
 * unbounded and lives on the unmirrored storage tier -- which opens a window
 * that a synchronous read did not have: a second swap can start while the first
 * is still reading, and the first read then lands last and installs an OLDER
 * key's prose over the document the user is looking at.
 *
 * The generation token closes it. It lives HERE rather than at the call site so
 * a caller cannot swap without it: every path to a document replacement goes
 * through `apply`, and `apply` runs only for the newest swap.
 *
 * `apply` is per swap because it closes over the editor instance and the
 * content-change handler that were current when the swap STARTED. A null key
 * means "no draft to restore", which shows an empty document.
 */
export function createDraftSwapper(): (draftKey: string | null, apply: (draft: Draft) => void) => Promise<void> {
  let token = 0
  return async (draftKey, apply) => {
    const mine = ++token
    const draft = draftKey ? await loadDraft(draftKey) : EMPTY_DRAFT
    // A later swap won. Replacing the document now would install an older key's
    // prose over the one the user is looking at.
    if (mine !== token)
      return
    apply(draft)
  }
}

export { clearDraft }
