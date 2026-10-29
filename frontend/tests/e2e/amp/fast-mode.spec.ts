import type { AmpThreadView } from '../helpers/ampSurface'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { AMP_E2E_THREADS_PATH } from '../helpers/ampSurface'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { getGlobalState } from '../helpers/server'
import { chooseSettingsOption, closeComposerMenus, expectSettingsOptionChosen, offeredSettingsOptions, openSettingsMenu, waitForSettingsIdle } from '../helpers/ui'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { AMP_AGENT, nativeContext } from './scenarios'

async function expectNativeSpeed(context: ManagedNativeScenarioContext, fast: boolean): Promise<string> {
  const current = await currentNativeAgent(context)
  expect(current.agentSessionId).not.toBe('')
  const response = await fetch(`${getGlobalState().mockModelUrl}${AMP_E2E_THREADS_PATH}`)
  expect(response.ok).toBe(true)
  const threads = await response.json() as AmpThreadView[]
  const thread = threads.find(thread => thread.id === current.agentSessionId)
  expect(thread).toBeDefined()
  expect(thread?.agentMode).toBe('high')
  expect(thread?.features).toEqual(fast
    ? ['fast']
    : [])
  await expectSettingsOptionChosen(context.page, `fastMode-${fast}`)
  expect(await offeredSettingsOptions(context.page, 'fastMode')).toEqual([
    String(fast),
  ])
  const group = await openSettingsMenu(context.page, 'fastMode')
  const option = group.getByTestId(`fastMode-${fast}`)
  await expect(option).toBeDisabled()
  await option.hover()
  await expect(context.page.getByRole('tooltip').filter({ visible: true })).toContainText('Start a new session to change Fast Mode')
  await closeComposerMenus(context.page)
  return current.agentSessionId
}

for (const fast of [false, true]) {
  ampTest(`creates native threads with Fast Mode ${fast ? 'on' : 'off'} and retains the choice on resume`, async ({ native }) => {
    const { page } = native
    expect(await offeredSettingsOptions(page, 'fastMode')).toEqual(['false', 'true'])
    await chooseSettingsOption(page, 'agent_mode-high')
    await waitForSettingsIdle(page)
    // Exercise both directions before creation, including an explicit Off choice.
    await chooseSettingsOption(page, 'fastMode-true')
    await waitForSettingsIdle(page)
    if (!fast) {
      await chooseSettingsOption(page, 'fastMode-false')
      await waitForSettingsIdle(page)
    }
    await expectSettingsOptionChosen(page, `fastMode-${fast}`)
    await exerciseSessionResume(native, {
      resumeEvidence: async ({ phase, prior }) => {
        expect(await expectNativeSpeed(native, fast)).toBe(prior.agentSessionId)
        if (phase === 'continued') {
          await page.reload()
          expect(await expectNativeSpeed(native, fast)).toBe(prior.agentSessionId)
          await sendNativeAnswer(native, 'Keep this thread and return the reload marker.', 'AMP_FAST_RELOAD')
          expect(await expectNativeSpeed(native, fast)).toBe(prior.agentSessionId)
        }
      },
    })
  })
}

const restartTest = createProviderProcessTest({
  agent: AMP_AGENT,
  nativeContext,
  skipReason: AMP_E2E_SKIP_REASON,
  openOptions: {
    optionValues: { agent_mode: 'high', fastMode: 'true' },
  },
})

restartTest('reads native Fast Mode after a worker restart', async ({ native, separateHubWorker }) => {
  await exerciseWorkerRestart(native, separateHubWorker, 'worker')
  await expectNativeSpeed(native, true)
})
