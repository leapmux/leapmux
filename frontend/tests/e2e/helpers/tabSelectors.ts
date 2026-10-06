/**
 * The CSS selectors of the tab bar that more than one helper layer needs.
 *
 * This module imports nothing. ./nativeScenario.ts sits below ./ui.ts, because ./ui.ts imports it, so a selector that
 * both modules need lives here. A `page.evaluate` body, which cannot take a locator, takes a selector as an argument.
 */

/** The CSS selector of an agent tab. */
export const AGENT_TAB_SELECTOR = '[data-testid="tab"][data-tab-type="agent"]'
