import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import type { Setter } from 'solid-js'
import { editorViewCtx, serializerCtx } from '@milkdown/core'
import { TextSelection } from '@milkdown/prose/state'
import { replaceAll } from '@milkdown/utils'

/** Options for setting up the ref callbacks exposed to the parent component. */
export interface EditorRefHandlersOptions {
  editor: Editor
  setMarkdown: Setter<string>
  onContentChange?: (hasContent: boolean) => void
  sendRef?: (send: () => void) => void
  focusRef?: (focus: () => void) => void
  contentRef?: (get: () => string, set: (text: string) => void) => void
  handleSend: () => void
}

/**
 * Wire up the ref callbacks (`contentRef`, `sendRef`, `focusRef`) that expose
 * editor operations to the parent component.
 */
export function setupEditorRefHandlers(opts: EditorRefHandlersOptions): void {
  const { editor } = opts

  // Expose get/set content functions to parent component (e.g. for save/restore per-question text)
  opts.contentRef?.(
    () => {
      let text = ''
      try {
        editor.action((ctx: Ctx) => {
          const serializer = ctx.get(serializerCtx)
          const view = ctx.get(editorViewCtx)
          text = serializer(view.state.doc).trim()
        })
      }
      catch { /* editor may not be ready */ }
      return text
    },
    (text: string) => {
      try {
        editor.action(replaceAll(text))
        // If the document ends with a blockquote, insert an empty paragraph
        // after it so the cursor lands outside the blockquote.  ProseMirror
        // does not create a trailing paragraph from trailing \n\n in markdown.
        editor.action((ctx: Ctx) => {
          const view = ctx.get(editorViewCtx)
          const { doc, schema } = view.state
          const lastChild = doc.lastChild
          if (lastChild && lastChild.type.name === 'blockquote') {
            const insertPos = doc.content.size
            const paragraph = schema.nodes.paragraph.create()
            const tr = view.state.tr.insert(insertPos, paragraph)
            tr.setSelection(TextSelection.create(tr.doc, insertPos + 1))
            view.dispatch(tr)
          }
          else {
            const endPos = doc.content.size - 1
            if (endPos > 0) {
              const tr = view.state.tr.setSelection(TextSelection.create(doc, endPos))
              view.dispatch(tr)
            }
          }
        })
        opts.setMarkdown(text)
        opts.onContentChange?.(text.trim().length > 0)
      }
      catch { /* editor may not be ready */ }
    },
  )

  // Expose handleSend to parent component (e.g. for external send button)
  opts.sendRef?.(opts.handleSend)

  // Expose focus function to parent component (e.g. for tab selection)
  opts.focusRef?.(() => {
    try {
      editor.action((ctx: Ctx) => {
        ctx.get(editorViewCtx).focus()
      })
    }
    catch { /* editor may not be ready */ }
  })
}
