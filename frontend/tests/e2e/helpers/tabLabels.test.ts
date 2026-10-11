import type { Page } from '@playwright/test'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readTabLabels, TAB_CHROME_SELECTOR, tabbarLabels } from './tabLabels'
import { agentTabs, terminalTabs } from './ui'

vi.mock('./ui', () => ({ agentTabs: vi.fn(), terminalTabs: vi.fn() }))

afterEach(() => {
  document.body.replaceChildren()
})

/** A tab element with the label `label` and every piece of chrome that a tab can carry. */
function tabWithChrome(label: string): Element {
  const tab = document.createElement('div')
  tab.dataset.testid = 'tab'
  tab.innerHTML = `
    <span>${label}</span>
    <span data-testid="tab-notification">1</span>
    <span data-testid="tab-progress">42%</span>
    <span data-testid="tab-remote-badge">remote</span>
    <button data-testid="tab-close">x</button>
    <menu popover><li>Rename</li><li>Close</li></menu>
  `
  document.body.append(tab)
  return tab
}

describe('readTabLabels', () => {
  it('reads each label without the close button, the badges, the progress ring, or a closed menu', () => {
    const tabs = [tabWithChrome('Terminal Orca'), tabWithChrome('Agent Heron')]
    expect(readTabLabels(tabs, TAB_CHROME_SELECTOR)).toEqual(['Terminal Orca', 'Agent Heron'])
  })

  it('leaves the tabs of the page unchanged', () => {
    const tab = tabWithChrome('Terminal Orca')
    readTabLabels([tab], TAB_CHROME_SELECTOR)
    expect(tab.querySelector('[data-testid="tab-close"]')).not.toBeNull()
    expect(tab.querySelector('[popover]')).not.toBeNull()
  })

  it('reads a tab with no label as the empty string', () => {
    expect(readTabLabels([tabWithChrome('')], TAB_CHROME_SELECTOR)).toEqual([''])
  })

  it('reads no tab as no label', () => {
    expect(readTabLabels([], TAB_CHROME_SELECTOR)).toEqual([])
  })
})

describe('tabbarLabels', () => {
  it.each([
    { tabType: 'agent' as const, locate: agentTabs },
    { tabType: 'terminal' as const, locate: terminalTabs },
  ])('reads the $tabType tabs through the shared reader and chrome list', async ({ tabType, locate }) => {
    const tab = tabWithChrome('Renamed')
    const evaluateAll = vi.fn(async (body: typeof readTabLabels, chrome: string) => body([tab], chrome))
    vi.mocked(locate).mockReturnValue({ evaluateAll } as unknown as ReturnType<typeof agentTabs>)
    const page = {} as Page
    await expect(tabbarLabels(page, tabType)).resolves.toEqual(['Renamed'])
    expect(locate).toHaveBeenCalledWith(page)
    expect(evaluateAll).toHaveBeenCalledWith(readTabLabels, TAB_CHROME_SELECTOR)
  })
})
