import { Code, ConnectError } from '@connectrpc/connect'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { deferred } from '~/test-support/async'
import { createAccountAction } from './createAccountAction'

describe('createAccountAction', () => {
  it('starts idle, with nothing to report', () => {
    createRoot((dispose) => {
      const action = createAccountAction()
      expect(action.busy()).toBeNull()
      expect(action.running()).toBe(false)
      expect(action.message()).toBeNull()
      dispose()
    })
  })

  it('reports the success text the work returns', async () => {
    await createRoot(async (dispose) => {
      const action = createAccountAction()
      await action.run({ fallback: 'Failed', work: async () => 'Profile updated.' })
      expect(action.message()).toEqual({ type: 'success', text: 'Profile updated.' })
      expect(action.running()).toBe(false)
      dispose()
    })
  })

  // A row that vanishes on success has nothing to say, and a green line under
  // an empty list reads as a report about the list.
  it('reports nothing when the work returns null', async () => {
    await createRoot(async (dispose) => {
      const action = createAccountAction()
      await action.run({ fallback: 'Failed', work: async () => null })
      expect(action.message()).toBeNull()
      dispose()
    })
  })

  // THE property the two unawaited refreshes broke. `busy` re-enables the
  // control, so everything the outcome depends on -- the account re-read
  // included -- has to finish before that flips back.
  it('stays busy for the whole of the work', async () => {
    await createRoot(async (dispose) => {
      const action = createAccountAction()
      const gate = deferred<void>()
      const running = action.run({
        fallback: 'Failed',
        work: async () => {
          await gate.promise
          return 'done'
        },
      })

      expect(action.running()).toBe(true)
      gate.resolve()
      await running
      expect(action.running()).toBe(false)
      dispose()
    })
  })

  // The hub answers a wrong password, a rate-limit refusal and a taken
  // username with sentences a user can act on, so those print verbatim.
  it('prints the hub message, and the fallback only when there is none', async () => {
    await createRoot(async (dispose) => {
      const action = createAccountAction()
      await action.run({
        fallback: 'Failed to change password',
        work: async () => {
          throw new ConnectError('too many attempts; try again in 5 minutes', Code.ResourceExhausted)
        },
      })
      expect(action.message()?.type).toBe('error')
      expect(action.message()?.text).toContain('too many attempts')

      // A REJECTED non-Error is the case the fallback documents: it carries no
      // message, so `String(err)` would print "[object Object]" to the user.
      const notAnError: unknown = { code: 13 }
      await action.run({
        fallback: 'Failed to change password',
        work: async () => {
          throw notAnError
        },
      })
      expect(action.message()).toEqual({ type: 'error', text: 'Failed to change password' })
      dispose()
    })
  })

  it('clears the previous outcome before it starts', async () => {
    await createRoot(async (dispose) => {
      const action = createAccountAction()
      action.reject('Email must not be empty.')
      expect(action.message()).toEqual({ type: 'error', text: 'Email must not be empty.' })

      const gate = deferred<void>()
      const running = action.run({
        fallback: 'Failed',
        work: async () => {
          await gate.promise
          return null
        },
      })
      expect(action.message()).toBeNull()
      gate.resolve()
      await running
      dispose()
    })
  })

  // A surface with a control per row keys the busy state, so one row's request
  // disables that row's control alone.
  it('carries a token, so only one row of many is busy', async () => {
    await createRoot(async (dispose) => {
      const action = createAccountAction<string>()
      const gate = deferred<void>()
      const running = action.run({
        token: 'github-1',
        fallback: 'Failed',
        work: async () => {
          await gate.promise
          return null
        },
      })

      expect(action.busy()).toBe('github-1')
      gate.resolve()
      await running
      expect(action.busy()).toBeNull()
      dispose()
    })
  })

  it('clears the token after a failure too', async () => {
    await createRoot(async (dispose) => {
      const action = createAccountAction<string>()
      await action.run({
        token: 'github-1',
        fallback: 'Failed to unlink provider',
        work: async () => {
          throw new Error('the hub is unreachable')
        },
      })
      expect(action.busy()).toBeNull()
      expect(action.message()?.text).toBe('the hub is unreachable')
      dispose()
    })
  })

  it('clears an outcome on demand', () => {
    createRoot((dispose) => {
      const action = createAccountAction()
      action.reject('nope')
      action.clear()
      expect(action.message()).toBeNull()
      dispose()
    })
  })
})
