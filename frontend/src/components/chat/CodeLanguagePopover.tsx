import type { Accessor, Component, Setter } from 'solid-js'
import { Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { LANGUAGES } from '~/lib/languages'
import * as styles from './MarkdownEditor.css'
import { FilterableListbox } from './settingsShared'

export interface CodeLanguagePopoverProps {
  open: Accessor<boolean>
  setOpen: Setter<boolean>
  nodePos: Accessor<number>
  setNodePos: Setter<number>
  filter: Accessor<string>
  setFilter: Setter<string>
  anchorRef: Accessor<HTMLElement | undefined>
  onApply: (langId: string) => void
}

/** Map languages to FilterableItem shape. */
const LANGUAGE_ITEMS = LANGUAGES.map(lang => ({
  label: lang.label,
  value: lang.id,
  secondary: lang.id,
}))

export const CodeLanguagePopover: Component<CodeLanguagePopoverProps> = (props) => {
  let popoverRef: HTMLElement | undefined

  return (
    <DropdownMenu
      as="div"
      anchorRef={props.anchorRef}
      open={props.open}
      popoverRef={(el) => { popoverRef = el }}
      class={styles.codeLangPopoverContent}
      data-testid="code-lang-popover"
      onToggle={(open) => {
        if (!open) {
          props.setOpen(false)
          props.setNodePos(-1)
          props.setFilter('')
        }
      }}
    >
      <Show when={props.open()}>
        <FilterableListbox
          items={LANGUAGE_ITEMS}
          placeholder="Filter languages..."
          testIdPrefix="code-lang"
          onSelect={langId => props.onApply(langId === 'plaintext' ? '' : langId)}
          onEscape={() => popoverRef?.hidePopover()}
          autoFocus
          listboxClass={styles.comboboxListbox}
          itemClass={styles.comboboxItem}
          itemHighlightedClass={styles.comboboxItemHighlighted}
          controlClass={styles.comboboxControl}
          inputClass={styles.comboboxInput}
        />
      </Show>
    </DropdownMenu>
  )
}
