import type { Editor } from '@milkdown/core'
import type { Accessor, Component } from 'solid-js'
import type { EnterKeyMode } from '~/lib/browserStorage'
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
  onUploadClick?: () => void
}

export const EditorToolbar: Component<EditorToolbarProps> = (props) => {
  const runCommand = (cmd: Parameters<typeof runEditorCommand>[2]) =>
    runEditorCommand(props.editorInstance, props.focusEditor, cmd)

  const listOpts = () => ({
    editorInstance: props.editorInstance,
    focusEditor: props.focusEditor,
    activeBulletList: props.activeBulletList,
    activeOrderedList: props.activeOrderedList,
    activeTaskList: props.activeTaskList,
  })

  const enterModeTitle = () => {
    if (props.enterMode() === 'enter-sends') {
      return `Enter to send, Shift+Enter for new line. Click to switch to ${modEnterLabel} mode.`
    }
    return `${modEnterLabel} to send, Enter for new line. Click to switch to Enter mode.`
  }

  return (
    <div class={styles.toolbar}>
      <HeadingDropdown
        editorInstance={props.editorInstance}
        focusEditor={props.focusEditor}
        activeHeadingLevel={props.activeHeadingLevel}
      />
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
        onClick={() => toggleBulletList(listOpts())}
      />
      <IconButton
        icon={ListOrdered}
        size="md"
        state={props.activeOrderedList() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-ordered-list"
        title="Ordered list"
        onClick={() => toggleOrderedList(listOpts())}
      />
      <IconButton
        icon={ListChecks}
        size="md"
        state={props.activeTaskList() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-tasklist"
        title="Task list"
        onClick={() => toggleTaskList(listOpts())}
      />
      <hr />
      <IconButton
        icon={TextQuote}
        size="md"
        state={props.activeBlockquote() ? IconButtonState.Active : IconButtonState.Enabled}
        data-testid="toolbar-blockquote"
        title="Blockquote"
        onClick={() => toggleBlockquote(props.editorInstance, props.focusEditor, props.activeBlockquote())}
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
        activeLink={props.activeLink}
        linkPopoverOpen={props.linkPopoverOpen}
        setLinkPopoverOpen={props.setLinkPopoverOpen}
        linkUrl={props.linkUrl}
        setLinkUrl={props.setLinkUrl}
        handleLinkSubmit={props.handleLinkSubmit}
        handleLinkRemove={props.handleLinkRemove}
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
              props.toggleEnterMode()
              props.setEnterTooltipOpen(true)
            }}
          >
            <Show
              when={props.enterMode() === 'enter-sends'}
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
