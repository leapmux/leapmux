import { onCleanup, onMount } from 'solid-js'
import { isTypingElement } from '~/lib/textInputBehavior'
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

    // `writable()` decides it, not the mere presence of a ref: a read-only
    // subagent's composer is mounted and registered, merely disabled. Returning
    // here rather than calling the guarded no-op keeps preventDefault honest --
    // the handler must not report a key as handled when it dropped it.
    const ref = getEditorRef(agentId)
    if (!ref?.writable())
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

  // The same predicate the shortcut system reads, so a key never lands in
  // the chat editor and in the element the user is typing in.
  if (isTypingElement(el))
    return true

  if (el.closest('dialog[open], [popover]:popover-open, [role="menu"], [role="listbox"], [role="dialog"]'))
    return true

  return false
}
