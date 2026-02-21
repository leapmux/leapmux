import type { Accessor, Component, JSX, Setter } from 'solid-js'
import { createEffect, createMemo, createSignal, For, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { LANGUAGES } from '~/lib/languages'
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

/** Highlight matching substring in text (case-insensitive). */
function highlightMatch(text: string, filter: string): JSX.Element {
  if (!filter)
    return text
  const idx = text.toLowerCase().indexOf(filter.toLowerCase())
  if (idx < 0)
    return text
  return (
    <>
      {text.slice(0, idx)}
      <strong>{text.slice(idx, idx + filter.length)}</strong>
      {text.slice(idx + filter.length)}
    </>
  )
}

export const CodeLanguagePopover: Component<CodeLanguagePopoverProps> = (props) => {
  const [highlightedLangIndex, setHighlightedLangIndex] = createSignal(0)
  let langListRef: HTMLDivElement | undefined
  let popoverRef: HTMLElement | undefined

  const filteredLanguages = createMemo(() => {
    const filter = props.filter().toLowerCase()
    if (!filter)
      return LANGUAGES
    return LANGUAGES.filter(lang =>
      lang.label.toLowerCase().includes(filter)
      || lang.id.toLowerCase().includes(filter),
    )
  })

  // Reset highlighted index when filter changes
  createEffect(() => {
    props.filter()
    setHighlightedLangIndex(0)
  })

  const handleLangKeyDown = (e: KeyboardEvent) => {
    const items = filteredLanguages()
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setHighlightedLangIndex(i => Math.min(i + 1, items.length - 1))
        requestAnimationFrame(() => {
          const el = langListRef?.children[highlightedLangIndex()] as HTMLElement | undefined
          el?.scrollIntoView({ block: 'nearest' })
        })
        break
      case 'ArrowUp':
        e.preventDefault()
        setHighlightedLangIndex(i => Math.max(i - 1, 0))
        requestAnimationFrame(() => {
          const el = langListRef?.children[highlightedLangIndex()] as HTMLElement | undefined
          el?.scrollIntoView({ block: 'nearest' })
        })
        break
      case 'Enter': {
        e.preventDefault()
        const item = items[highlightedLangIndex()]
        if (item) {
          props.onApply(item.id === 'plaintext' ? '' : item.id)
        }
        break
      }
      case 'Escape':
        popoverRef?.hidePopover()
        break
    }
  }

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
        <div class={styles.comboboxListbox} ref={langListRef}>
          <For each={filteredLanguages()}>
            {(lang, index) => (
              <div
                class={`${styles.comboboxItem} ${index() === highlightedLangIndex() ? styles.comboboxItemHighlighted : ''}`}
                onClick={() => props.onApply(lang.id === 'plaintext' ? '' : lang.id)}
                onMouseEnter={() => setHighlightedLangIndex(index())}
              >
                <span>{highlightMatch(lang.label, props.filter())}</span>
                <span class={styles.comboboxItemCode}>
                  {highlightMatch(lang.id, props.filter())}
                </span>
              </div>
            )}
          </For>
        </div>
        <div class={styles.comboboxControl}>
          <input
            class={styles.comboboxInput}
            placeholder="Filter languages..."
            value={props.filter()}
            onInput={e => props.setFilter(e.currentTarget.value)}
            onKeyDown={handleLangKeyDown}
            ref={(el: HTMLInputElement) => {
              requestAnimationFrame(() => {
                el.focus()
                el.select()
              })
            }}
            data-testid="code-lang-input"
          />
        </div>
      </Show>
    </DropdownMenu>
  )
}
