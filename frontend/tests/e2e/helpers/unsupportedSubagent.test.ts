import type { Locator, Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { RunningNativeChild } from './unsupportedSubagent'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectUnsupportedSubagent } from './unsupportedSubagent'

function context(): ManagedNativeScenarioContext {
  return {
    provider: AgentProvider.CURSOR,
    workspaceId: 'cleanup-boundary',
    leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
    get page(): Page {
      throw new Error('The identity guard must run before the page access.')
    },
    get modelScript(): ModelScript {
      throw new Error('The identity guard must run before the model access.')
    },
  }
}

function invalidChild(finish: () => Promise<void>): RunningNativeChild {
  return {
    childId: '',
    parentId: 'native-parent',
    finish,
    get row(): Locator {
      throw new Error('The identity guard must run before the row access.')
    },
  }
}

describe('expectUnsupportedSubagent', () => {
  it('finishes an acquired child when its identity assertion fails', async () => {
    const finish = vi.fn(async () => {})
    await expect(expectUnsupportedSubagent(context(), {
      operation: 'send',
      openChild: async () => invalidChild(finish),
    })).rejects.toThrow('Expected: not')
    expect(finish).toHaveBeenCalledOnce()
  })

  it('retains the failed assertion when the child cleanup fails', async () => {
    const cleanupError = new Error('The actual native child cleanup failed.')
    const finish = vi.fn(async () => {
      throw cleanupError
    })
    const result: unknown = await expectUnsupportedSubagent(context(), {
      operation: 'interrupt',
      openChild: async () => invalidChild(finish),
    }).then(() => null, error => error)
    expect(result).toBeInstanceOf(AggregateError)
    if (!(result instanceof AggregateError))
      throw new Error('The original assertion and cleanup failure were not retained.')
    expect(result.errors).toHaveLength(2)
    expect(result.errors[0]).toBeInstanceOf(Error)
    expect(result.errors[0].message).toContain('Expected: not')
    expect(result.errors[1]).toBe(cleanupError)
    expect(finish).toHaveBeenCalledOnce()
  })
})
