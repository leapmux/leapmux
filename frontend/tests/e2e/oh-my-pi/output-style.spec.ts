import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { ohMyPiTest } from '../ohmypi-fixtures'

const PERSONALITY_MARKERS: Record<string, string> = {
  auto: 'Evidence-first terse engineer',
  default: 'Evidence-first terse engineer',
  friendly: 'Warm, supportive collaborator',
  pragmatic: 'Pragmatic, effective senior engineer',
}

ohMyPiTest('applies native personality instructions and restores the selected output style', async ({ native }) => {
  let initialModel: unknown
  let initialEffort: unknown
  await exerciseNativeOptionSequence(native, {
    groupId: 'outputStyle',
    steps: [
      { value: 'auto', via: 'default' },
      { value: 'friendly', via: 'choose' },
      { value: 'friendly', via: 'reload' },
      { value: 'pragmatic', via: 'choose' },
      { value: 'none', via: 'choose' },
      { value: 'default', via: 'choose' },
      { value: 'auto', via: 'choose' },
    ],
    nativeProof: (request, step, index) => {
      expect(request.protocol).toBe('openai-chat-completions')
      if (!isObject(request.body) || !Array.isArray(request.body.messages))
        throw new Error('The native personality request has no messages.')
      const system = JSON.stringify(request.body.messages.filter(isObject).filter(message => message.role === 'system'))
      if (step.value === 'none') {
        expect(system).not.toContain('# Personality')
        for (const marker of Object.values(PERSONALITY_MARKERS))
          expect(system).not.toContain(marker)
      }
      else {
        expect(system).toContain(PERSONALITY_MARKERS[step.value])
      }
      if (index === 0) {
        initialModel = request.body.model
        initialEffort = request.body.reasoning_effort
      }
      expect(request.body.model).toBe(initialModel)
      expect(request.body.reasoning_effort).toBe(initialEffort)
    },
  })
})
