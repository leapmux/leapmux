import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import type { Accessor, Component } from 'solid-js'
import { editorViewCtx } from '@milkdown/core'
import { insertHrCommand, toggleEmphasisCommand, toggleStrongCommand, wrapInBlockquoteCommand, wrapInBulletListCommand, wrapInHeadingCommand, wrapInOrderedListCommand } from '@milkdown/preset-commonmark'
import { toggleStrikethroughCommand } from '@milkdown/preset-gfm'
import { lift } from '@milkdown/prose/commands'
import { liftListItem } from '@milkdown/prose/schema-list'
import { callCommand } from '@milkdown/utils'
import Bold from 'lucide-solid/icons/bold'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import ChevronUp from 'lucide-solid/icons/chevron-up'
import Code from 'lucide-solid/icons/code'
import Command from 'lucide-solid/icons/command'
import Heading1 from 'lucide-solid/icons/heading-1'
import Heading2 from 'lucide-solid/icons/heading-2'
import Heading3 from 'lucide-solid/icons/heading-3'
import Heading4 from 'lucide-solid/icons/heading-4'
import Heading5 from 'lucide-solid/icons/heading-5'
import Heading6 from 'lucide-solid/icons/heading-6'
import Italic from 'lucide-solid/icons/italic'
import Link2 from 'lucide-solid/icons/link-2'
import List from 'lucide-solid/icons/list'
import ListChecks from 'lucide-solid/icons/list-checks'
import ListOrdered from 'lucide-solid/icons/list-ordered'
import Minus from 'lucide-solid/icons/minus'
import Pilcrow from 'lucide-solid/icons/pilcrow'
import SquareCode from 'lucide-solid/icons/square-code'
import Strikethrough from 'lucide-solid/icons/strikethrough'
import TextQuote from 'lucide-solid/icons/text-quote'
import { createUniqueId, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { positionPopoverAbove } from '~/lib/popoverPosition'
import * as styles from './MarkdownEditor.css'

type EnterKeyMode = 'enter-sends' | 'cmd-enter-sends'

const isMac = typeof navigator !== 'undefined'
  && /Mac|iPhone|iPad|iPod/.test(navigator.platform)

export interface EditorToolbarProps {
  editorInstance: Accessor<Editor | undefined>
  focusEditor: () => void
  enterMode: Accessor<EnterKeyMode>
  toggleEnterMode: () => void
  enterTooltipOpen: Accessor<boolean>
  setEnterTooltipOpen: (open: boolean) => void
  activeBold: Accessor<boolean>
  activeItalic: Accessor<boolean>
  activeStrikethrough: Accessor<boolean>
  activeCode: Accessor<boolean>
  activeCodeBlock: Accessor<boolean>
  activeBlockquote: Accessor<boolean>
  activeLink: Accessor<boolean>
  activeHeadingLevel: Accessor<number>
  activeBulletList: Accessor<boolean>
  activeOrderedList: Accessor<boolean>
  activeTaskList: Accessor<boolean>
  linkPopoverOpen: Accessor<boolean>
  setLinkPopoverOpen: (open: boolean) => void
  linkUrl: Accessor<string>
  setLinkUrl: (url: string) => void
  handleLinkSubmit: () => void
  handleLinkRemove: () => void
  handleCodeBlockClick: () => void
  handleInlineCodeClick: () => void
}

export const EditorToolbar: Component<EditorToolbarProps> = (props) => {
  const linkPopoverId = createUniqueId()
  let linkPopoverRef: HTMLDivElement | undefined
  let linkTriggerRef: HTMLButtonElement | undefined
  let linkInputRef: HTMLInputElement | undefined

  let headingMenuRef: HTMLElement | undefined

  const runCommand = (cmd: Parameters<typeof callCommand>[0], payload?: unknown) => {
    const editor = props.editorInstance()
    if (editor) {
      editor.action(callCommand(cmd, payload))
      props.focusEditor()
    }
  }

  // Toggle blockquote: lift out if inside, wrap if outside
  const toggleBlockquote = () => {
    const editor = props.editorInstance()
    if (!editor)
      return
    if (props.activeBlockquote()) {
      editor.action((ctx: Ctx) => {
        const view = ctx.get(editorViewCtx)
        lift(view.state, view.dispatch)
        view.focus()
      })
    }
    else {
      runCommand(wrapInBlockquoteCommand.key)
    }
  }

  /** Lift the current list item out to a paragraph. */
  const liftFromList = () => {
    const editor = props.editorInstance()
    if (!editor)
      return
    editor.action((ctx: Ctx) => {
      const view = ctx.get(editorViewCtx)
      const listItemType = view.state.schema.nodes.list_item
      liftListItem(listItemType)(view.state, view.dispatch)
      view.focus()
    })
  }

  /** Switch from the current list type to a different one. */
  const switchListType = (targetListType: 'bullet_list' | 'ordered_list', taskList = false) => {
    const editor = props.editorInstance()
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
          // Update list item attrs for task list conversion
          const newListNode = tr.doc.nodeAt(pos)
          if (newListNode) {
            newListNode.forEach((child, offset) => {
              if (child.type.name === 'list_item') {
                const childPos = pos + 1 + offset
                if (taskList) {
                  // Set checked to 'false' for task list items
                  if (child.attrs.checked == null) {
                    tr = tr.setNodeMarkup(childPos, undefined, { ...child.attrs, checked: 'false' })
                  }
                }
                else {
                  // Remove checked attr for non-task list items
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

  const toggleBulletList = () => {
    if (props.activeBulletList()) {
      liftFromList()
    }
    else if (props.activeOrderedList() || props.activeTaskList()) {
      switchListType('bullet_list')
    }
    else {
      runCommand(wrapInBulletListCommand.key)
    }
  }

  const toggleOrderedList = () => {
    if (props.activeOrderedList()) {
      liftFromList()
    }
    else if (props.activeBulletList() || props.activeTaskList()) {
      switchListType('ordered_list')
    }
    else {
      runCommand(wrapInOrderedListCommand.key)
    }
  }

  const toggleTaskList = () => {
    if (props.activeTaskList()) {
      liftFromList()
    }
    else if (props.activeBulletList() || props.activeOrderedList()) {
      switchListType('bullet_list', true)
    }
    else {
      // Wrap into a bullet list first, then convert items to task items
      runCommand(wrapInBulletListCommand.key)
      const editor = props.editorInstance()
      if (editor) {
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
    }
  }

  const headingIcon = () => {
    switch (props.activeHeadingLevel()) {
      case 1: return Heading1
      case 2: return Heading2
      case 3: return Heading3
      case 4: return Heading4
      case 5: return Heading5
      case 6: return Heading6
      default: return Pilcrow
    }
  }

  const headingLabel = () => {
    const level = props.activeHeadingLevel()
    if (level === 0)
      return 'Paragraph'
    return `Heading ${level}`
  }

  const modKeyLabel = isMac ? 'Cmd' : 'Ctrl'

  const enterModeTitle = () => {
    if (props.enterMode() === 'enter-sends') {
      return `Enter to send, Shift+Enter for new line. Click to switch to ${modKeyLabel}+Enter mode.`
    }
    return `${modKeyLabel}+Enter to send, Enter for new line. Click to switch to Enter mode.`
  }

  const handleLinkTriggerClick = () => {
    if (props.activeLink()) {
      props.handleLinkRemove()
      return
    }
    // Toggle: if already open, close it
    if (props.linkPopoverOpen()) {
      linkPopoverRef?.hidePopover()
      return
    }
    props.setLinkPopoverOpen(true)
    linkPopoverRef?.showPopover()
    if (linkPopoverRef && linkTriggerRef) {
      requestAnimationFrame(() => {
        positionPopoverAbove(linkTriggerRef!, linkPopoverRef!)
      })
    }
  }

  const handleLinkPopoverToggle = (e: Event) => {
    const toggleEvent = e as ToggleEvent
    if (toggleEvent.newState === 'open') {
      // Focus the URL input when the popover opens
      linkInputRef?.focus()
    }
    else {
      props.setLinkPopoverOpen(false)
      props.setLinkUrl('')
    }
  }

  return (
    <div class={styles.toolbar}>
      <DropdownMenu
        trigger={triggerProps => (
          <IconButton
            icon={headingIcon()}
            class={styles.headingPickerButton}
            state={props.activeHeadingLevel() > 0 ? IconButtonState.Active : IconButtonState.Enabled}
            title={headingLabel()}
            data-testid="toolbar-heading"
            {...triggerProps}
          >
            <ChevronDown size={10} />
          </IconButton>
        )}
        popoverRef={(el) => { headingMenuRef = el }}
        data-testid="heading-menu"
      >
        <button
          role="menuitem"
          data-testid="heading-paragraph"
          onClick={() => {
            runCommand(wrapInHeadingCommand.key, 0)
            headingMenuRef?.hidePopover()
          }}
        >
          <p class={styles.headingPreviewItem}>Paragraph</p>
        </button>
        <button
          role="menuitem"
          data-testid="heading-1"
          onClick={() => {
            runCommand(wrapInHeadingCommand.key, 1)
            headingMenuRef?.hidePopover()
          }}
        >
          <h1 class={styles.headingPreviewItem}>Heading 1</h1>
        </button>
        <button
          role="menuitem"
          data-testid="heading-2"
          onClick={() => {
            runCommand(wrapInHeadingCommand.key, 2)
            headingMenuRef?.hidePopover()
          }}
        >
          <h2 class={styles.headingPreviewItem}>Heading 2</h2>
        </button>
        <button
          role="menuitem"
          data-testid="heading-3"
          onClick={() => {
            runCommand(wrapInHeadingCommand.key, 3)
            headingMenuRef?.hidePopover()
          }}
        >
          <h3 class={styles.headingPreviewItem}>Heading 3</h3>
        </button>
        <button
          role="menuitem"
          data-testid="heading-4"
          onClick={() => {
            runCommand(wrapInHeadingCommand.key, 4)
            headingMenuRef?.hidePopover()
          }}
        >
          <h4 class={styles.headingPreviewItem}>Heading 4</h4>
        </button>
        <button
          role="menuitem"
          data-testid="heading-5"
          onClick={() => {
            runCommand(wrapInHeadingCommand.key, 5)
            headingMenuRef?.hidePopover()
          }}
        >
          <h5 class={styles.headingPreviewItem}>Heading 5</h5>
        </button>
        <button
          role="menuitem"
          data-testid="heading-6"
          onClick={() => {
            runCommand(wrapInHeadingCommand.key, 6)
            headingMenuRef?.hidePopover()
          }}
        >
          <h6 class={styles.headingPreviewItem}>Heading 6</h6>
        </button>
      </DropdownMenu>
      <hr />
      <IconButton
        icon={Bold}
        size="md"
        state={props.activeBold() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-bold"
        title="Bold"
        onClick={() => runCommand(toggleStrongCommand.key)}
      />
      <IconButton
        icon={Italic}
        size="md"
        state={props.activeItalic() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-italic"
        title="Italic"
        onClick={() => runCommand(toggleEmphasisCommand.key)}
      />
      <IconButton
        icon={Strikethrough}
        size="md"
        state={props.activeStrikethrough() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-strikethrough"
        title="Strikethrough"
        onClick={() => runCommand(toggleStrikethroughCommand.key)}
      />
      <hr />
      <IconButton
        icon={Code}
        size="md"
        state={props.activeCode() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-code"
        title="Inline code"
        onClick={() => props.handleInlineCodeClick()}
      />
      <IconButton
        icon={SquareCode}
        size="md"
        state={props.activeCodeBlock() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-codeblock"
        title="Code block"
        onClick={() => props.handleCodeBlockClick()}
      />
      <hr />
      <IconButton
        icon={List}
        size="md"
        state={props.activeBulletList() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-bullet-list"
        title="Bullet list"
        onClick={() => toggleBulletList()}
      />
      <IconButton
        icon={ListOrdered}
        size="md"
        state={props.activeOrderedList() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-ordered-list"
        title="Ordered list"
        onClick={() => toggleOrderedList()}
      />
      <IconButton
        icon={ListChecks}
        size="md"
        state={props.activeTaskList() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-tasklist"
        title="Task list"
        onClick={() => toggleTaskList()}
      />
      <hr />
      <IconButton
        icon={TextQuote}
        size="md"
        state={props.activeBlockquote() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-blockquote"
        title="Blockquote"
        onClick={() => toggleBlockquote()}
      />
      <IconButton
        icon={Minus}
        size="md"
        data-testid="toolbar-hr"
        title="Horizontal rule"
        onClick={() => runCommand(insertHrCommand.key)}
      />
      <hr />
      <IconButton
        ref={linkTriggerRef}
        icon={Link2}
        size="md"
        state={props.activeLink() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-link"
        title="Link"
        onClick={handleLinkTriggerClick}
      />
      <div
        popover="auto"
        id={linkPopoverId}
        class={styles.linkPopover}
        data-testid="link-popover"
        ref={linkPopoverRef}
        onToggle={handleLinkPopoverToggle}
      >
        <form
          onSubmit={(e) => {
            e.preventDefault()
            props.handleLinkSubmit()
            linkPopoverRef?.hidePopover()
          }}
          class={styles.linkPopoverForm}
        >
          <input
            type="url"
            class={styles.linkPopoverInput}
            placeholder="https://..."
            value={props.linkUrl()}
            onInput={e => props.setLinkUrl(e.currentTarget.value)}
            ref={linkInputRef}
            data-testid="link-url-input"
          />
          <button type="submit" class="ghost small" data-testid="link-url-submit">
            Add
          </button>
        </form>
      </div>
      <span class={styles.enterModeWrapper}>
        <button
          type="button"
          class="ghost small"
          title={enterModeTitle()}
          onClick={() => {
            props.toggleEnterMode()
            props.setEnterTooltipOpen(true)
          }}
        >
          <Show
            when={props.enterMode() === 'enter-sends'}
            fallback={(
              <>
                {isMac
                  ? <Command size={10} class={styles.iconNudge} />
                  : <ChevronUp size={10} class={styles.iconNudge} />}
                <span>Enter sends</span>
              </>
            )}
          >
            Enter sends
          </Show>
        </button>
      </span>
    </div>
  )
}
