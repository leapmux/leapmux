import type { Locator, Page } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord, MockModelScenarioStatus } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { RunningNativeChild } from './runningChildProof'
import { Code } from '@connectrpc/connect'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoModelRequestCarries, expectUnsupportedSubagent, READ_ONLY_SUBAGENT_REASON } from './unsupportedSubagent'

/**
 * The fake Worker, transcript, and composer of one test.
 * The Worker refuses each queued input and records its text. A test can let a refused text reach the model before or
 * after the child finishes, and can change what the composer shows.
 */
const fake = vi.hoisted(() => ({
  log: [] as string[],
  refused: [] as string[],
  requests: [] as unknown[],
  leak: 'never' as 'never' | 'at-refusal' | 'after-finish',
  placeholders: 1,
  visibleReasons: 0,
}))

/** A locator probe that the fake `expect` reads. */
interface Probe {
  probe: true
  count: number
  attributes: Record<string, string>
}

function probe(count: number, attributes: Record<string, string> = {}): Probe {
  return { probe: true, count, attributes }
}

function isProbe(value: unknown): value is Probe {
  return typeof value === 'object' && value !== null && 'probe' in value
}

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (!isProbe(value))
      return actual.expect(value, message)
    return {
      toBeVisible: async () => actual.expect(value.count, message).toBeGreaterThan(0),
      toHaveCount: async (expected: number) => actual.expect(value.count, message).toBe(expected),
      toHaveAttribute: async (name: string, expected: string) => actual.expect(value.attributes[name], message).toBe(expected),
    }
  }
  return { ...actual, expect: Object.assign(check, actual.expect) }
})

vi.mock('./api', async importOriginal => ({
  ...await importOriginal<typeof import('./api')>(),
  getTestChannel: async () => ({
    callWorker: async (_workerId: string, method: string, _request: unknown, _response: unknown, payload: { text?: string }) => {
      fake.log.push(method)
      if (method === 'ListAgentInputQueue')
        return { snapshot: { items: [] } }
      if (method === 'EnqueueAgentInput') {
        const text = payload.text ?? ''
        fake.refused.push(text)
        if (fake.leak === 'at-refusal')
          fake.requests.push(request('the child task', text))
        throw Object.assign(new Error('refused'), { source: 'rpc', code: Code.InvalidArgument, message: 'invalid queued agent input: this agent does not accept that input' })
      }
      if (method === 'InterruptAgent')
        throw Object.assign(new Error('refused'), { source: 'rpc', code: Code.FailedPrecondition, message: 'this subagent cannot be interrupted' })
      throw new Error(`The fake Worker has no method ${method}.`)
    },
  }),
}))

vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  nativeAgentsByIds: async (): Promise<Partial<AgentInfo>[]> => [
    { id: 'native-child', parentAgentId: 'native-parent', rootAgentId: 'native-root', acceptsMessages: false, acceptsInterrupt: false },
    { id: 'native-parent', parentAgentId: '', rootAgentId: 'native-root', acceptsMessages: true, acceptsInterrupt: true },
  ],
}))

vi.mock('./subagentRegistry', async importOriginal => ({
  ...await importOriginal<typeof import('./subagentRegistry')>(),
  openChildTabFromRow: async () => {
    fake.log.push('open child tab')
    return 'native-child'
  },
}))

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  composerEditor: () => probe(1, { contenteditable: 'false' }),
}))

/** One recorded model request that `rule` answered, with `text` in its body. */
function request(rule: string, text: string): MockModelRequestRecord {
  return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', rule, body: { messages: [{ role: 'user', content: text }] } }
}

function status(): MockModelScenarioStatus {
  return { complete: true, nextStep: 0, stepCount: 0, ruleMatches: {}, pendingGates: [], requests: fake.requests as MockModelRequestRecord[], unexpectedRequests: [] }
}

/** A context whose page and model script are the fakes of this file. */
function fakeContext(): ManagedNativeScenarioContext {
  const page = Object.assign({} as Page, {
    locator: (selector: string) => {
      if (selector === `[data-placeholder="${READ_ONLY_SUBAGENT_REASON}"]:visible`)
        return probe(fake.placeholders)
      if (selector === '[data-testid="interrupt-button"]:visible')
        return probe(0)
      throw new Error(`The fake page has no locator for ${selector}.`)
    },
    getByText: (text: string) => {
      if (text !== READ_ONLY_SUBAGENT_REASON)
        throw new Error(`The fake page has no text locator for ${text}.`)
      return { filter: () => probe(fake.visibleReasons) }
    },
  })
  const modelScript = Object.assign({} as ModelScript, {
    status: async () => {
      fake.log.push('status')
      return status()
    },
  })
  return { page, modelScript, provider: AgentProvider.KILO, workspaceId: 'unsupported-route', leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' } }
}

/** A running child whose finish logs itself, and can make the parent report carry the refused message. */
function runningChild(): RunningNativeChild {
  return {
    row: probe(1, { 'data-status': 'running' }) as unknown as Locator,
    childId: 'native-child',
    parentId: 'native-parent',
    finish: async () => {
      fake.log.push('finish')
      if (fake.leak === 'after-finish')
        fake.requests.push(request('the parent report', fake.refused[0] ?? ''))
    },
  }
}

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
  beforeEach(() => {
    fake.log = []
    fake.refused = []
    fake.requests = []
    fake.leak = 'never'
    fake.placeholders = 1
    fake.visibleReasons = 0
  })

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

  it('refuses a send, finishes the child, and checks the model requests again after the finish', async () => {
    await expectUnsupportedSubagent(fakeContext(), { operation: 'send', openChild: async () => runningChild() })
    expect(fake.refused).toHaveLength(1)
    expect(fake.refused[0]).toMatch(/^REFUSEDCHILDMESSAGE[0-9a-f]{32}$/)
    expect(fake.log).toEqual(['open child tab', 'ListAgentInputQueue', 'EnqueueAgentInput', 'ListAgentInputQueue', 'status', 'finish', 'status'])
  })

  it('fails when a model request after the finish carries the refused message', async () => {
    fake.leak = 'after-finish'
    await expect(expectUnsupportedSubagent(fakeContext(), { operation: 'send', openChild: async () => runningChild() }))
      .rejects
      .toThrow('no model request carries')
    expect(fake.log.filter(entry => entry === 'finish')).toHaveLength(1)
  })

  it('fails and finishes the child when the refused message reaches the model before the finish', async () => {
    fake.leak = 'at-refusal'
    await expect(expectUnsupportedSubagent(fakeContext(), { operation: 'send', openChild: async () => runningChild() }))
      .rejects
      .toThrow('no model request carries')
    expect(fake.log.at(-1)).toBe('finish')
  })

  it('requires the composer placeholder to state the read-only reason', async () => {
    fake.placeholders = 0
    await expect(expectUnsupportedSubagent(fakeContext(), { operation: 'send', openChild: async () => runningChild() })).rejects.toThrow()
    expect(fake.refused).toEqual([])
    expect(fake.log.at(-1)).toBe('finish')
  })

  it('refuses a second visible copy of the read-only reason', async () => {
    fake.visibleReasons = 1
    await expect(expectUnsupportedSubagent(fakeContext(), { operation: 'send', openChild: async () => runningChild() })).rejects.toThrow()
    expect(fake.refused).toEqual([])
  })

  it('refuses a new message text in each call', async () => {
    await expectUnsupportedSubagent(fakeContext(), { operation: 'send', openChild: async () => runningChild() })
    await expectUnsupportedSubagent(fakeContext(), { operation: 'send', openChild: async () => runningChild() })
    expect(fake.refused).toHaveLength(2)
    expect(fake.refused[0]).not.toBe(fake.refused[1])
  })

  it('proves a refused interrupt without a model request check', async () => {
    await expectUnsupportedSubagent(fakeContext(), { operation: 'interrupt', openChild: async () => runningChild() })
    expect(fake.log).toEqual(['open child tab', 'InterruptAgent', 'finish'])
  })
})

describe('expectNoModelRequestCarries', () => {
  beforeEach(() => {
    fake.requests = []
  })

  const script = { status: async () => status() }

  it('accepts requests that do not carry the text', async () => {
    fake.requests = [request('the child task', 'another text')]
    await expectNoModelRequestCarries(script, 'REFUSED')
  })

  it('states the answer of each request that carries the text', async () => {
    fake.requests = [request('the parent report', 'carries REFUSED'), { protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex: 3, body: 'REFUSED' }]
    await expect(expectNoModelRequestCarries(script, 'REFUSED')).rejects.toThrow(/the parent report[\s\S]*step 3/)
  })

  it('checks a request that no rule or step answered', async () => {
    const unexpected = { status: async () => ({ ...status(), unexpectedRequests: [{ protocol: 'openai-chat-completions' as const, path: '/v1/chat/completions', reason: 'no answer', body: { text: 'REFUSED' } }] }) }
    await expect(expectNoModelRequestCarries(unexpected, 'REFUSED')).rejects.toThrow('an unexpected request: no answer')
  })

  it.each(['', '  '])('refuses an empty text: %j', async (text) => {
    await expect(expectNoModelRequestCarries(script, text)).rejects.toThrow('not empty')
  })
})
