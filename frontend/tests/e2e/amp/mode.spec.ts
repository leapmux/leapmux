import type { AmpThreadView } from '../helpers/ampSurface'
import { realpathSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { AMP_E2E_THREADS_PATH } from '../helpers/ampSurface'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, closeComposerMenus, expectAssistantAnswer, expectSettingsChip, offeredSettingsOptions, openSettingsMenu, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

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

ampTest('starts the thread in the mode chosen before the first message, and keeps it after', async ({ authenticatedAmpWorkspace, page, modelScript, leapmuxServer }) => {
  // Amp's default mode, and the four modes of its Dial.
  await expectSettingsChip(page, 'Medium')
  expect(await offeredSettingsOptions(page, 'agent_mode')).toEqual(['low', 'medium', 'high', 'ultra'])

  await chooseSettingsOption(page, 'agent_mode-high')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'High')

  const first = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps(first + 1)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)

  // The thread started in the chosen mode.
  await expect.poll(async () => (await threadIn(leapmuxServer.mockModelUrl, authenticatedAmpWorkspace.workingDir))?.agentMode).toBe('high')

  // The thread keeps its mode, so the group offers that mode alone, and says why.
  await expect.poll(() => offeredSettingsOptions(page, 'agent_mode')).toEqual(['high'])
  // The reason is the read-only option's tooltip.
  const group = await openSettingsMenu(page, 'agent_mode')
  await group.getByTestId('agent_mode-high').hover()
  await expect(page.getByRole('tooltip').filter({ visible: true })).toContainText('Start a new session for another mode')
  await closeComposerMenus(page)
  await expectSettingsChip(page, 'High')

  await page.reload()
  await expectSettingsChip(page, 'High')
  expect(await offeredSettingsOptions(page, 'agent_mode')).toEqual(['high'])

  // The next message continues the same thread in the same mode after reload.
  const next = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps(next + 1)
  await waitForAgentIdle(page)
  const thread = await threadIn(leapmuxServer.mockModelUrl, authenticatedAmpWorkspace.workingDir)
  expect(thread?.agentMode).toBe('high')
  expect(thread?.messageCount).toBe(4)
})
