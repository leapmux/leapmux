const TEXT_ENTRY_INPUT_TYPES = new Set([
  '',
  'text',
  'search',
  'email',
  'password',
  'tel',
  'url',
  'number',
])

function shouldDisableTextSubstitutions(el: Element): el is HTMLInputElement | HTMLTextAreaElement | HTMLElement {
  if (el instanceof HTMLTextAreaElement)
    return true
  if (el instanceof HTMLInputElement)
    return TEXT_ENTRY_INPUT_TYPES.has(el.type.toLowerCase())
  if (!(el instanceof HTMLElement))
    return false
  const attr = el.getAttribute('contenteditable')
  return el.isContentEditable || attr === '' || attr === 'true' || attr === 'plaintext-only'
}

function applyTextSubstitutionAttrs(el: HTMLInputElement | HTMLTextAreaElement | HTMLElement) {
  if (el.getAttribute('autocorrect') !== 'off')
    el.setAttribute('autocorrect', 'off')
  if (el.getAttribute('autocapitalize') !== 'off')
    el.setAttribute('autocapitalize', 'off')
  if (el.spellcheck !== false)
    el.spellcheck = false
}

export function disableTextSubstitutions(root: ParentNode = document) {
  const nodes = root instanceof Element && shouldDisableTextSubstitutions(root)
    ? [root]
    : [...root.querySelectorAll('input, textarea, [contenteditable=""], [contenteditable="true"], [contenteditable="plaintext-only"]')]

  for (const node of nodes) {
    if (shouldDisableTextSubstitutions(node))
      applyTextSubstitutionAttrs(node)
  }
}
