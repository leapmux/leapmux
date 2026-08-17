import type { Component } from 'solid-js'
import { onCleanup, onMount } from 'solid-js'
import { isTypingContext } from '~/lib/textInputBehavior'
import * as styles from './PreferencesDialog.css'

export interface PreferencesSearchProps {
  query: string
  onQuery: (query: string) => void
}

/**
 * The preferences search box.
 *
 * `/` focuses it from anywhere in the dialog (unless focus is already in an
 * input), and Escape clears the query before it closes the dialog — the
 * ordinary "Escape backs out one level" contract, with the search text as the
 * innermost level.
 */
export const PreferencesSearch: Component<PreferencesSearchProps> = (props) => {
  let inputRef: HTMLInputElement | undefined

  const handleGlobalKeydown = (e: KeyboardEvent) => {
    if (e.key === '/' && !isTypingContext() && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault()
      inputRef?.focus()
    }
  }

  onMount(() => {
    document.addEventListener('keydown', handleGlobalKeydown)
    onCleanup(() => document.removeEventListener('keydown', handleGlobalKeydown))
  })

  return (
    <input
      ref={inputRef}
      type="search"
      class={styles.searchInput}
      placeholder="Search settings (press /)"
      aria-label="Search settings"
      data-testid="preferences-search"
      value={props.query}
      onInput={e => props.onQuery(e.currentTarget.value)}
      onKeyDown={(e) => {
        if (e.key === 'Escape' && props.query !== '') {
          // Clear the search first; only an EMPTY query lets Escape through
          // to the dialog's close handler.
          e.preventDefault()
          e.stopPropagation()
          props.onQuery('')
        }
      }}
    />
  )
}
