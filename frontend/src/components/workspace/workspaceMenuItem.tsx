import type { JSX } from 'solid-js'

/**
 * One plain command row of a workspace menu.
 *
 * The row menu renders this shape in six places -- three tab-creation shapes
 * and three repository actions -- across two components, so the markup is
 * written once here. A row inside a `<For>` carries no test id: the popover it
 * sits in already has one, and one id per repository would be a selector
 * nobody can predict.
 */
export function menuItem(label: string, onClick: () => void, testId?: string): JSX.Element {
  return (
    <button type="button" role="menuitem" data-testid={testId} onClick={onClick}>
      {label}
    </button>
  )
}
