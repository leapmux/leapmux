// The testids below are WRITTEN by `~/components/common/LoadingMenu` (its
// `data-testid` on the trigger and on each option). The Playwright counterpart
// in `tests/e2e/helpers/ui.ts` encodes the same two templates -- the two cannot
// share query code, because one drives the DOM and the other drives a Locator,
// so a rename has to be applied in all three files. Both pick helpers throw on
// a missing testid, so a drift fails loudly rather than silently.
import { fireEvent, screen, within } from '@solidjs/testing-library'

/**
 * Reading and driving a `DropdownMenu`, for the suites that used to drive a
 * native `<select>`.
 *
 * Two things differ from a `<select>` and both bite silently:
 *
 *   - A menu keeps its items MOUNTED whether or not it is open, so an unscoped
 *     `getAllByRole` returns every menu on screen merged together. Every helper
 *     here scopes to one popover.
 *   - A closed popover is outside the accessibility tree, so role queries need
 *     `hidden: true`. The role is still asserted -- these have to be
 *     `menuitemradio`, which is what makes the group a one-of-N choice.
 */
/**
 * The selector that finds an option's LABEL inside its row.
 *
 * `DropdownMenuCheckableItem` derives the label's test id from the row's own by
 * appending this suffix; the Playwright counterpart is `menuOptionLabel` in
 * `tests/e2e/helpers/ui.ts`. Spelled once on each side.
 */
export const MENU_LABEL_SELECTOR = '[data-testid$="-label"]'

function popover(testId: string): HTMLElement {
  return screen.getByTestId(testId)
}

/**
 * Every option row one menu offers, in order.
 *
 * The one place the role that makes this group a one-of-N choice is spelled, so
 * the three readers below cannot disagree about what an option IS.
 */
function menuItems(testId: string): HTMLElement[] {
  return within(popover(testId)).queryAllByRole('menuitemradio', { hidden: true })
}

/** The option labels one menu offers, in order. */
export function menuOptions(testId: string): string[] {
  return menuItems(testId).map(el => el.textContent?.trim() ?? '')
}

/**
 * The option LABELS one menu offers, in order.
 *
 * Distinct from {@link menuOptions}, which reads the whole row: a row that
 * carries a DETAIL -- the age of a session -- holds that text as well, and an
 * age is a moving target. Use this whenever the assertion is about the label.
 *
 * `DropdownMenuCheckableItem` derives the label's test id from the row's own,
 * which is what makes one suffix match every menu.
 */
export function menuOptionLabels(testId: string): string[] {
  return menuItems(testId).map(el => el.querySelector(MENU_LABEL_SELECTOR)?.textContent?.trim() ?? '')
}

/**
 * Open one menu, as a user does, by clicking its trigger.
 *
 * Required before an assertion about what a ROW draws. `LoadingMenu` defers the
 * expensive part of a row -- its label tooltip, and a caller's live detail
 * element -- to the first open, because `DropdownMenu` mounts its children
 * eagerly and a menu nobody opened would otherwise allocate one Tooltip per
 * option. The rows themselves are always mounted, so `menuOptions`,
 * `menuOptionLabels` and `menuOptionValues` need no open.
 */
export function openMenu(testId: string): void {
  fireEvent.click(menuTrigger(testId))
}

/** Activate the option with this exact label. */
export function pickMenuOption(testId: string, label: string | RegExp): void {
  fireEvent.click(within(popover(testId)).getByRole('menuitemradio', { name: label, hidden: true }))
}

/** Activate the option carrying this value, whatever its label reads. */
export function pickMenuValue(testId: string, value: string): void {
  fireEvent.click(within(popover(testId)).getByTestId(`loading-menu-option-${value}`))
}

/** The menu's trigger button — what the row shows without being opened. */
export function menuTrigger(testId: string): HTMLElement {
  // Its own id, not a descent into the menu: `DropdownMenu` puts the menu's id
  // on the popover and renders the trigger beside it, not inside it.
  return screen.getByTestId(`${testId}-trigger`)
}

/** The label the trigger currently shows: the selected option, or the prompt. */
export function menuTriggerText(testId: string): string {
  return menuTrigger(testId).textContent?.trim() ?? ''
}

/** The option VALUES one menu offers, for a caller that asserted on `<option value>`. */
export function menuOptionValues(testId: string): string[] {
  return menuItems(testId).map(el => el.getAttribute('data-testid')?.replace(/^loading-menu-option-/, '') ?? '')
}
