import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { currentNativeAgent, nativeOptionGroup, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { waitForSettingsHydrated } from '../helpers/ui'
import { anthropicDiracTest } from './fixtures'
import { nativeContext } from './scenarios'

anthropicDiracTest.use({ anthropicModel: 'claude-opus-5' })

anthropicDiracTest('enables and disables native fast serving with the model and effort fixed', async ({ anthropicDiracWorkspace, page, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer: anthropicDiracWorkspace.server, workspaceId: anthropicDiracWorkspace.workspaceId })
  await waitForSettingsHydrated(page)
  const before = await currentNativeAgent(context)
  const effort = nativeOptionGroup(before, 'reasoning_effort') ?? nativeOptionGroup(before, 'effort')
  if (!effort || !effort.currentValue)
    throw new Error('The native Dirac effort catalog is absent.')
  expect(effort.currentValue).toBe('medium')
  await exerciseNativeOptionSequence(context, {
    groupId: 'inference_speed',
    steps: [
      { value: 'fast', via: 'choose' },
      { value: 'standard', via: 'choose' },
      { value: 'standard', via: 'reload' },
    ],
    nativeProof: async (request, step) => {
      expect(request.protocol).toBe('anthropic-messages')
      expect(request.body).toMatchObject({ model: 'claude-opus-5' })
      if (!isObject(request.body))
        throw new Error('The native speed request is not an object.')
      if (step.value === 'fast')
        expect(request.body.speed).toBe('fast')
      else
        expect(request.body).not.toHaveProperty('speed')
      expect(nativeOptionValue(await currentNativeAgent(context), effort.id)).toBe(effort.currentValue)
    },
  })
})
