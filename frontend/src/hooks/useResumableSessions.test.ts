import type { UseResumableSessionsArgs } from '~/hooks/useResumableSessions'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { useResumableSessions } from '~/hooks/useResumableSessions'

vi.mock('~/api/workerRpc', () => ({ listAgentSessions: vi.fn() }))

const listAgentSessions = vi.mocked(workerRpc.listAgentSessions)

function session(sessionId: string) {
  return { $typeName: 'leapmux.v1.AgentSessionSummary', sessionId, title: sessionId, updatedAt: '' }
}

function response(...ids: string[]) {
  return { $typeName: 'leapmux.v1.ListAgentSessionsResponse', sessions: ids.map(session) } as never
}

const ARGS: UseResumableSessionsArgs = {
  workerId: 'w-1',
  workingDir: '/repo',
  agentProvider: AgentProvider.CLAUDE_CODE,
}

// Lets a test resolve a pending call itself, so the ordering assertions do not
// depend on a timer.
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (err: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const flush = () => new Promise(resolve => setTimeout(resolve, 0))

beforeEach(() => {
  listAgentSessions.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('useResumableSessions', () => {
  it('fetches for the source and exposes what the worker returned', async () => {
    // Deferred rather than pre-resolved, so `loading()` is asserted while a
    // fetch is genuinely in flight. The effect has not run yet at the moment
    // the hook returns, so reading it there would assert nothing.
    const pending = deferred<never>()
    listAgentSessions.mockReturnValue(pending.promise)

    await createRoot(async (dispose) => {
      const { sessions, loading } = useResumableSessions(() => ARGS)
      await flush()
      expect(loading()).toBe(true)
      expect(sessions()).toEqual([])

      pending.resolve(response('ses_a', 'ses_b'))
      await flush()
      expect(sessions().map(s => s.sessionId)).toEqual(['ses_a', 'ses_b'])
      expect(loading()).toBe(false)

      // The worker id rides both the envelope and the request body, and the
      // signal is what lets a superseded call be abandoned.
      expect(listAgentSessions).toHaveBeenCalledWith('w-1', {
        workerId: 'w-1',
        workingDir: '/repo',
        agentProvider: AgentProvider.CLAUDE_CODE,
      }, expect.objectContaining({ signal: expect.anything() }))
      dispose()
    })
  })

  it('does not call the worker until all three keys are known', async () => {
    await createRoot(async (dispose) => {
      const { sessions } = useResumableSessions(() => null)
      await flush()
      expect(listAgentSessions).not.toHaveBeenCalled()
      expect(sessions()).toEqual([])
      dispose()
    })
  })

  // The three keys that change the answer. Shells depend on the worker alone;
  // a session list also moves with the directory the user picks and with the
  // provider selector beside it.
  it.each([
    ['the working directory', { ...ARGS, workingDir: '/other' }],
    ['the provider', { ...ARGS, agentProvider: AgentProvider.CODEX }],
    ['the worker', { ...ARGS, workerId: 'w-2' }],
  ])('re-fetches when %s changes', async (_name, next) => {
    listAgentSessions.mockResolvedValue(response('ses_a'))

    await createRoot(async (dispose) => {
      const [args, setArgs] = createSignal<UseResumableSessionsArgs>(ARGS)
      useResumableSessions(args)
      await flush()
      expect(listAgentSessions).toHaveBeenCalledTimes(1)

      setArgs(next)
      await flush()
      expect(listAgentSessions).toHaveBeenCalledTimes(2)
      dispose()
    })
  })

  it('does not re-fetch when the args object changes but its three keys do not', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))

    await createRoot(async (dispose) => {
      const [tick, setTick] = createSignal(0)
      // A fresh object every read, which is what a dialog's inline closure
      // produces.
      useResumableSessions(() => {
        tick()
        return { ...ARGS }
      })
      await flush()
      expect(listAgentSessions).toHaveBeenCalledTimes(1)

      setTick(1)
      await flush()
      expect(listAgentSessions).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  // EACH of the three, one at a time. A key dropped from the memo would leave
  // every other case in this file passing while the dialog answered a stale
  // question: the previous worker's sessions after switching machine, or Codex
  // sessions under the Claude Code selector -- handles the chosen provider
  // cannot resume at all.
  it.each([
    ['worker', { workerId: 'w-2' }],
    ['directory', { workingDir: '/other' }],
    ['provider', { agentProvider: AgentProvider.CODEX }],
  ])('re-fetches when the %s changes', async (_name, change) => {
    listAgentSessions.mockResolvedValue(response('ses_a'))

    await createRoot(async (dispose) => {
      const [args, setArgs] = createSignal<UseResumableSessionsArgs>(ARGS)
      useResumableSessions(args)
      await flush()
      expect(listAgentSessions).toHaveBeenCalledTimes(1)

      setArgs({ ...ARGS, ...change })
      await flush()
      expect(listAgentSessions).toHaveBeenCalledTimes(2)
      expect(listAgentSessions).toHaveBeenLastCalledWith(
        { ...ARGS, ...change }.workerId,
        expect.objectContaining(change),
        expect.anything(),
      )
      dispose()
    })
  })

  // Offering the previous directory's sessions while the new fetch is in
  // flight would invite the user to resume a handle that belongs elsewhere.
  it('clears the list immediately when the source changes', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))

    await createRoot(async (dispose) => {
      const [args, setArgs] = createSignal<UseResumableSessionsArgs>(ARGS)
      const { sessions } = useResumableSessions(args)
      await flush()
      expect(sessions()).toHaveLength(1)

      setArgs({ ...ARGS, workingDir: '/other' })
      expect(sessions()).toEqual([])
      dispose()
    })
  })

  it('clears the list when the source becomes null', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))

    await createRoot(async (dispose) => {
      const [args, setArgs] = createSignal<UseResumableSessionsArgs | null>(ARGS)
      const { sessions } = useResumableSessions(args)
      await flush()
      expect(sessions()).toHaveLength(1)

      setArgs(null)
      expect(sessions()).toEqual([])
      dispose()
    })
  })

  it('applies only the latest fetch when one supersedes another', async () => {
    const first = deferred<never>()
    const second = deferred<never>()
    listAgentSessions.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    await createRoot(async (dispose) => {
      const [args, setArgs] = createSignal<UseResumableSessionsArgs>(ARGS)
      const { sessions } = useResumableSessions(args)
      // Let the first fetch actually start before superseding it. Without this
      // the effect has not run yet, Solid coalesces the two source values, and
      // only ONE fetch is ever issued -- which would make this case pass
      // without exercising supersession at all.
      await flush()
      setArgs({ ...ARGS, workingDir: '/other' })
      await flush()

      // The superseded call resolves LAST, which is the ordering that would
      // otherwise leave the first directory's sessions on screen.
      second.resolve(response('ses_second'))
      await flush()
      first.resolve(response('ses_first'))
      await flush()

      expect(sessions().map(s => s.sessionId)).toEqual(['ses_second'])
      dispose()
    })
  })

  // The whole fallback rule rests on this: a failure and an empty directory
  // both leave the list empty, so the field needs one condition and not two.
  it('leaves the list empty when the worker fails', async () => {
    listAgentSessions.mockRejectedValue(new Error('worker offline'))

    await createRoot(async (dispose) => {
      const { sessions, loading } = useResumableSessions(() => ARGS)
      await flush()
      expect(sessions()).toEqual([])
      expect(loading()).toBe(false)
      dispose()
    })
  })

  it('refresh re-runs the current source, and is a no-op without one', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))

    await createRoot(async (dispose) => {
      const [args, setArgs] = createSignal<UseResumableSessionsArgs | null>(ARGS)
      const { refresh } = useResumableSessions(args)
      await flush()
      expect(listAgentSessions).toHaveBeenCalledTimes(1)

      await refresh()
      expect(listAgentSessions).toHaveBeenCalledTimes(2)

      setArgs(null)
      await refresh()
      expect(listAgentSessions).toHaveBeenCalledTimes(2)
      dispose()
    })
  })
})
