import type { NativeSessionIdentity } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { COPILOT_OPTION } from '../../../src/generated/contracts/copilot-protocol'
import { copilotTest } from '../copilot-fixtures'
import { currentNativeAgent, expectSameNativeSession, nativeModelLastUserText, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseNativeOptionSequence, expectSettingsOptionsOffered } from '../helpers/nativeSettings'

/** The instruction that Copilot's fleet request puts before the prompt of the turn. */
const FLEET_INSTRUCTION = 'You are now in fleet mode. Dispatch sub-agents (via the task tool) in parallel'

// Copilot applies fleet mode to one request at a time (`session.fleet.start`, and `copilot --fleet` for its prompt).
// LeapMux's Fleet setting therefore sends each new turn as a fleet request while it is on, in the same session.
copilotTest('sends each new turn as a fleet request while Fleet is on, across reload', async ({ native }) => {
  await expectSettingsOptionsOffered(native.page, COPILOT_OPTION.Fleet, ['false', 'true'])
  let first: NativeSessionIdentity | undefined
  await exerciseNativeOptionSequence(native, {
    groupId: COPILOT_OPTION.Fleet,
    steps: [
      { value: 'false', via: 'default' },
      { value: 'true', via: 'choose' },
      { value: 'false', via: 'choose' },
      { value: 'true', via: 'choose' },
      { value: 'true', via: 'reload' },
    ],
    nativeProof: async (request, step, index) => {
      const prompt = nativeModelLastUserText(request)
      expect(prompt, 'the prompt of this turn reaches the model').toContain(`step ${index}`)
      if (step.value === 'true')
        expect(prompt).toContain(FLEET_INSTRUCTION)
      else
        expect(prompt).not.toContain(FLEET_INSTRUCTION)
      const current = await currentNativeAgent(native)
      first ??= current
      expectSameNativeSession(first, current, 'Changing Fleet')
      expect(nativeOptionValue(current, COPILOT_OPTION.Fleet)).toBe(step.value)
    },
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/523.
