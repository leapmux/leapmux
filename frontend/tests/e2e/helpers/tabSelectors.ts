import { cssAttributeValue } from './cssAttribute'

/**
 * The CSS selectors of the tab bar that more than one helper layer needs.
 *
 * This module imports only ./cssAttribute.ts, which imports nothing. ./nativeScenario.ts sits below ./ui.ts, because
 * ./ui.ts imports it, so a selector that both modules need lives here. A `page.evaluate` body, which cannot take a
 * locator, takes a selector as an argument.
 */

/** The CSS selector of an agent tab. */
export const AGENT_TAB_SELECTOR = '[data-testid="tab"][data-tab-type="agent"]'

/**
 * The CSS selector of each element that carries the tab ID `tabId`: a tab of a tab bar, and a leaf of the sidebar tree.
 * The selector escapes the ID, so an ID with a quote, a backslash, or a line break selects its own element only.
 */
export function tabIdSelector(tabId: string): string {
  return `[data-tab-id="${cssAttributeValue(tabId)}"]`
}
