import type { ListAvailableShellsResponse } from '~/generated/leapmux/v1/terminal_pb'
import { createRoot, createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { deferred, flush } from '~/test-support/async'

const listAvailableShells = vi.fn<(workerId: string, req: { orgId: string, workspaceId: string, workerId: string }) => Promise<ListAvailableShellsResponse>>()

vi.mock('~/api/workerRpc', () => ({
  listAvailableShells: (workerId: string, req: { orgId: string, workspaceId: string, workerId: string }) =>
    listAvailableShells(workerId, req),
}))

const { useAvailableShells } = await import('./useAvailableShells')

function shellsResp(shells: string[], defaultShell = ''): ListAvailableShellsResponse {
  return {
    $typeName: 'leapmux.v1.ListAvailableShellsResponse',
    shells,
    defaultShell,
  } as ListAvailableShellsResponse
}

beforeEach(() => {
  listAvailableShells.mockReset()
})

describe('useAvailableShells', () => {
  it('does not fetch while the source returns null', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, _setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source)
        await flush()
        expect(listAvailableShells).not.toHaveBeenCalled()
        expect(hook.loading()).toBe(false)
        expect(hook.shells()).toEqual([])
        expect(hook.shell()).toBe('')
        dispose()
        done()
      })
    })
  })

  it('fetches once the source returns args, populates shells, and uses the server default', async () => {
    listAvailableShells.mockResolvedValueOnce(shellsResp(['/bin/zsh', '/bin/bash'], '/bin/zsh'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source)
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(listAvailableShells).toHaveBeenCalledTimes(1)
        expect(listAvailableShells.mock.calls[0]).toEqual(['A', { orgId: 'o', workspaceId: 'w', workerId: 'A' }])
        expect(hook.shells()).toEqual(['/bin/zsh', '/bin/bash'])
        expect(hook.defaultShell()).toBe('/bin/zsh')
        expect(hook.shell()).toBe('/bin/zsh')
        expect(hook.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('falls back to the first shell when the server-reported default is empty', async () => {
    listAvailableShells.mockResolvedValueOnce(shellsResp(['/bin/fish', '/bin/zsh'], ''))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source)
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(hook.defaultShell()).toBe('/bin/fish')
        dispose()
        done()
      })
    })
  })

  it('returns empty defaultShell when both server default and shells list are empty', async () => {
    listAvailableShells.mockResolvedValueOnce(shellsResp([], ''))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source)
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(hook.defaultShell()).toBe('')
        expect(hook.shell()).toBe('')
        dispose()
        done()
      })
    })
  })

  it('latches: source flipping null → null doesn\'t re-fetch and keeps shells', async () => {
    listAvailableShells.mockResolvedValueOnce(shellsResp(['/bin/zsh'], '/bin/zsh'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source)
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(listAvailableShells).toHaveBeenCalledTimes(1)

        // Caller drops the gate. Cached shells must survive the toggle.
        setSource(null)
        await flush()
        expect(hook.shells()).toEqual(['/bin/zsh'])
        expect(listAvailableShells).toHaveBeenCalledTimes(1)

        // Re-toggle for the same worker. Still no refetch -- this is
        // the regression guard for the ChangeBranchDialog "fires once
        // even after Open-as toggles" case.
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(listAvailableShells).toHaveBeenCalledTimes(1)
        dispose()
        done()
      })
    })
  })

  it('re-fetches when workerId changes and resets any prior user override', async () => {
    listAvailableShells
      .mockResolvedValueOnce(shellsResp(['/bin/zsh', '/bin/bash'], '/bin/zsh'))
      .mockResolvedValueOnce(shellsResp(['/usr/bin/pwsh'], '/usr/bin/pwsh'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source)
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()

        // User overrides to /bin/bash.
        hook.setShell('/bin/bash')
        expect(hook.shell()).toBe('/bin/bash')

        // User picks a different worker. The override must clear so
        // we don't send a shell path that doesn't exist on worker B.
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'B' })
        await flush()
        expect(listAvailableShells).toHaveBeenCalledTimes(2)
        expect(hook.shells()).toEqual(['/usr/bin/pwsh'])
        expect(hook.shell()).toBe('/usr/bin/pwsh')
        dispose()
        done()
      })
    })
  })

  it('setShell(null) clears the override and the effective shell follows the default again', async () => {
    listAvailableShells.mockResolvedValueOnce(shellsResp(['/bin/zsh', '/bin/bash'], '/bin/zsh'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source)
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(hook.shell()).toBe('/bin/zsh')

        hook.setShell('/bin/bash')
        expect(hook.shell()).toBe('/bin/bash')

        hook.setShell(null)
        expect(hook.shell()).toBe('/bin/zsh')
        dispose()
        done()
      })
    })
  })

  it('loading flips true during the in-flight fetch and false on resolve', async () => {
    const d = deferred<ListAvailableShellsResponse>()
    listAvailableShells.mockImplementationOnce(() => d.promise)
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source)
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(hook.loading()).toBe(true)
        d.resolve(shellsResp(['/bin/zsh'], '/bin/zsh'))
        await flush()
        expect(hook.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('source identity churn with stable workerId does not refire the fetch', async () => {
    // Real callers build a fresh args object on every reactive read
    // (`{ orgId: org.orgId(), workspaceId: …, workerId: … }`). Tracking
    // the source accessor by reference would refire the effect on
    // every upstream tick (e.g. an `org.orgId()` change) even though
    // the workerId — the only field that gates the fetch — is
    // unchanged. The hook tracks `source()?.workerId` instead, so
    // identity churn upstream stays a memo no-op.
    listAvailableShells.mockResolvedValueOnce(shellsResp(['/bin/zsh'], '/bin/zsh'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [orgId, setOrgId] = createSignal('org-1')
        const source = () => ({ orgId: orgId(), workspaceId: 'w', workerId: 'A' })
        useAvailableShells(source)
        await flush()
        expect(listAvailableShells).toHaveBeenCalledTimes(1)

        // orgId change: source returns a fresh object identity, but the
        // workerId field is unchanged. No refetch.
        setOrgId('org-2')
        await flush()
        expect(listAvailableShells).toHaveBeenCalledTimes(1)

        // And a third churn for good measure.
        setOrgId('org-3')
        await flush()
        expect(listAvailableShells).toHaveBeenCalledTimes(1)
        dispose()
        done()
      })
    })
  })

  it('on RPC failure: invokes onError, clears shells, and turns loading off', async () => {
    const onError = vi.fn()
    listAvailableShells.mockRejectedValueOnce(new Error('worker offline'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source, onError)
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(onError).toHaveBeenCalledTimes(1)
        expect((onError.mock.calls[0][0] as Error).message).toBe('worker offline')
        expect(hook.shells()).toEqual([])
        expect(hook.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('clears prior shells and serverDefault while a worker swap is in flight', async () => {
    // Regression: an earlier revision left the previous worker's shells
    // and serverDefault cached during the in-flight fetch, so shell()
    // returned the stale default and an isTerminalCreateDisabled gate
    // that only checked shell() != '' would let Create fire on the new
    // worker with the old worker's shell path.
    const d = deferred<ListAvailableShellsResponse>()
    listAvailableShells
      .mockResolvedValueOnce(shellsResp(['/bin/zsh'], '/bin/zsh'))
      .mockImplementationOnce(() => d.promise)
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source)
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(hook.shells()).toEqual(['/bin/zsh'])
        expect(hook.shell()).toBe('/bin/zsh')

        // Switch workers — the new worker's fetch is in flight.
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'B' })
        await flush()
        // While the new fetch is pending, the OLD worker's shell must
        // not be reported — otherwise downstream gates would accept it.
        expect(hook.shells()).toEqual([])
        expect(hook.defaultShell()).toBe('')
        expect(hook.shell()).toBe('')
        expect(hook.loading()).toBe(true)

        // Settle the new worker's fetch — values now reflect worker B.
        d.resolve(shellsResp(['/usr/bin/pwsh'], '/usr/bin/pwsh'))
        await flush()
        expect(hook.shells()).toEqual(['/usr/bin/pwsh'])
        expect(hook.shell()).toBe('/usr/bin/pwsh')
        dispose()
        done()
      })
    })
  })

  it('refresh() re-fetches against the current source so a transient failure can recover without a worker swap', async () => {
    // Regression: previously the only retry trigger was a workerId
    // transition. On a single-worker dialog with a transient failure
    // (network blip, slow worker), the user had no recovery path until
    // they picked a different worker and back. The new `refresh()` hook
    // re-fires the fetch against the current source.
    const onError = vi.fn()
    listAvailableShells
      .mockRejectedValueOnce(new Error('transient'))
      .mockResolvedValueOnce(shellsResp(['/bin/zsh'], '/bin/zsh'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(
          { orgId: 'o', workspaceId: 'w', workerId: 'A' },
        )
        const hook = useAvailableShells(source, onError)
        await flush()
        expect(onError).toHaveBeenCalledTimes(1)
        expect(hook.shells()).toEqual([])

        await hook.refresh()
        await flush()
        expect(listAvailableShells).toHaveBeenCalledTimes(2)
        expect(hook.shells()).toEqual(['/bin/zsh'])
        expect(hook.shell()).toBe('/bin/zsh')
        dispose()
        done()
      })
    })
  })

  it('refresh() is a no-op when the source returns null (the gate said don\'t fetch)', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source)
        await hook.refresh()
        await flush()
        expect(listAvailableShells).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('a failed fetch can be retried for the same workerId once the source re-emits', async () => {
    // Regression: an earlier revision assigned lastWorkerId BEFORE the
    // fetch ran, so a failed listAvailableShells locked out subsequent
    // re-fetches for the same workerId. The dialog became stuck until
    // the user switched workers and back. The hook now advances the
    // sentinel only on success so a transient failure can recover.
    const onError = vi.fn()
    listAvailableShells
      .mockRejectedValueOnce(new Error('transient worker error'))
      .mockResolvedValueOnce(shellsResp(['/bin/zsh'], '/bin/zsh'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ orgId: string, workspaceId: string, workerId: string } | null>(null)
        const hook = useAvailableShells(source, onError)
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(onError).toHaveBeenCalledTimes(1)
        expect(hook.shells()).toEqual([])

        // Caller drops the gate, then re-emits with the same workerId —
        // the simplest model of "toggle the mode that gates the source
        // and toggle back" without involving a worker swap. The retry
        // must fire and succeed.
        setSource(null)
        await flush()
        setSource({ orgId: 'o', workspaceId: 'w', workerId: 'A' })
        await flush()
        expect(listAvailableShells).toHaveBeenCalledTimes(2)
        expect(hook.shells()).toEqual(['/bin/zsh'])
        expect(hook.shell()).toBe('/bin/zsh')
        dispose()
        done()
      })
    })
  })
})
