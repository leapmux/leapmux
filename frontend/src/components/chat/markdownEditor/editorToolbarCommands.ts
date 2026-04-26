import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import type { Accessor } from 'solid-js'
import { editorViewCtx } from '@milkdown/core'
import { wrapInBlockquoteCommand, wrapInBulletListCommand, wrapInOrderedListCommand } from '@milkdown/preset-commonmark'
import { lift } from '@milkdown/prose/commands'
import { liftListItem } from '@milkdown/prose/schema-list'
import { callCommand } from '@milkdown/utils'

/** Run a Milkdown command via the active editor and refocus. */
export function runEditorCommand(
  editorInstance: Accessor<Editor | undefined>,
  focusEditor: () => void,
  cmd: Parameters<typeof callCommand>[0],
  payload?: unknown,
): void {
  const editor = editorInstance()
  if (!editor)
    return
  editor.action(callCommand(cmd, payload))
  focusEditor()
}

/** Toggle blockquote: lift out if inside, wrap if outside. */
export function toggleBlockquote(
  editorInstance: Accessor<Editor | undefined>,
  focusEditor: () => void,
  isActive: boolean,
): void {
  const editor = editorInstance()
  if (!editor)
    return
  if (isActive) {
    editor.action((ctx: Ctx) => {
      const view = ctx.get(editorViewCtx)
      lift(view.state, view.dispatch)
      view.focus()
    })
  }
  else {
    runEditorCommand(editorInstance, focusEditor, wrapInBlockquoteCommand.key)
  }
}

/** Lift the current list item out to a paragraph. */
export function liftFromList(editorInstance: Accessor<Editor | undefined>): void {
  const editor = editorInstance()
  if (!editor)
    return
  editor.action((ctx: Ctx) => {
    const view = ctx.get(editorViewCtx)
    const listItemType = view.state.schema.nodes.list_item
    liftListItem(listItemType)(view.state, view.dispatch)
    view.focus()
  })
}

/** Switch from the current list type to a different one (optionally a task list). */
export function switchListType(
  editorInstance: Accessor<Editor | undefined>,
  targetListType: 'bullet_list' | 'ordered_list',
  taskList = false,
): void {
  const editor = editorInstance()
  if (!editor)
    return
  editor.action((ctx: Ctx) => {
    const view = ctx.get(editorViewCtx)
    const { state } = view
    const { $from } = state.selection
    for (let d = $from.depth; d >= 1; d--) {
      const node = $from.node(d)
      if (node.type.name === 'bullet_list' || node.type.name === 'ordered_list') {
        const pos = $from.before(d)
        const newType = state.schema.nodes[targetListType]
        let tr = state.tr.setNodeMarkup(pos, newType)
        const newListNode = tr.doc.nodeAt(pos)
        if (newListNode) {
          newListNode.forEach((child, offset) => {
            if (child.type.name === 'list_item') {
              const childPos = pos + 1 + offset
              if (taskList) {
                if (child.attrs.checked == null) {
                  tr = tr.setNodeMarkup(childPos, undefined, { ...child.attrs, checked: 'false' })
                }
              }
              else {
                if (child.attrs.checked != null) {
                  tr = tr.setNodeMarkup(childPos, undefined, { ...child.attrs, checked: null })
                }
              }
            }
          })
        }
        view.dispatch(tr)
        view.focus()
        break
      }
    }
  })
}

interface ListToggleOptions {
  editorInstance: Accessor<Editor | undefined>
  focusEditor: () => void
  activeBulletList: Accessor<boolean>
  activeOrderedList: Accessor<boolean>
  activeTaskList: Accessor<boolean>
}

export function toggleBulletList(opts: ListToggleOptions): void {
  if (opts.activeBulletList()) {
    liftFromList(opts.editorInstance)
  }
  else if (opts.activeOrderedList() || opts.activeTaskList()) {
    switchListType(opts.editorInstance, 'bullet_list')
  }
  else {
    runEditorCommand(opts.editorInstance, opts.focusEditor, wrapInBulletListCommand.key)
  }
}

export function toggleOrderedList(opts: ListToggleOptions): void {
  if (opts.activeOrderedList()) {
    liftFromList(opts.editorInstance)
  }
  else if (opts.activeBulletList() || opts.activeTaskList()) {
    switchListType(opts.editorInstance, 'ordered_list')
  }
  else {
    runEditorCommand(opts.editorInstance, opts.focusEditor, wrapInOrderedListCommand.key)
  }
}

export function toggleTaskList(opts: ListToggleOptions): void {
  if (opts.activeTaskList()) {
    liftFromList(opts.editorInstance)
    return
  }
  if (opts.activeBulletList() || opts.activeOrderedList()) {
    switchListType(opts.editorInstance, 'bullet_list', true)
    return
  }
  // Wrap into a bullet list first, then convert items to task items
  runEditorCommand(opts.editorInstance, opts.focusEditor, wrapInBulletListCommand.key)
  const editor = opts.editorInstance()
  if (!editor)
    return
  editor.action((ctx: Ctx) => {
    const view = ctx.get(editorViewCtx)
    const { state } = view
    const { $from } = state.selection
    for (let d = $from.depth; d >= 1; d--) {
      const node = $from.node(d)
      if (node.type.name === 'bullet_list') {
        const pos = $from.before(d)
        let tr = state.tr
        node.forEach((child, offset) => {
          if (child.type.name === 'list_item' && child.attrs.checked == null) {
            tr = tr.setNodeMarkup(pos + 1 + offset, undefined, { ...child.attrs, checked: 'false' })
          }
        })
        view.dispatch(tr)
        view.focus()
        break
      }
    }
  })
}
