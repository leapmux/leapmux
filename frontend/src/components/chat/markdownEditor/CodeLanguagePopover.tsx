import type { Accessor, Component, Setter } from 'solid-js'
import { createEffect } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { LANGUAGES } from '~/lib/languages'
import { FilterableListbox } from '../settingsShared'
import * as styles from './MarkdownEditor.css'

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

  // The listbox stays mounted (NOT gated on `open`) so the popover always has its
  // content in the DOM. That removes the race where the popover was shown/measured
  // before the (open-gated) list had rendered -- which positioned an empty/wrong-
  // sized box that only corrected on a later event, showing as an empty/misplaced
  // popover or one that "appeared" only after a mouse move. Because it no longer
  // remounts per open, reset the filter and focus the input on open here instead of
  // via mount-time autofocus.
  createEffect(() => {
    if (!props.open())
      return
    props.setFilter('')
    requestAnimationFrame(() => {
      // Bail if the popover closed again before this frame (rapid open->close): the
      // listbox stays mounted, so the input still resolves, and focusing it would pull
      // focus/caret onto a now-hidden popover.
      if (!props.open())
        return
      const input = popoverRef?.querySelector<HTMLInputElement>('input')
      input?.focus()
      input?.select()
    })
  })

  return (
    <DropdownMenu
      as="div"
      anchorRef={props.anchorRef}
      open={props.open}
      popoverRef={(el) => { popoverRef = el }}
      class={styles.codeLangPopoverContent}
      data-testid="code-lang-popover"
      onToggle={(open) => {
        // Sync the open signal to the NATIVE popover state in both directions, so a
        // native light-dismiss (click-outside) keeps the signal accurate and the
        // pointerdown-captured "was open" check (toggle-on-reclick) stays reliable.
        props.setOpen(open)
        if (!open) {
          props.setNodePos(-1)
          props.setFilter('')
        }
      }}
    >
      <FilterableListbox
        items={LANGUAGE_ITEMS}
        placeholder="Filter languages..."
        testIdPrefix="code-lang"
        onSelect={langId => props.onApply(langId === 'plaintext' ? '' : langId)}
        onEscape={() => popoverRef?.hidePopover()}
        filter={props.filter}
        setFilter={props.setFilter}
        resetKey={props.open}
        listboxClass={styles.comboboxListbox}
        itemClass={styles.comboboxItem}
        itemHighlightedClass={styles.comboboxItemHighlighted}
        controlClass={styles.comboboxControl}
        inputClass={styles.comboboxInput}
      />
    </DropdownMenu>
  )
}
