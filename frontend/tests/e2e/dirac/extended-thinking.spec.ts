import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { expectSettingsOptionChosen } from '../helpers/ui'
import { anthropicDiracTest } from './fixtures'
import { nativeContext } from './scenarios'

anthropicDiracTest('enables and disables native thinking with the model and effort fixed', async ({ anthropicDiracWorkspace, page, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer: anthropicDiracWorkspace.server, workspaceId: anthropicDiracWorkspace.workspaceId })
  await exerciseNativeOptionSequence(context, {
    groupId: 'thinking_budget',
    steps: [
      { value: '1024', via: 'choose' },
      { value: '0', via: 'choose' },
      { value: '0', via: 'reload' },
      // The next request with thinking on proves that the switch kept the native effort.
      { value: '1024', via: 'choose' },
    ],
    nativeProof: async (request, step) => {
      expect(request.protocol).toBe('anthropic-messages')
      if (step.value === '1024') {
        expect(request.body).toMatchObject({
          model: 'claude-haiku-4-5-20251001',
          thinking: { type: 'adaptive' },
          output_config: { effort: 'medium' },
        })
        return
      }
      expect(request.body).toMatchObject({ model: 'claude-haiku-4-5-20251001' })
      if (!isObject(request.body))
        throw new Error('The native thinking request is not an object.')
      // Dirac 0.5.17 (AnthropicHandler.createMessage) sends the effort only as
      // `output_config.effort`, and only while thinking is on. A request without
      // thinking therefore states no effort, and the menu keeps the effort.
      expect(request.body).not.toHaveProperty('thinking')
      expect(request.body).not.toHaveProperty('output_config')
      await expectSettingsOptionChosen(page, 'reasoning_effort-medium')
    },
  })
  expect(anthropicDiracWorkspace.agentId).not.toBe('')
})
