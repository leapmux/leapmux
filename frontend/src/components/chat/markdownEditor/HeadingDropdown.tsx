import type { Editor } from '@milkdown/core'
import type { Accessor, Component } from 'solid-js'
import { wrapInHeadingCommand } from '@milkdown/preset-commonmark'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import Heading1 from 'lucide-solid/icons/heading-1'
import Heading2 from 'lucide-solid/icons/heading-2'
import Heading3 from 'lucide-solid/icons/heading-3'
import Heading4 from 'lucide-solid/icons/heading-4'
import Heading5 from 'lucide-solid/icons/heading-5'
import Heading6 from 'lucide-solid/icons/heading-6'
import Pilcrow from 'lucide-solid/icons/pilcrow'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { runEditorCommand } from './editorToolbarCommands'
import * as styles from './MarkdownEditor.css'

export interface HeadingDropdownProps {
  editorInstance: Accessor<Editor | undefined>
  focusEditor: () => void
  activeHeadingLevel: Accessor<number>
}

export const HeadingDropdown: Component<HeadingDropdownProps> = (props) => {
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

  const setHeading = (level: number) =>
    runEditorCommand(props.editorInstance, props.focusEditor, wrapInHeadingCommand.key, level)

  return (
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
          <Icon icon={ChevronDown} size="xxs" />
        </IconButton>
      )}
      data-testid="heading-menu"
    >
      <button role="menuitem" data-testid="heading-paragraph" onClick={() => setHeading(0)}>
        <p class={styles.headingPreviewItem}>Paragraph</p>
      </button>
      <button role="menuitem" data-testid="heading-1" onClick={() => setHeading(1)}>
        <h1 class={styles.headingPreviewItem}>Heading 1</h1>
      </button>
      <button role="menuitem" data-testid="heading-2" onClick={() => setHeading(2)}>
        <h2 class={styles.headingPreviewItem}>Heading 2</h2>
      </button>
      <button role="menuitem" data-testid="heading-3" onClick={() => setHeading(3)}>
        <h3 class={styles.headingPreviewItem}>Heading 3</h3>
      </button>
      <button role="menuitem" data-testid="heading-4" onClick={() => setHeading(4)}>
        <h4 class={styles.headingPreviewItem}>Heading 4</h4>
      </button>
      <button role="menuitem" data-testid="heading-5" onClick={() => setHeading(5)}>
        <h5 class={styles.headingPreviewItem}>Heading 5</h5>
      </button>
      <button role="menuitem" data-testid="heading-6" onClick={() => setHeading(6)}>
        <h6 class={styles.headingPreviewItem}>Heading 6</h6>
      </button>
    </DropdownMenu>
  )
}
