/**
 * Muse serves the model its settings pin: `model/list` holds that row alone under
 * the local mock (the bundled catalog's other routes surface only through the real
 * provider), so the provable selection is the pinned model itself. Every native
 * model request carries it, the catalog row stays chosen, and a reload keeps both.
 */
import { expect } from '@playwright/test'
import { currentNativeAgent, expectNativeOptionValue, nativeOptionGroup, nativeTextStep } from '../helpers/nativeScenario'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

const PINNED_MODEL = 'muse-spark-1.2'

museTest('keeps the pinned native model on every request and after reload', async ({ native }) => {
  const { page, modelScript } = native
  const step = await modelScript.queue(nativeTextStep(native, 'The pinned model answered.'))
  await sendMessage(page, modelScript.prompt('Reply once to prove the model route.'))
  await modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(page)
  const request = await modelScript.requestAt(step)
  expect(request.protocol).toBe('openai-responses')
  expect(request.path).toBe('/v1/responses')
  expect(request.body).toHaveProperty('model', PINNED_MODEL)
  expect(request.mockCredential?.accepted).toBe(true)

  const agent = await currentNativeAgent(native)
  const group = nativeOptionGroup(agent, 'model')
  expect(group?.options.map(option => option.id)).toEqual([PINNED_MODEL])
  await expectNativeOptionValue(native, 'model', PINNED_MODEL)

  await page.reload()
  await waitForAgentIdle(page)
  await expectNativeOptionValue(native, 'model', PINNED_MODEL)
  const again = await modelScript.queue(nativeTextStep(native, 'The pinned model answered again.'))
  await sendMessage(page, modelScript.prompt('Reply once more after the reload.'))
  await modelScript.waitForSteps(again + 1)
  await waitForAgentIdle(page)
  expect((await modelScript.requestAt(again)).body).toHaveProperty('model', PINNED_MODEL)
})
