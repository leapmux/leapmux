import type { Locator } from '@playwright/test'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { HeldNativeChild } from '../helpers/runningChildProof'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { runningChild } from './scenarios'

const state = vi.hoisted(() => ({ events: [] as string[] }))

vi.mock('../helpers/runningChildProof', async original => ({
  ...await original<typeof import('../helpers/runningChildProof')>(),
  openRunningNativeChild: vi.fn(),
}))

vi.mock('../helpers/subagentRegistry', async original => ({
  ...await original<typeof import('../helpers/subagentRegistry')>(),
  expectRowBecomesFinal: vi.fn(async () => { state.events.push('child final') }),
}))

vi.mock('../helpers/ui', async original => ({
  ...await original<typeof import('../helpers/ui')>(),
  tabById: () => ({
    click: async () => { state.events.push('select parent') },
  }),
}))

const child: HeldNativeChild = {
  row: {} as Locator,
  childId: 'native-child',
  parentId: 'native-parent',
  heldRequest: vi.fn(),
  finish: async () => { state.events.push('base finish') },
}

function context() {
  const waitForGate = vi.fn(async (gate: string) => {
    state.events.push(`wait ${gate}`)
  })
  const releaseGateIfHeld = vi.fn(async (gate: string) => {
    state.events.push(`release ${gate}`)
    return true
  })
  return {
    native: {
      provider: AgentProvider.DIRAC,
      modelScript: {
        prompt: (text: string) => text,
        waitForGate,
        releaseGateIfHeld,
      },
    } as unknown as ManagedNativeScenarioContext,
    waitForGate,
    releaseGateIfHeld,
  }
}

function gates() {
  const options = vi.mocked(openRunningNativeChild).mock.calls[0]?.[1]
  const parent = options?.parentSteps?.[1]?.gate
  if (!options || !parent)
    throw new Error('The asynchronous Dirac child requires a separate parent response gate.')
  return { child: options.gate, parent }
}

beforeEach(() => {
  state.events = []
  vi.mocked(openRunningNativeChild).mockReset().mockImplementation(async () => {
    state.events.push('open child')
    return child
  })
  vi.mocked(expectRowBecomesFinal).mockReset().mockImplementation(async () => {
    state.events.push('child final')
  })
})

describe('runningChild', () => {
  it('holds the parent until the child commits its final outcome', async () => {
    const rig = context()
    const held = await runningChild(rig.native)
    const gate = gates()
    expect(gate.parent).not.toBe(gate.child)
    expect(rig.waitForGate).toHaveBeenCalledExactlyOnceWith(gate.parent)
    expect(rig.releaseGateIfHeld).not.toHaveBeenCalled()
    await held.finish()
    expect(state.events).toEqual([
      'open child',
      `wait ${gate.parent}`,
      `release ${gate.child}`,
      'select parent',
      'child final',
      `release ${gate.parent}`,
      'base finish',
    ])
  })

  it('releases the parent if native child setup fails', async () => {
    const failure = new Error('Native child setup failed')
    vi.mocked(openRunningNativeChild).mockRejectedValueOnce(failure)
    const rig = context()
    await expect(runningChild(rig.native)).rejects.toBe(failure)
    expect(rig.releaseGateIfHeld.mock.calls).toEqual([
      [gates().child],
      [gates().parent],
    ])
  })

  it('releases the parent if its response never reaches the gate', async () => {
    const failure = new Error('Native parent response failed')
    const rig = context()
    rig.waitForGate.mockRejectedValueOnce(failure)
    await expect(runningChild(rig.native)).rejects.toBe(failure)
    expect(rig.releaseGateIfHeld.mock.calls).toEqual([
      [gates().child],
      [gates().parent],
    ])
  })

  it('releases the parent and preserves a failed child-finality check', async () => {
    const failure = new Error('Native child outcome was not committed')
    const rig = context()
    const held = await runningChild(rig.native)
    vi.mocked(expectRowBecomesFinal).mockRejectedValueOnce(failure)
    await expect(held.finish()).rejects.toBe(failure)
    expect(rig.releaseGateIfHeld.mock.calls).toEqual([
      [gates().child],
      [gates().parent],
    ])
    expect(state.events).not.toContain('base finish')
  })
})
