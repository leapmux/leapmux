import { onCleanup, onMount } from 'solid-js'
import { getEditorRef } from '~/stores/editorRef.store'

export function useChatAutoFocus(getFocusedAgentId: () => string | null): void {
  function handleKeyDown(e: KeyboardEvent) {
    // Skip modifier combos (shortcuts like Ctrl+C, Cmd+V, Alt+p)
    if (e.ctrlKey || e.metaKey || e.altKey)
      return

    // Only handle single printable characters (filters out Escape, Tab,
    // Enter, Backspace, Delete, ArrowUp, F1, Shift, Control, Alt, etc.)
    if (e.key.length !== 1)
      return

    // Skip space to avoid hijacking scroll-with-space behavior
    if (e.key === ' ')
      return

    // Don't steal focus from meaningful interactive elements
    if (isMeaningfulElement(document.activeElement))
      return

    // Only auto-focus when an agent tab is active
    const agentId = getFocusedAgentId()
    if (!agentId)
      return

    const ref = getEditorRef(agentId)
    if (!ref)
      return

    ref.focus()
    ref.insert(e.key)
    e.preventDefault()
  }

  onMount(() => {
    document.addEventListener('keydown', handleKeyDown)
    onCleanup(() => document.removeEventListener('keydown', handleKeyDown))
  })
}

/** Returns true if the element is an interactive control that should retain focus. */
function isMeaningfulElement(el: Element | null): boolean {
  if (!el)
    return false

  const tag = el.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT')
    return true

  if (el.getAttribute('contenteditable') === 'true')
    return true

  if (el.closest('dialog[open], [popover]:popover-open, [role="menu"], [role="listbox"], [role="dialog"]'))
    return true

  return false
}
