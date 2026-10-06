import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest } from '../claude-fixtures'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'

/** The instruction that each output style puts after the last user message. Null states no style instruction. */
const STYLE_MARKERS = [
  null,
  '# Explanatory Style Active',
  '# Explanatory Style Active',
  '# Learning Style Active',
  'The output style was reset to the default.',
] as const

claudeTest('applies native output-style instructions and restores the selected style', async ({ native }) => {
  let initialModel: unknown
  let initialEffort: unknown
  await exerciseNativeOptionSequence(native, {
    groupId: 'outputStyle',
    steps: [
      { value: 'default', via: 'choose' },
      { value: 'Explanatory', via: 'choose' },
      { value: 'Explanatory', via: 'reload' },
      { value: 'Learning', via: 'choose' },
      { value: 'default', via: 'choose' },
    ],
    nativeProof: (request, step, index) => {
      if (!isObject(request.body) || !Array.isArray(request.body.messages))
        throw new Error('The native output-style request has no messages.')
      const messages = request.body.messages.filter(isObject)
      const lastUser = messages.findLastIndex(message => message.role === 'user')
      expect(lastUser).toBeGreaterThanOrEqual(0)
      const styleMessages = messages.filter(message => message.role === 'system' && /Style Active|output style was reset/.test(JSON.stringify(message)))
      const instructions = step.via === 'reload'
        ? JSON.stringify(styleMessages.at(-1)) ?? ''
        : JSON.stringify(messages.slice(lastUser + 1).filter(message => message.role === 'system'))
      const marker = STYLE_MARKERS[index]
      if (marker === undefined)
        throw new Error(`The output-style sequence has no marker for step ${index}.`)
      if (marker)
        expect(instructions).toContain(marker)
      else
        expect(instructions).not.toContain('Style Active')
      const effort = isObject(request.body.output_config) ? request.body.output_config.effort : undefined
      if (index === 0) {
        expect(request.protocol).toBe('anthropic-messages')
        expect(request.body.model).toEqual(expect.stringMatching(/^claude-/))
        initialModel = request.body.model
        initialEffort = effort
      }
      expect(request.body.model).toBe(initialModel)
      expect(effort).toBe(initialEffort)
    },
  })
})
