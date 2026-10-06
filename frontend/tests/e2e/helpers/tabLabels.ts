import type { Page } from '@playwright/test'
import { agentTabs, terminalTabs } from './ui'

/**
 * The parts of a tab that are chrome, not label: the close button, the notification badge, the remote badge, and the
 * progress ring. A closed popover keeps its menu items in the tab, so every `[popover]` is chrome also.
 * One list serves every tab type, so a badge that one type gains cannot leak into the label of that type only.
 */
export const TAB_CHROME_SELECTOR = [
  '[data-testid="tab-close"]',
  '[data-testid="tab-notification"]',
  '[data-testid="tab-remote-badge"]',
  '[data-testid="tab-progress"]',
  '[popover]',
].join(', ')

/**
 * Read the label of each tab, with the chrome removed from a copy of the tab.
 * The page runs this function, so it uses nothing from this module: the caller passes the chrome selector.
 */
export function readTabLabels(tabs: Element[], chrome: string): string[] {
  return tabs.map((tab) => {
    const copy = tab.cloneNode(true) as Element
    copy.querySelectorAll(chrome).forEach(part => part.remove())
    return (copy.textContent ?? '').trim()
  })
}

/**
 * Read the rendered titles of the tabs of one type in the tab bars, in DOM order.
 * Drag and restore specs use them to verify tab metadata.
 */
export async function tabbarLabels(page: Page, tabType: 'agent' | 'terminal'): Promise<string[]> {
  const tabs = tabType === 'agent' ? agentTabs(page) : terminalTabs(page)
  return tabs.evaluateAll(readTabLabels, TAB_CHROME_SELECTOR)
}
