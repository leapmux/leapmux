import { onCleanup, onMount } from 'solid-js'
import { getEditorRef } from '~/stores/editorRef.store'

export function useChatAutoFocus(getFocusedAgentId: () => string | null): void {
  function handleKeyDown(e: KeyboardEvent) {
    if (e.ctrlKey || e.metaKey || e.altKey)
      return
    if (e.key.length !== 1)
      return
    // Preserve scroll-with-space behavior
    if (e.key === ' ')
      return
    if (isMeaningfulElement(document.activeElement))
      return

    const agentId = getFocusedAgentId()
    if (!agentId)
      return

    const ref = getEditorRef(agentId)
    if (!ref)
      return

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
