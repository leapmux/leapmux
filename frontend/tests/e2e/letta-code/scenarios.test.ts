import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { runningChild } from './scenarios'

const calls = vi.hoisted(() => ({ open: vi.fn() }))
vi.mock('../helpers/runningChildProof', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../helpers/runningChildProof')>()
  return { ...actual, openRunningNativeChild: calls.open }
})
vi.mock('../letta-fixtures', () => ({ LETTA_TITLE_RULE: { name: 'title-letta', when: { body: 'session title' }, respond: { text: 'The native session title.' } } }))

function rejectModelAccess(): never {
  throw new Error('The child setup unit must not contact a model.')
}

describe('runningChild', () => {
  it('gives two actual child setup calls distinct native tool call IDs', async () => {
    calls.open.mockReset()
    const modelScript: ModelScript = {
      id: 'unit-letta-two-children',
      testDeadline: () => undefined,
      prompt: text => `${text}\nSCENARIO_TEST`,
      queue: async () => rejectModelAccess(),
      requestAt: async () => rejectModelAccess(),
      rule: async () => rejectModelAccess(),
      fallback: async () => rejectModelAccess(),
      status: async () => rejectModelAccess(),
      waitForSteps: async () => rejectModelAccess(),
      waitForGate: async () => rejectModelAccess(),
      releaseGate: async () => rejectModelAccess(),
      releaseGateIfHeld: async () => rejectModelAccess(),
      allowUnconsumed: () => rejectModelAccess(),
    }
    const context: ManagedNativeScenarioContext = {
      provider: AgentProvider.LETTA,
      modelScript,
      workspaceId: 'unit-letta-workspace',
      get page(): never {
        throw new Error('The child setup unit must not access a browser.')
      },
      get leapmuxServer(): never {
        throw new Error('The child setup unit must not access a Worker.')
      },
    }
    await runningChild(context)
    await runningChild(context, { allowExistingRows: true })
    expect(calls.open).toHaveBeenCalledTimes(2)
    const ids = calls.open.mock.calls.map(([, options]: unknown[]) => {
      if (!isObject(options) || !isObject(options.spawn) || typeof options.spawn.id !== 'string')
        throw new Error('The actual Letta child setup contains no native tool call ID.')
      expect(options.spawn.name).toBe('Agent')
      expect(options.spawn.arguments).toMatchObject({ subagent_type: 'general-purpose' })
      return options.spawn.id
    })
    expect(ids[0]).not.toBe(ids[1])
  })
})
