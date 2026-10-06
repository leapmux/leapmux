import type { Page } from '@playwright/test'
import type { AddressInfo } from 'node:net'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { createServer } from 'node:http'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from './cleanup'
import { MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { readJSONBody } from './mockHttp'
import { createMockModelServer } from './mockModelServer'
import { startModelScript } from './modelScriptFixture'
import { exerciseNativeQuotaHeaders } from './nativeQuota'

const browser = vi.hoisted(() => ({
  send: vi.fn<(prompt: string) => Promise<void>>(),
  idle: vi.fn<() => Promise<void>>(),
  visible: vi.fn<(answer: string) => Promise<void>>(),
  bubble: { answer: '' },
}))

vi.mock('./ui', () => ({
  sendMessage: async (_page: Page, prompt: string) => browser.send(prompt),
  waitForAgentIdle: async () => browser.idle(),
  setInitialBrowserPref: vi.fn(),
  assistantBubbles: () => ({
    filter: ({ hasText }: { hasText: string }) => ({ first: () => ({ ...browser.bubble, answer: hasText }) }),
  }),
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: (value: unknown) => {
      if (typeof value === 'object' && value !== null && 'answer' in value && typeof value.answer === 'string') {
        const answer = value.answer
        return { toBeVisible: async () => browser.visible(answer) }
      }
      return expect(value)
    },
  }
})

beforeEach(() => {
  vi.clearAllMocks()
  browser.idle.mockResolvedValue()
})

afterEach(() => vi.restoreAllMocks())

interface QuotaReceiptOptions {
  keepAnthropic?: boolean
  status?: number
  omitHeader?: string
  utilization?: string
  rateStatus?: string
  wrongAnswer?: boolean
  earlierResponse?: boolean
}

/** Exercise the actual helper against an actual generic HTTP response and its observed receipt. */
async function runQuotaScenario(options: QuotaReceiptOptions = {}): Promise<MockModelRequestRecord> {
  const model = await createMockModelServer({ models: MOCK_MODEL_IDS })
  return withCleanup(async () => {
    const lifecycle = await startModelScript(model.url)
    return withCleanup(async () => {
      const receipts = new Map<string, NonNullable<MockModelRequestRecord['response']>>()
      const answers: string[] = []
      const proxy = createServer((request, response) => {
        void (async () => {
          const body = await readJSONBody(request)
          const upstream = await fetch(`${model.url}/v1/chat/completions`, {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify(body),
          })
          const headers = Object.fromEntries([...upstream.headers].filter(([key]) => options.keepAnthropic || !key.startsWith('anthropic-')))
          if (options.omitHeader)
            delete headers[options.omitHeader]
          if (options.utilization !== undefined)
            headers['x-leapmux-e2e-ratelimit-utilization'] = options.utilization
          if (options.rateStatus !== undefined)
            headers['x-leapmux-e2e-ratelimit-status'] = options.rateStatus
          const text = await upstream.text()
          delete headers['content-length']
          delete headers['transfer-encoding']
          response.writeHead(options.status ?? upstream.status, headers)
          response.end(options.wrongAnswer ? text.replace(/NATIVEQUOTAHEADERS[0-9a-f]+/, 'WRONG_ACTUAL_HTTP_ANSWER') : text)
        })().catch((error: unknown) => response.destroy(error instanceof Error ? error : new Error(String(error))))
      })
      return withCleanup(async () => {
        await new Promise<void>((resolve, reject) => {
          proxy.once('error', reject)
          proxy.listen(0, '127.0.0.1', () => {
            proxy.off('error', reject)
            resolve()
          })
        })
        const { port } = proxy.address() as AddressInfo
        const send = async (prompt: string) => {
          const response = await fetch(`http://127.0.0.1:${port}/v1/chat/completions`, {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ model: 'mock-model', stream: false, messages: [{ role: 'user', content: prompt }] }),
          })
          receipts.set(prompt, { status: response.status, headers: Object.fromEntries(response.headers) })
          const body = await response.json() as { choices: { message: { content: string } }[] }
          const answer = body.choices[0]?.message.content
          if (typeof answer !== 'string')
            throw new Error('The actual generic response contains no assistant text.')
          answers.push(answer)
        }
        browser.send.mockImplementation(send)
        browser.visible.mockImplementation(async (answer) => {
          expect(answers).toContain(answer)
        })
        if (options.earlierResponse) {
          await lifecycle.script.queue({ text: 'The earlier real response.' })
          await send(lifecycle.script.prompt('Complete the earlier model turn.'))
        }
        // The proxy changes the response after the mock records it, so attach the response that the client received.
        const withReceipt = (record: MockModelRequestRecord): MockModelRequestRecord => {
          const prompt = typeof record.body === 'object' && record.body !== null && 'messages' in record.body && Array.isArray(record.body.messages)
            ? record.body.messages.findLast((message: unknown) => typeof message === 'object' && message !== null && 'role' in message && message.role === 'user')
            : undefined
          const text = typeof prompt === 'object' && prompt !== null && 'content' in prompt && typeof prompt.content === 'string' ? prompt.content : undefined
          const receipt = text === undefined ? undefined : receipts.get(text)
          return receipt ? { ...record, response: receipt } : record
        }
        const script: ModelScript = {
          ...lifecycle.script,
          status: async () => {
            const status = await lifecycle.script.status()
            return { ...status, requests: status.requests.map(withReceipt) }
          },
          requestAt: async stepIndex => withReceipt(await lifecycle.script.requestAt(stepIndex)),
        }
        // Browser adapters treat the Page as an opaque handle. No fake browser method supplies this receipt.
        const page = {} as Page
        const context: ManagedNativeScenarioContext = {
          page,
          modelScript: script,
          provider: AgentProvider.GITHUB_COPILOT,
          workspaceId: 'quota-unit-workspace',
          leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused-token', workerId: 'unused-worker' },
        }
        const record = await exerciseNativeQuotaHeaders(context)
        const status = await lifecycle.script.status()
        expect(status.complete).toBe(true)
        expect(status.nextStep).toBe(options.earlierResponse ? 2 : 1)
        expect(status.unexpectedRequests).toEqual([])
        expect(browser.send).toHaveBeenCalledTimes(1)
        expect(browser.idle).toHaveBeenCalledTimes(1)
        expect(browser.visible).toHaveBeenCalledTimes(1)
        if (!options.keepAnthropic) {
          const delivered = [...receipts.values()].at(-1)
          expect(delivered?.status).toBe(200)
          expect(Object.keys(delivered?.headers ?? {}).some(key => key.startsWith('anthropic-'))).toBe(false)
          expect(delivered?.headers).toHaveProperty('x-leapmux-e2e-ratelimit-utilization', '0.92')
        }
        return record
      }, async () => {
        proxy.closeAllConnections()
        await new Promise<void>(resolve => proxy.close(() => resolve()))
      })
    }, async () => lifecycle.finish(true))
  }, async () => model.close())
}

describe('exerciseNativeQuotaHeaders', () => {
  it('accepts a real generic quota response without Anthropic headers', async () => {
    await runQuotaScenario()
  })

  it('returns the request of its own turn after an earlier turn, with the response receipt', async () => {
    const record = await runQuotaScenario({ earlierResponse: true })
    expect(record.stepIndex).toBe(1)
    expect(record.response?.status).toBe(200)
    expect(record.response?.headers).toHaveProperty('x-leapmux-e2e-ratelimit-status', 'allowed_warning')
  })

  it.each(['allowed', 'rejected'])('rejects a window status other than the near-limit warning: %s', async (rateStatus) => {
    await expect(runQuotaScenario({ keepAnthropic: true, rateStatus })).rejects.toThrow(/x-leapmux-e2e-ratelimit-status|allowed_warning/)
  })

  it('retains the existing combined response headers and ordered-step proof', async () => {
    await runQuotaScenario({ keepAnthropic: true, earlierResponse: true })
  })

  it('rejects an actual response status other than 200', async () => {
    await expect(runQuotaScenario({ keepAnthropic: true, status: 429 })).rejects.toThrow(/200|429/)
  })

  it.each(['x-ratelimit-limit-requests', 'x-ratelimit-remaining-requests'])('rejects a missing generic request header: %s', async (omitHeader) => {
    await expect(runQuotaScenario({ keepAnthropic: true, omitHeader })).rejects.toThrow(omitHeader)
  })

  it.each([
    { label: 'absent', omitHeader: 'x-leapmux-e2e-ratelimit-utilization' },
    { label: 'empty', utilization: '' },
    { label: 'zero', utilization: '0' },
  ])('rejects a $label generic utilization receipt even when Anthropic headers exist', async (options) => {
    await expect(runQuotaScenario({ keepAnthropic: true, ...options })).rejects.toThrow(/x-leapmux-e2e-ratelimit-utilization|0\.92/)
  })

  it('retains the exact final assistant-answer visibility check', async () => {
    await expect(runQuotaScenario({ keepAnthropic: true, wrongAnswer: true })).rejects.toThrow('NATIVEQUOTAHEADERS')
  })
})
