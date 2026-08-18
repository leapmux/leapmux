/**
 * What counts as a text-entry element, and the pass that turns the
 * platform's text substitutions off in each one.
 *
 * The predicates live together because they answer one question at three
 * widths, and a copy of any of them that reads `contenteditable === 'true'`
 * alone gets two of the three editable spellings wrong.
 */

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

/**
 * The CSS fragment that matches an editing host, whichever spelling of the
 * attribute it carries.
 *
 * The selector form of `isContentEditableElement` below, for the call sites
 * that ask about an element TREE rather than one element: a `closest()` that
 * walks up from an event target, and the `querySelectorAll` sweep at the foot
 * of this file. Interpolate it; never restate the three spellings, because a
 * list that names `true` alone misses the two that also enable editing.
 *
 * `closest()` needs no `isContentEditable` fallback. Editing is inherited
 * from the host, and the host is the ancestor that carries the attribute, so
 * walking up finds it from any descendant the press landed on.
 *
 * Every caller outside this file reaches this fragment through
 * `INPUT_OR_EDITABLE_SELECTOR` below, which is the wider group all of them
 * want. It stays exported on its own for the test that holds it to the same
 * three spellings the predicate accepts.
 */
export const CONTENT_EDITABLE_SELECTOR = '[contenteditable=""], [contenteditable="true"], [contenteditable="plaintext-only"]'

/**
 * The CSS fragment that matches an input, a textarea, or an editing host.
 *
 * The shared core of three pointer-gesture guards and of the sweep in
 * `disableTextSubstitutions` below. A press that lands inside one of these
 * elements belongs to the element, not to the gesture around it. State a new
 * member of the group here once, and every caller gets it -- a member that
 * reaches one guard alone is the defect this fragment prevents.
 *
 * The fragment matches an INPUT whatever its `type`, so a checkbox and a
 * range slider match too. `TEXT_ENTRY_INPUT_TYPES` above narrows that set,
 * and `shouldDisableTextSubstitutions` is the one caller that applies the
 * narrowing, because autocorrect has nothing to act on in a checkbox. A
 * pointer gesture wants the wide form: a press on any INPUT belongs to that
 * control.
 *
 * Each gesture composes this fragment into an `EMBEDDED_UI_SELECTOR` of its
 * own and adds what it alone declines. Those three lists must NOT merge into
 * one, and they must not push their own additions down into this fragment.
 * See ~/components/common/contextMenuGesture.ts,
 * ~/components/shell/guardedPointerSensor.ts and ~/lib/dragActivators.ts --
 * each one states the behavior that a merge would break.
 *
 * All three lists also carry `[popover]`, and each one states that token
 * itself rather than take it from here. A single self-describing token gains
 * nothing from a name, and unlike `contenteditable` it has no second spelling
 * to get wrong -- which is the whole reason the editable fragment exists.
 */
export const INPUT_OR_EDITABLE_SELECTOR = `input, textarea, ${CONTENT_EDITABLE_SELECTOR}`

/**
 * Whether `el` is an editing host, or sits inside one.
 *
 * Three spellings of the attribute enable editing: `true`, `""` (the HTML
 * shorthand for the true state), and `plaintext-only`. `isContentEditable`
 * reports all three, and it alone reports an element that INHERITS editing
 * from an editable ancestor, which no attribute on the element itself
 * shows. jsdom implements neither `isContentEditable` nor `contentEditable`,
 * so the attribute comparison stays as the fallback that keeps the answer
 * honest under the unit tests.
 */
export function isContentEditableElement(el: Element): boolean {
  if (el instanceof HTMLElement && el.isContentEditable)
    return true
  const attr = el.getAttribute('contenteditable')
  return attr === '' || attr === 'true' || attr === 'plaintext-only'
}

/**
 * Whether `el` consumes a typed key.
 *
 * Wider than "takes text": a SELECT holds no text, but the key belongs to
 * its own type-ahead, so a single-key shortcut must not fire while it has
 * focus. `shouldDisableTextSubstitutions` below asks the narrower question,
 * because autocorrect has nothing to correct in a SELECT or a checkbox.
 */
export function isTypingElement(el: Element | null): boolean {
  if (!el)
    return false
  const tag = el.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT')
    return true
  return isContentEditableElement(el)
}

/**
 * Whether focus currently sits in an element that consumes a typed key.
 *
 * The predicate, not the registered `inputFocused` shortcut context,
 * because a component that owns a single-key shortcut of its own must
 * answer the same question WITHOUT the shortcut system: the preferences
 * search box binds `/` on its own and renders standalone, where no lazy
 * context is registered and `getContext('inputFocused')` would answer
 * undefined — so the box would steal the slash out of another input.
 */
export function isTypingContext(): boolean {
  return isTypingElement(document.activeElement)
}

/**
 * Whether the platform's text substitutions apply to `el`.
 *
 * Narrower than `isTypingElement`: only an element that holds free text has
 * something for autocorrect, autocapitalize and the spell checker to act
 * on, so a SELECT and a checkbox are out.
 */
function shouldDisableTextSubstitutions(el: Element): el is HTMLInputElement | HTMLTextAreaElement | HTMLElement {
  if (el instanceof HTMLTextAreaElement)
    return true
  if (el instanceof HTMLInputElement)
    return TEXT_ENTRY_INPUT_TYPES.has(el.type.toLowerCase())
  if (!(el instanceof HTMLElement))
    return false
  return isContentEditableElement(el)
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
    : [...root.querySelectorAll(INPUT_OR_EDITABLE_SELECTOR)]

  for (const node of nodes) {
    if (shouldDisableTextSubstitutions(node))
      applyTextSubstitutionAttrs(node)
  }
}
