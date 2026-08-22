// Shared DOM fixtures for the pointer-gesture guards. Each of them composes
// `INPUT_OR_EDITABLE_SELECTOR` (see ~/lib/textInputBehavior.ts) into a
// selector of its own and declines a press that starts inside one of these
// elements.
//
// The membership lives here once rather than in hand-copied lists. A member
// dropped from the shared fragment then fails every spec that uses these
// hosts, instead of the one spec that happened to name it.

/** An element a guard must decline, and the descendant a real press lands on. */
export interface EmbeddedUiHost {
  /** Names the member in an assertion message. */
  label: string
  /** The element the selector matches. Append it to the row under test. */
  host: HTMLElement
  /** Where the press lands: `host` itself, or a descendant of it. */
  target: Element
}

/**
 * One host for each member of `INPUT_OR_EDITABLE_SELECTOR`, in build order:
 * an input, a textarea, and an editing host in each of the three spellings.
 *
 * Mirrors that fragment and nothing else. A member that belongs to one guard
 * alone -- `select`, `button` and `[data-drag-handle]` are
 * ~/lib/dragActivators.ts's own -- stays in that guard's spec, so the split
 * between shared and tuned reads the same way in the tests as in the code.
 *
 * Every guard asks with `closest()`, so an editing host hands back a
 * descendant as the target: a press inside a real editor lands on the text
 * node's element, not on the host that carries the attribute.
 */
export function inputOrEditableHosts(): EmbeddedUiHost[] {
  const hosts: EmbeddedUiHost[] = []

  for (const tag of ['input', 'textarea'] as const) {
    const host = document.createElement(tag)
    hosts.push({ label: `<${tag}>`, host, target: host })
  }

  // The three spellings that enable editing. A guard that names `true` alone
  // misses the two below it.
  for (const spelling of ['', 'true', 'plaintext-only']) {
    const host = document.createElement('div')
    host.setAttribute('contenteditable', spelling)
    const inner = document.createElement('span')
    host.appendChild(inner)
    hosts.push({ label: `contenteditable="${spelling}"`, host, target: inner })
  }

  return hosts
}

/**
 * An open menu popover holding one item, as a row renders it.
 *
 * `[popover]` sits in all three guards, and in none of them through the shared
 * fragment: it is one self-describing token that each list states itself. The
 * press lands on the item, which is what a user taps.
 */
export function popoverHost(): EmbeddedUiHost {
  const host = document.createElement('menu')
  host.setAttribute('popover', 'auto')
  const item = document.createElement('button')
  host.appendChild(item)
  return { label: '[popover]', host, target: item }
}
