import type { Page } from '@playwright/test'
import type { AmpThreadView } from '../helpers/ampSurface'
import { realpathSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { AMP_E2E_THREADS_PATH } from '../helpers/ampSurface'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, closeComposerMenus, expectAssistantAnswer, expectSettingsChip, openSettingsMenu, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

/**
 * The mode choice must reach the actual native session. The browser must follow native changes and refusal limits.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 *
 * Amp's mode selects its model and effort together. The first message fixes the mode. The Worker starts no Amp process before that message. Later choices explain that another mode needs a new session.
 */
/** The mock's record of the thread that an agent in `workingDir` created. */
async function threadIn(mockModelUrl: string, workingDir: string): Promise<AmpThreadView | undefined> {
  const tree = pathToFileURL(realpathSync(workingDir)).href
  const threads = await (await fetch(`${mockModelUrl}${AMP_E2E_THREADS_PATH}`)).json() as AmpThreadView[]
  return threads.find(thread => thread.tree === tree)
}

/** The option ids the mode group offers. */
async function modeOptions(page: Page): Promise<string[]> {
  const group = await openSettingsMenu(page, 'agent_mode')
  // Each option also holds a label element whose id ends `-label`.
  const ids = await group.locator('[data-testid^="agent_mode-"]:not([data-testid$="-label"])').evaluateAll(nodes => nodes.map(node => node.getAttribute('data-testid') ?? ''))
  await closeComposerMenus(page)
  return ids
}

ampTest('starts the thread in the mode chosen before the first message, and keeps it after', async ({ authenticatedAmpWorkspace, page, modelScript, leapmuxServer }) => {
  // Amp's default mode, and the four modes of its Dial.
  await expectSettingsChip(page, 'Medium')
  expect(await modeOptions(page)).toEqual(['agent_mode-low', 'agent_mode-medium', 'agent_mode-high', 'agent_mode-ultra'])

  await chooseSettingsOption(page, 'agent_mode-high')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'High')

  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)

  // The thread started in the chosen mode.
  await expect.poll(async () => (await threadIn(leapmuxServer.mockModelUrl, authenticatedAmpWorkspace.workingDir))?.agentMode).toBe('high')

  // The thread keeps its mode, so the group offers that mode alone, and says why.
  await expect.poll(() => modeOptions(page)).toEqual(['agent_mode-high'])
  // The reason is the read-only option's tooltip.
  const group = await openSettingsMenu(page, 'agent_mode')
  await group.getByTestId('agent_mode-high').hover()
  await expect(page.getByRole('tooltip').filter({ visible: true })).toContainText('Start a new session for another mode')
  await closeComposerMenus(page)
  await expectSettingsChip(page, 'High')

  await page.reload()
  await expectSettingsChip(page, 'High')
  expect(await modeOptions(page)).toEqual(['agent_mode-high'])

  // The next message continues the same thread in the same mode after reload.
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const thread = await threadIn(leapmuxServer.mockModelUrl, authenticatedAmpWorkspace.workingDir)
  expect(thread?.agentMode).toBe('high')
  expect(thread?.messageCount).toBe(4)
})
