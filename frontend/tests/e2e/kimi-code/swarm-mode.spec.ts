import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { kimiTest } from '../kimi-fixtures'
import { kimiModelContextText } from './modelContextText'

/** The reminder that Kimi Code puts into the context while swarm mode is on. */
const SWARM_REMINDER = 'You are now in "agent swarm" mode.'

/** The reminder that Kimi Code puts into the context when swarm mode ends. */
const SWARM_EXIT_REMINDER = 'Swarm Mode has ended.'

kimiTest('applies independent swarm mode to native context and preserves it after reload', async ({ native }) => {
  // The context reader joins the raw message text: the generic JSON reader escapes the quotes of the native
  // reminder, so the swarm reminder would never match.
  let restored: MockModelRequestRecord | undefined
  await exerciseNativeOptionSequence(native, {
    groupId: 'swarmMode',
    steps: [
      { value: 'on', via: 'choose' },
      { value: 'on', via: 'reload' },
      { value: 'off', via: 'choose' },
    ],
    nativeProof: (request, step) => {
      if (step.value === 'on') {
        expect(kimiModelContextText(request)).toContain(SWARM_REMINDER)
        restored = request
        return
      }
      if (!restored)
        throw new Error('The swarm-mode sequence ended swarm mode before a turn in swarm mode.')
      // The turn after the change holds one exit reminder more than the turn before it.
      expect(kimiModelContextText(request).split(SWARM_EXIT_REMINDER).length).toBeGreaterThan(kimiModelContextText(restored).split(SWARM_EXIT_REMINDER).length)
    },
  })
})
