import { expect } from '@playwright/test'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { kimiTest } from '../kimi-fixtures'
import { kimiModelContextText } from './modelContextText'

/** The reminder that Kimi Code puts into the context while swarm mode is on. */
const SWARM_REMINDER = 'You are now in "agent swarm" mode.'

/** Kimi 2.6 no longer emits this exit reminder. */
const SWARM_EXIT_REMINDER = 'Swarm Mode has ended.'

kimiTest('persists native swarm mode and adds one reminder per activation', async ({ native }) => {
  // The context reader joins the raw message text: the generic JSON reader escapes the quotes of the native
  // reminder, so the swarm reminder would never match.
  let previousMode: string | undefined
  let reminderCount = 0
  await exerciseNativeOptionSequence(native, {
    groupId: 'swarmMode',
    steps: [
      { value: 'on', via: 'choose' },
      { value: 'on', via: 'reload' },
      { value: 'off', via: 'choose' },
      { value: 'off', via: 'reload' },
      { value: 'on', via: 'choose' },
    ],
    nativeProof: (request, step) => {
      // Each activation appends one reminder. Reload and exit retain history.
      if (step.value === 'on' && previousMode !== 'on')
        reminderCount++
      previousMode = step.value
      const context = kimiModelContextText(request)
      expect(context.split(SWARM_REMINDER)).toHaveLength(reminderCount + 1)
      expect(context).not.toContain(SWARM_EXIT_REMINDER)
    },
  })
})
