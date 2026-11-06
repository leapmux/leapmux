import type { InputRecoveryDiscovery, InputRecoveryResult, InputRecoveryTarget, InputRecoveryWorkerFailure } from '~/models/agentInputRecovery'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentInputProcessRecoverySchema, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { deferred } from '~/test-support/async'
import { AgentInputRecoveryDialog } from './AgentInputRecoveryDialog'

function target(agentId: string, workerId = 'worker-a', generation = 1n): InputRecoveryTarget {
  return {
    agentId,
    workerId,
    workerName: `Host ${workerId}`,
    title: agentId,
    recovery: create(AgentInputProcessRecoverySchema, {
      outputGeneration: generation,
      agentSessionId: `session-${generation}`,
      agentProvider: AgentProvider.CODEX,
      processInstanceId: `process-${agentId}-${generation}`,
    }),
  }
}

function fixture(options: { related?: InputRecoveryTarget[], failures?: InputRecoveryWorkerFailure[] } = {}) {
  const current = target('Current tab')
  const related = options.related ?? [
    target('Other tab', 'worker-b'),
  ]
  const discovery: InputRecoveryDiscovery = {
    current,
    targets: [current, ...related],
    failures: options.failures ?? [],
  }
  const discoverTargets = vi.fn(async (requested: InputRecoveryTarget): Promise<InputRecoveryDiscovery> => ({
    ...discovery,
    current: requested,
    targets: [requested, ...related],
  }))
  const recoverTargets = vi.fn(async (targets: readonly InputRecoveryTarget[]): Promise<readonly InputRecoveryResult[]> => targets.map(target => ({ target, status: 'recovered' })))
  const onClose = vi.fn()
  const actions = { discoverTargets, recoverTargets }
  return {
    current,
    discovery,
    discoverTargets,
    recoverTargets,
    onClose,
    actions,
  }
}

describe('AgentInputRecoveryDialog', () => {
  it('warns about interruption and cancels without recovery', async () => {
    const f = fixture()
    render(() => <AgentInputRecoveryDialog current={f.current} actions={f.actions} onClose={f.onClose} />)
    await screen.findByTestId('input-recovery-summary')
    expect(screen.getByText(/running work.*subagents.*interrupted/)).toBeInTheDocument()
    expect(screen.getByText(/Recovery uses each worker's current Codex login/)).toBeInTheDocument()
    expect(screen.getByText(/Failed and uncertain inputs are not retried/)).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Recover all Codex tabs with login issues' })).not.toBeChecked()
    expect(f.recoverTargets).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(f.onClose).toHaveBeenCalledOnce()
    expect(f.recoverTargets).not.toHaveBeenCalled()
  })

  it('recovers only the current tab by default', async () => {
    const f = fixture()
    render(() => <AgentInputRecoveryDialog current={f.current} actions={f.actions} onClose={f.onClose} />)
    expect(await screen.findByTestId('input-recovery-summary')).toHaveTextContent('1 Codex tab on 1 worker')
    await fireEvent.click(screen.getByRole('button', { name: 'Recover login' }))
    expect(await screen.findByTestId('input-recovery-result-summary')).toHaveTextContent('Recovered 1 of 1 Codex tab.')
    expect(f.recoverTargets).toHaveBeenCalledExactlyOnceWith([f.current])
  })

  it('puts long content in the shared dialog scroller above the footer', async () => {
    const f = fixture()
    render(() => <AgentInputRecoveryDialog current={f.current} actions={f.actions} onClose={f.onClose} />)
    const summary = await screen.findByTestId('input-recovery-summary')
    const section = summary.closest('section')
    expect(section, 'the shared dialog supplies scrolling only to its direct section').not.toBeNull()
    const footer = screen.getByRole('button', { name: 'Recover login' }).closest('footer')
    expect(footer?.parentElement).toBe(section?.parentElement)
    expect(section?.contains(footer)).toBe(false)
  })

  it('summarizes selected workers and keeps the full tab list collapsed', async () => {
    const f = fixture({
      related: [
        target('Second tab', 'worker-b'),
        target('Third tab', 'worker-b'),
      ],
    })
    render(() => <AgentInputRecoveryDialog current={f.current} actions={f.actions} onClose={f.onClose} />)
    await screen.findByTestId('input-recovery-summary')
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Recover all Codex tabs with login issues' }))
    expect(screen.getByTestId('input-recovery-summary')).toHaveTextContent('3 Codex tabs on 2 workers')
    const summary = screen.getByText('View affected tabs')
    expect(summary.closest('details')).not.toHaveAttribute('open')
    const list = screen.getByTestId('input-recovery-targets')
    expect(within(list).getAllByRole('listitem', { hidden: true })).toHaveLength(3)
    await fireEvent.click(screen.getByRole('button', { name: 'Recover login' }))
    await screen.findByTestId('input-recovery-result-summary')
    expect(f.recoverTargets).toHaveBeenCalledExactlyOnceWith(f.discovery.targets)
  })

  it('does not transfer an open confirmation to updated props', async () => {
    const f = fixture()
    const [current, setCurrent] = createSignal(f.current)
    render(() => <AgentInputRecoveryDialog current={current()} actions={f.actions} onClose={f.onClose} />)
    await screen.findByTestId('input-recovery-summary')
    setCurrent(target('Current tab', 'worker-a', 9n))
    await fireEvent.click(screen.getByRole('button', { name: 'Recover login' }))
    await screen.findByTestId('input-recovery-result-summary')
    expect(f.recoverTargets).toHaveBeenCalledExactlyOnceWith([f.current])
    expect(f.discoverTargets).toHaveBeenCalledOnce()
    expect(f.current.recovery.outputGeneration).toBe(1n)
  })

  it('blocks an all-tab approval when a worker could not be checked', async () => {
    const f = fixture({
      related: [],
      failures: [
        { workerId: 'worker-b', workerName: 'Remote host', message: 'Disconnected' },
      ],
    })
    render(() => <AgentInputRecoveryDialog current={f.current} actions={f.actions} onClose={f.onClose} />)
    await screen.findByTestId('input-recovery-summary')
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Recover all Codex tabs with login issues' }))
    expect(screen.getByRole('button', { name: 'Recover login' })).toBeDisabled()
    expect(screen.getByRole('alert')).toHaveTextContent('Remote host: Disconnected')
    expect(f.recoverTargets).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Recover all Codex tabs with login issues' }))
    expect(screen.getByRole('button', { name: 'Recover login' })).not.toBeDisabled()
    await fireEvent.click(screen.getByRole('button', { name: 'Recover login' }))
    await screen.findByTestId('input-recovery-result-summary')
    expect(f.recoverTargets).toHaveBeenCalledExactlyOnceWith([f.current])
  })

  it('shows independent results without retrying an unconfirmed tab', async () => {
    const f = fixture()
    const other = f.discovery.targets[1]!
    f.recoverTargets.mockResolvedValue([
      { target: f.current, status: 'recovered' },
      { target: other, status: 'unconfirmed', message: 'This process changed. Reopen Recover login.' },
    ])
    render(() => <AgentInputRecoveryDialog current={f.current} actions={f.actions} onClose={f.onClose} />)
    await screen.findByTestId('input-recovery-summary')
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Recover all Codex tabs with login issues' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Recover login' }))
    expect(await screen.findByTestId('input-recovery-result-summary')).toHaveTextContent('Recovered 1 of 2 Codex tabs.')
    expect(screen.getByTestId('input-recovery-results')).toHaveTextContent('This process changed')
    expect(screen.queryByRole('button', { name: 'Recover login' })).not.toBeInTheDocument()
    await fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(f.recoverTargets).toHaveBeenCalledOnce()
    expect(f.onClose).toHaveBeenCalledOnce()
  })

  it('waits for discovery and reports an incomplete recovery response honestly', async () => {
    const f = fixture()
    const check = deferred<InputRecoveryDiscovery>()
    f.discoverTargets.mockReturnValueOnce(check.promise)
    f.recoverTargets.mockResolvedValueOnce([])
    render(() => <AgentInputRecoveryDialog current={f.current} actions={f.actions} onClose={f.onClose} />)
    expect(screen.getByRole('button', { name: 'Recover login' })).toBeDisabled()
    check.resolve(f.discovery)
    await screen.findByTestId('input-recovery-summary')
    await fireEvent.click(screen.getByRole('button', { name: 'Recover login' }))
    expect(await screen.findByTestId('input-recovery-result-summary')).toHaveTextContent('Recovered 0 of 1 Codex tab.')
    expect(screen.getByTestId('input-recovery-results')).toHaveTextContent('could not be confirmed')
    expect(f.recoverTargets).toHaveBeenCalledOnce()
  })

  it('prevents another approval or cancellation while recovery is in progress', async () => {
    const f = fixture()
    const pending = deferred<readonly InputRecoveryResult[]>()
    f.recoverTargets.mockReturnValueOnce(pending.promise)
    render(() => <AgentInputRecoveryDialog current={f.current} actions={f.actions} onClose={f.onClose} />)
    await screen.findByTestId('input-recovery-summary')
    const approve = screen.getByRole('button', { name: 'Recover login' })
    await fireEvent.click(approve)
    expect(approve).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    expect(screen.getByRole('checkbox')).toBeDisabled()
    await fireEvent.click(approve)
    expect(f.recoverTargets).toHaveBeenCalledOnce()
    expect(f.onClose).not.toHaveBeenCalled()
    pending.resolve([
      { target: f.current, status: 'recovered' },
    ])
    expect(await screen.findByTestId('input-recovery-result-summary')).toHaveTextContent('Recovered 1 of 1')
    expect(screen.getByRole('button', { name: 'Done' })).not.toBeDisabled()
  })

  it('requires an explicit retry after a discovery failure', async () => {
    const f = fixture()
    f.discoverTargets.mockRejectedValueOnce(new Error('The worker disconnected'))
    render(() => <AgentInputRecoveryDialog current={f.current} actions={f.actions} onClose={f.onClose} />)
    expect(await screen.findByRole('alert')).toHaveTextContent('The worker disconnected')
    expect(screen.getByRole('button', { name: 'Recover login' })).toBeDisabled()
    expect(f.discoverTargets).toHaveBeenCalledOnce()
    expect(f.recoverTargets).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByRole('button', { name: 'Retry checks' }))
    await screen.findByTestId('input-recovery-summary')
    expect(f.discoverTargets).toHaveBeenCalledTimes(2)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Recover login' })).not.toBeDisabled()
    expect(f.recoverTargets).not.toHaveBeenCalled()
  })
})
