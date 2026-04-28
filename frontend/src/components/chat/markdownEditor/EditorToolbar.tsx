import type { Editor } from '@milkdown/core'
import type { Accessor, Component } from 'solid-js'
import type { LinkPopoverProps } from './LinkPopover'
import type { EnterKeyMode } from '~/lib/browserStorage'
import type { ActiveFormatting } from '~/lib/editor/toolbarState'
import { insertHrCommand, toggleEmphasisCommand, toggleStrongCommand } from '@milkdown/preset-commonmark'
import { toggleStrikethroughCommand } from '@milkdown/preset-gfm'
import Bold from 'lucide-solid/icons/bold'
import Code from 'lucide-solid/icons/code'
import Italic from 'lucide-solid/icons/italic'
import List from 'lucide-solid/icons/list'
import ListChecks from 'lucide-solid/icons/list-checks'
import ListOrdered from 'lucide-solid/icons/list-ordered'
import Minus from 'lucide-solid/icons/minus'
import Paperclip from 'lucide-solid/icons/paperclip'
import SquareCode from 'lucide-solid/icons/square-code'
import Strikethrough from 'lucide-solid/icons/strikethrough'
import TextQuote from 'lucide-solid/icons/text-quote'
import { Show } from 'solid-js'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { Tooltip } from '~/components/common/Tooltip'
import { formatShortcut } from '~/lib/shortcuts/display'
import { runEditorCommand, toggleBlockquote, toggleBulletList, toggleOrderedList, toggleTaskList } from './editorToolbarCommands'
import { HeadingDropdown } from './HeadingDropdown'
import { LinkPopover } from './LinkPopover'
import * as styles from './MarkdownEditor.css'

const modEnterLabel = formatShortcut('$mod+Enter')

/** Editor wiring for the toolbar's command buttons. */
export interface EditorRefs {
  editorInstance: Accessor<Editor | undefined>
  focusEditor: () => void
}

/** Enter-key mode state for the trailing toggle button + tooltip. */
export interface EnterModeState {
  mode: Accessor<EnterKeyMode>
  toggle: () => void
  tooltipOpen: Accessor<boolean>
  setTooltipOpen: (open: boolean) => void
}

export interface EditorToolbarProps {
  refs: EditorRefs
  /** Solid store proxy holding the active-formatting state. Per-key reactivity. */
  activeFormatting: ActiveFormatting
  /** Link popover state. Re-uses the props shape consumed by `<LinkPopover>` minus the `activeLink` accessor (sourced from `activeFormatting.link`). */
  link: Omit<LinkPopoverProps, 'activeLink'>
  enterMode: EnterModeState
  /** Inline-code button click handler. */
  handleInlineCodeClick: () => void
  /** Code-block button click handler. */
  handleCodeBlockClick: () => void
  /** Optional upload (paperclip) button handler. */
  onUploadClick?: () => void
}

export const EditorToolbar: Component<EditorToolbarProps> = (props) => {
  const runCommand = (cmd: Parameters<typeof runEditorCommand>[2]) =>
    runEditorCommand(props.refs.editorInstance, props.refs.focusEditor, cmd)

  const listOpts = () => ({
    editorInstance: props.refs.editorInstance,
    focusEditor: props.refs.focusEditor,
    activeBulletList: () => props.activeFormatting.bulletList,
    activeOrderedList: () => props.activeFormatting.orderedList,
    activeTaskList: () => props.activeFormatting.taskList,
  })

  const enterModeTitle = () => {
    if (props.enterMode.mode() === 'enter-sends') {
      return `Enter to send, Shift+Enter for new line. Click to switch to ${modEnterLabel} mode.`
    }
    return `${modEnterLabel} to send, Enter for new line. Click to switch to Enter mode.`
  }

  return (
    <div class={styles.toolbar}>
      <HeadingDropdown
        editorInstance={props.refs.editorInstance}
        focusEditor={props.refs.focusEditor}
        activeHeadingLevel={() => props.activeFormatting.headingLevel}
      />
      <hr />
      <IconButton
        icon={Bold}
        size="md"
        state={props.activeFormatting.bold ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-bold"
        title="Bold"
        onClick={() => runCommand(toggleStrongCommand.key)}
      />
      <IconButton
        icon={Italic}
        size="md"
        state={props.activeFormatting.italic ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-italic"
        title="Italic"
        onClick={() => runCommand(toggleEmphasisCommand.key)}
      />
      <IconButton
        icon={Strikethrough}
        size="md"
        state={props.activeFormatting.strikethrough ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-strikethrough"
        title="Strikethrough"
        onClick={() => runCommand(toggleStrikethroughCommand.key)}
      />
      <hr />
      <IconButton
        icon={Code}
        size="md"
        state={props.activeFormatting.code ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-code"
        title="Inline code"
        onClick={() => props.handleInlineCodeClick()}
      />
      <IconButton
        icon={SquareCode}
        size="md"
        state={props.activeFormatting.codeBlock ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-codeblock"
        title="Code block"
        onClick={() => props.handleCodeBlockClick()}
      />
      <hr />
      <IconButton
        icon={List}
        size="md"
        state={props.activeFormatting.bulletList ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-bullet-list"
        title="Bullet list"
        onClick={() => toggleBulletList(listOpts())}
      />
      <IconButton
        icon={ListOrdered}
        size="md"
        state={props.activeFormatting.orderedList ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-ordered-list"
        title="Ordered list"
        onClick={() => toggleOrderedList(listOpts())}
      />
      <IconButton
        icon={ListChecks}
        size="md"
        state={props.activeFormatting.taskList ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-tasklist"
        title="Task list"
        onClick={() => toggleTaskList(listOpts())}
      />
      <hr />
      <IconButton
        icon={TextQuote}
        size="md"
        state={props.activeFormatting.blockquote ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-blockquote"
        title="Blockquote"
        onClick={() => toggleBlockquote(props.refs.editorInstance, props.refs.focusEditor, props.activeFormatting.blockquote)}
      />
      <IconButton
        icon={Minus}
        size="md"
        data-testid="toolbar-hr"
        title="Horizontal rule"
        onClick={() => runCommand(insertHrCommand.key)}
      />
      <hr />
      <LinkPopover
        activeLink={() => props.activeFormatting.link}
        linkPopoverOpen={props.link.linkPopoverOpen}
        setLinkPopoverOpen={props.link.setLinkPopoverOpen}
        linkUrl={props.link.linkUrl}
        setLinkUrl={props.link.setLinkUrl}
        handleLinkSubmit={props.link.handleLinkSubmit}
        handleLinkRemove={props.link.handleLinkRemove}
      />
      <Show when={props.onUploadClick}>
        <IconButton
          icon={Paperclip}
          size="md"
          data-testid="toolbar-upload"
          title="Attach file"
          onClick={() => props.onUploadClick?.()}
        />
      </Show>
      <span class={styles.enterModeWrapper}>
        <Tooltip text={enterModeTitle()}>
          <button
            type="button"
            class="ghost small"
            data-testid="enter-mode-toggle"
            onClick={() => {
              props.enterMode.toggle()
              props.enterMode.setTooltipOpen(true)
            }}
          >
            <Show
              when={props.enterMode.mode() === 'enter-sends'}
              fallback={`${modEnterLabel} sends`}
            >
              Enter sends
            </Show>
          </button>
        </Tooltip>
      </span>
    </div>
  )
}
