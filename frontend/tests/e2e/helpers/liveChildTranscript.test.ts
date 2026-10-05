import type { Page } from '@playwright/test'
import type { LiveChildSpec } from './liveChildTranscript'
import type { MockModelScenarioStatus, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { deferred } from '../../../src/test-support/async'
import { withCleanup } from './cleanup'
import { completeLiveChildTranscript, exerciseLiveChildTranscript } from './liveChildTranscript'
import { MOCK_MODEL_IDS, MOCK_MODELS } from './mockAgentEnvironment'
import { isRecord } from './mockModelScript'
import { createMockModelServer } from './mockModelServer'
import { startModelScript } from './modelScriptFixture'
import { ohMyPiYieldToolCall, readToolCall } from './providerToolCalls'

type RowKind = 'user' | 'assistant' | 'tool'

/** One visible transcript row. A row can hold text that only an expanded result view shows. */
interface FakeRow {
  kind: RowKind
  text: string
  hiddenUntilExpanded?: string
  expanded?: boolean
}

function visibleText(row: FakeRow): string {
  return row.expanded ? `${row.text}${row.hiddenUntilExpanded ?? ''}` : row.text
}

/**
 * The fake browser state of one test.
 * Each tab holds the visible rows of one transcript.
 * The simulated native agent writes the rows, and the fake locators read them.
 */
const browser = vi.hoisted(() => ({
  tabs: new Map<string, FakeRow[]>(),
  selected: '',
  childAgentId: '',
  childStatus: '',
  /** The selector of each result view that the helper expanded, in order. */
  expansions: [] as string[],
  onExpand: undefined as (() => void) | undefined,
  send: vi.fn<(prompt: string) => Promise<void>>(),
}))

/** A fake locator over the visible rows of the selected tab. */
interface TranscriptProbe {
  transcriptProbe: true
  first: () => TranscriptProbe
  count: () => number
  label: () => string
}

function transcriptProbe(kind: RowKind | undefined, hasText: string): TranscriptProbe {
  const probe: TranscriptProbe = {
    transcriptProbe: true,
    first: () => probe,
    count: () => (browser.tabs.get(browser.selected) ?? []).filter(row => (kind === undefined || row.kind === kind) && visibleText(row).includes(hasText)).length,
    label: () => `the ${kind ?? 'message'} rows that hold ${hasText} in tab ${browser.selected}`,
  }
  return probe
}

function rows(kind: RowKind | undefined) {
  return { filter: ({ hasText }: { hasText: string }) => transcriptProbe(kind, hasText) }
}

function isTranscriptProbe(value: unknown): value is TranscriptProbe {
  return typeof value === 'object' && value !== null && 'transcriptProbe' in value
}

/** The fake registry row of the one child that the simulated native agent starts. */
interface RegistryRowProbe {
  registryRowProbe: true
  getAttribute: (name: string) => Promise<string | null>
}

function registryRow(): RegistryRowProbe {
  return {
    registryRowProbe: true,
    getAttribute: async name => name === 'data-child-agent-id' ? browser.childAgentId : name === 'data-status' ? browser.childStatus : null,
  }
}

function isRegistryRowProbe(value: unknown): value is RegistryRowProbe {
  return typeof value === 'object' && value !== null && 'registryRowProbe' in value
}

/** The fake result bubble of one native tool call. */
interface ResultBubbleProbe {
  resultBubbleProbe: true
  selector: string
}

const RESULT_BUBBLE_SELECTOR = /^\[data-testid="message-bubble"\]\[data-tool-call-id="[^"]+"\]\[data-tool-row-role="result"\]:visible$/

/** The selector that the helper must use for the result bubble of one call ID. */
function resultBubbleSelector(callId: string): string {
  return `[data-testid="message-bubble"][data-tool-call-id="${callId}"][data-tool-row-role="result"]:visible`
}

function isResultBubbleProbe(value: unknown): value is ResultBubbleProbe {
  return typeof value === 'object' && value !== null && 'resultBubbleProbe' in value
}

function selectTab(id: string): void {
  if (!browser.tabs.has(id))
    throw new Error(`The fake browser has no tab ${id}.`)
  browser.selected = id
}

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  sendMessage: async (_page: Page, prompt: string) => browser.send(prompt),
  userBubbles: () => rows('user'),
  assistantBubbles: () => rows('assistant'),
  messageContents: () => rows(undefined),
  tabById: (_page: Page, id: string) => ({ click: async () => selectTab(id) }),
}))

vi.mock('./nativeResultView', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeResultView')>(),
  // The real helper clicks the Expand button of the result view. The fake reveals the hidden rows of the selected tab.
  expandNativeResultView: async (result: unknown) => {
    if (!isResultBubbleProbe(result))
      throw new Error('The fake expands only the result bubble of a native tool call.')
    browser.expansions.push(result.selector)
    for (const row of browser.tabs.get(browser.selected) ?? [])
      row.expanded = true
    browser.onExpand?.()
  },
}))

vi.mock('./subagentRegistry', async importOriginal => ({
  ...await importOriginal<typeof import('./subagentRegistry')>(),
  requireRegistryRow: async () => registryRow(),
  openChildTabFromRow: async () => {
    selectTab(browser.childAgentId)
    return browser.childAgentId
  },
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  // A fake assertion reads the current fake state once. The simulated agent writes each row before the helper can observe it.
  const check = (value: unknown, message?: string) => {
    if (isTranscriptProbe(value)) {
      return {
        toBeVisible: async () => expect(value.count(), value.label()).toBeGreaterThan(0),
        toHaveCount: async (expected: number) => expect(value.count(), value.label()).toBe(expected),
      }
    }
    if (isRegistryRowProbe(value)) {
      return {
        toHaveAttribute: async (name: string, expected: string | RegExp) => {
          const attribute = await value.getAttribute(name)
          if (typeof expected === 'string')
            expect(attribute, `the registry row ${name}`).toBe(expected)
          else
            expect(attribute, `the registry row ${name}`).toMatch(expected)
        },
      }
    }
    return expect(value, message)
  }
  return {
    ...actual,
    expect: Object.assign(check, {
      poll: (read: () => Promise<unknown>) => ({
        not: { toBe: async (unexpected: unknown) => expect(await read()).not.toBe(unexpected) },
      }),
    }),
  }
})

const status: MockModelScenarioStatus = { complete: true, nextStep: 2, stepCount: 2, ruleMatches: {}, pendingGates: [], requests: [], unexpectedRequests: [] }

describe('completeLiveChildTranscript', () => {
  it('waits for queued model steps before provider completion work', async () => {
    const steps = deferred<void>()
    const entered = deferred<void>()
    const completion = vi.fn(async () => {})
    const finished = completeLiveChildTranscript({ waitForSteps: async () => {
      entered.resolve()
      await steps.promise
      return status
    } }, completion)
    await withCleanup(async () => {
      await entered.promise
      expect(completion).not.toHaveBeenCalled()
      steps.resolve()
      await finished
      expect(completion).toHaveBeenCalledOnce()
    }, async () => {
      steps.resolve()
      await finished
    })
  })

  it('preserves a provider completion failure', async () => {
    const failure = new Error('The native parent report failed.')
    await expect(completeLiveChildTranscript({ waitForSteps: async () => status }, async () => {
      throw failure
    })).rejects.toBe(failure)
  })

  it('keeps the initial step wait when the provider supplies no completion hook', async () => {
    const waitForSteps = vi.fn(async () => status)
    await completeLiveChildTranscript({ waitForSteps })
    expect(waitForSteps).toHaveBeenCalledOnce()
  })
})

const PROVIDER = AgentProvider.OH_MY_PI
const PARENT_TAB = 'unit-parent-tab'
const CHILD_AGENT = 'unit-child-agent'
const CHILD_TASK = 'Complete the unit child assignment.'
const PARENT_TASK = 'Delegate the unit child assignment.'
const READ_TOOL = readToolCall(PROVIDER, 'unit-read-name', '/unit-read-name').name
const DEFAULT_FINAL: Omit<MockModelStep, 'gate'> = { text: 'CHILD_LIVE_DONE' }
const YIELD_FINAL: Omit<MockModelStep, 'gate'> = { toolCalls: [ohMyPiYieldToolCall('unit-native-yield', 'CHILD_LIVE_DONE')] }

/** One model answer as the simulated native agent received it. */
interface ReceivedStep {
  text?: string
  toolCalls?: MockModelToolCall[]
}

interface NativeAgentReceipt {
  /** Each answer of the child, in request order. */
  childResponses: ReceivedStep[]
  /** The file that the child read, with its exact bytes, and the ID of the Read call. */
  read?: { path: string, content: string, callId: string }
}

/** Parse one Chat Completions answer of the actual mock model server. */
function parseChatAnswer(text: string): ReceivedStep {
  const body: unknown = JSON.parse(text)
  const choice: unknown = isRecord(body) && Array.isArray(body.choices) ? body.choices[0] : undefined
  const message: unknown = isRecord(choice) ? choice.message : undefined
  if (!isRecord(message) || message.role !== 'assistant' || (message.content !== null && typeof message.content !== 'string'))
    throw new Error(`The mock model answer holds no assistant message: ${text}`)
  const calls: unknown = message.tool_calls ?? []
  if (!Array.isArray(calls))
    throw new Error(`The mock model answer holds malformed tool calls: ${text}`)
  const toolCalls = calls.map((call: unknown): MockModelToolCall => {
    const fn: unknown = isRecord(call) ? call.function : undefined
    if (!isRecord(call) || typeof call.id !== 'string' || !isRecord(fn) || typeof fn.name !== 'string' || typeof fn.arguments !== 'string')
      throw new Error(`The mock model answer holds a malformed tool call: ${text}`)
    const args: unknown = JSON.parse(fn.arguments)
    if (!isRecord(args))
      throw new Error(`The mock model tool call holds no argument object: ${text}`)
    return { id: call.id, name: fn.name, arguments: args }
  })
  return { ...(message.content === null ? {} : { text: message.content }), ...(toolCalls.length > 0 ? { toolCalls } : {}) }
}

/** Send one non-streaming Chat Completions request, as a native agent does. */
async function chat(serverURL: string, messages: readonly unknown[]): Promise<ReceivedStep> {
  const response = await fetch(`${serverURL}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ model: MOCK_MODELS.zai, stream: false, messages }),
  })
  const text = await response.text()
  if (response.status !== 200)
    throw new Error(`The mock model refused a simulated native request: ${response.status} ${text}`)
  return parseChatAnswer(text)
}

function assistantMessage(step: ReceivedStep): Record<string, unknown> {
  return {
    role: 'assistant',
    content: step.text ?? null,
    ...(step.toolCalls ? { tool_calls: step.toolCalls.map(call => ({ id: call.id, type: 'function', function: { name: call.name, arguments: JSON.stringify(call.arguments) } })) } : {}),
  }
}

function findString(value: unknown, accept: (text: string) => boolean): string | undefined {
  if (typeof value === 'string')
    return accept(value) ? value : undefined
  const children = Array.isArray(value) ? value : isRecord(value) ? Object.values(value) : []
  for (const child of children) {
    const found = findString(child, accept)
    if (found !== undefined)
      return found
  }
  return undefined
}

function appendRow(tab: string, kind: RowKind, text: string, hiddenUntilExpanded?: string): void {
  const transcript = browser.tabs.get(tab)
  if (!transcript)
    throw new Error(`The fake browser has no tab ${tab}.`)
  transcript.push({ kind, text, ...(hiddenUntilExpanded === undefined ? {} : { hiddenUntilExpanded }) })
}

/**
 * Act as a native parent and its native child against the actual mock model server.
 * The child performs a real file Read when its answer asks for one.
 * It writes each visible row before its next model request, so a held request finds the rows in place.
 */
async function runNativeAgent(serverURL: string, parentPrompt: string, log: string[], options: { leakReadToParent: boolean, collapseRead: boolean }): Promise<NativeAgentReceipt> {
  appendRow(PARENT_TAB, 'user', parentPrompt)
  const spawn = await chat(serverURL, [{ role: 'user', content: parentPrompt }])
  const spawnCall = spawn.toolCalls?.[0]
  if (!spawnCall || spawn.toolCalls?.length !== 1)
    throw new Error('The simulated parent did not receive exactly one spawn call.')
  const childPrompt = findString(spawnCall.arguments, text => text.includes(CHILD_TASK))
  if (childPrompt === undefined)
    throw new Error('The spawn call holds no child prompt.')
  appendRow(PARENT_TAB, 'tool', spawnCall.name)
  browser.tabs.set(CHILD_AGENT, [])
  browser.childAgentId = CHILD_AGENT
  browser.childStatus = 'running'
  appendRow(CHILD_AGENT, 'user', childPrompt)

  const receipt: NativeAgentReceipt = { childResponses: [] }
  const childMessages: unknown[] = [{ role: 'user', content: childPrompt }]
  for (;;) {
    const answer = await chat(serverURL, childMessages)
    receipt.childResponses.push(answer)
    const readCall = answer.toolCalls?.find(call => call.name === READ_TOOL)
    if (!readCall) {
      log.push('final-delivered')
      appendRow(CHILD_AGENT, 'assistant', answer.text ?? JSON.stringify(answer.toolCalls))
      break
    }
    log.push('read-delivered')
    if (receipt.read)
      throw new Error('The simulated child received a second Read.')
    const path = findString(readCall.arguments, text => text.startsWith('/'))
    if (path === undefined)
      throw new Error('The Read call holds no absolute path.')
    const content = readFileSync(path, 'utf8')
    receipt.read = { path, content, callId: readCall.id }
    if (options.collapseRead) {
      // A native Read result can put header rows before the file text. The result view shows only the first rows until the reader expands it.
      appendRow(CHILD_AGENT, 'tool', `<path>${path}</path>\n<type>file</type>\n<content>`, `\n1: ${content}</content>`)
    }
    else {
      appendRow(CHILD_AGENT, 'tool', `${path}\n${content}`)
    }
    if (options.leakReadToParent)
      appendRow(PARENT_TAB, 'tool', content)
    childMessages.push(assistantMessage(answer), { role: 'tool', tool_call_id: readCall.id, content })
  }
  browser.childStatus = 'completed'

  const report = await chat(serverURL, [
    { role: 'user', content: parentPrompt },
    assistantMessage(spawn),
    { role: 'tool', tool_call_id: spawnCall.id, content: 'The unit child ended.' },
  ])
  appendRow(PARENT_TAB, 'assistant', report.text ?? '')
  return receipt
}

function fakePage(): Page {
  // The fake supplies only the two page queries that the helper makes directly.
  return Object.assign({} as Page, {
    locator: (selector: string) => {
      if (selector === '[data-testid="tab"][data-tab-type="agent"]')
        return { first: () => ({ getAttribute: async (name: string) => name === 'data-tab-id' ? PARENT_TAB : null }) }
      if (selector === '[data-tool-message]:visible')
        return rows('tool')
      if (RESULT_BUBBLE_SELECTOR.test(selector))
        return { resultBubbleProbe: true, selector }
      throw new Error(`The fake page has no locator for ${selector}.`)
    },
  })
}

interface LiveChildRun {
  /** The helper under test. */
  helper: Promise<void>
  /** The simulated native agent. It finishes after the helper releases the held child answer. */
  agent: Promise<NativeAgentReceipt>
  /** The order of the native deliveries and the helper's gate releases. */
  log: string[]
}

let workingDir = ''

function startLiveChild(serverURL: string, script: ModelScript, options: {
  childResponse?: Omit<MockModelStep, 'gate'>
  read: boolean
  leakReadToParent?: boolean
  /** The native Read result puts header rows before the file text, so its collapsed view hides the marker. */
  collapseRead?: boolean
  /** The spec asks the helper to expand the result view of the Read. */
  expandResult?: boolean
  failRelease?: Error
}): LiveChildRun {
  const log: string[] = []
  browser.onExpand = () => log.push('result-expanded')
  const agent = deferred<NativeAgentReceipt>()
  // The test reads this promise later. This branch keeps an early failure from becoming an unhandled rejection.
  agent.promise.catch(() => {})
  browser.send.mockImplementationOnce(async (prompt) => {
    runNativeAgent(serverURL, prompt, log, { leakReadToParent: options.leakReadToParent ?? false, collapseRead: options.collapseRead ?? false }).then(agent.resolve, agent.reject)
  })
  const observed: ModelScript = {
    ...script,
    releaseGate: async (gate) => {
      log.push('gate-released')
      await script.releaseGate(gate)
    },
    releaseGateIfHeld: async (gate) => {
      log.push('cleanup-release')
      if (options.failRelease)
        throw options.failRelease
      return script.releaseGateIfHeld(gate)
    },
  }
  const spec: LiveChildSpec = {
    provider: PROVIDER,
    childWhen: { user: CHILD_TASK },
    childTask: CHILD_TASK,
    parentTask: PARENT_TASK,
    ...(options.childResponse ? { childResponse: options.childResponse } : {}),
    ...(options.read ? { toolProof: { workingDir, ...(options.expandResult ? { expandResult: true } : {}) } } : {}),
  }
  return { helper: exerciseLiveChildTranscript(fakePage(), observed, spec), agent: agent.promise, log }
}

/** Run one test body against an actual mock model server and an actual model script. */
async function withLiveChildScenario(run: (serverURL: string, script: ModelScript) => Promise<void>): Promise<void> {
  const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
  await withCleanup(async () => {
    const lifecycle = await startModelScript(server.url)
    let passed = false
    await withCleanup(async () => {
      await run(server.url, lifecycle.script)
      passed = true
    }, async () => {
      // A failed body can leave a simulated request held. Release it before the scenario ends.
      for (const gate of (await lifecycle.script.status()).pendingGates)
        await lifecycle.script.releaseGateIfHeld(gate)
      await lifecycle.finish(passed)
    })
  }, () => server.close())
}

function expectOrder(log: readonly string[], order: readonly string[]): void {
  const positions = order.map(entry => log.indexOf(entry))
  expect(positions.every(position => position >= 0), `the log ${JSON.stringify(log)} holds ${JSON.stringify(order)}`).toBe(true)
  expect([...positions].sort((left, right) => left - right), `the log ${JSON.stringify(log)} orders ${JSON.stringify(order)}`).toEqual(positions)
}

/** Require one actual native Read of the file that the helper wrote with its computed marker. */
function expectMarkerRead(receipt: NativeAgentReceipt): void {
  expect(receipt.read, 'the child read the marker file').toBeDefined()
  expect(dirname(receipt.read?.path ?? '')).toBe(workingDir)
  expect(receipt.read?.content).toMatch(/^CHILDREAD[0-9a-f]{32}\n$/)
  expect(readFileSync(receipt.read?.path ?? '', 'utf8')).toBe(receipt.read?.content)
  expect(receipt.childResponses[0]).toEqual({ toolCalls: [{ ...readToolCall(PROVIDER, 'unused', receipt.read?.path ?? ''), id: expect.any(String) }] })
}

describe('exerciseLiveChildTranscript', () => {
  beforeEach(() => {
    const scratch = resolve(import.meta.dirname, '../../../../.tmp')
    mkdirSync(scratch, { recursive: true })
    workingDir = mkdtempSync(join(scratch, 'live-child-transcript-'))
    browser.tabs = new Map([[PARENT_TAB, []]])
    browser.selected = PARENT_TAB
    browser.childAgentId = ''
    browser.childStatus = ''
    browser.expansions = []
    browser.onExpand = undefined
    browser.send.mockReset()
  })

  afterEach(() => rmSync(workingDir, { recursive: true, force: true }))

  it('holds the native yield tool response when the child needs no Read', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { childResponse: YIELD_FINAL, read: false })
      await run.helper
      const receipt = await run.agent
      expect(receipt.childResponses).toEqual([YIELD_FINAL])
      expect(receipt.read).toBeUndefined()
      expectOrder(run.log, ['gate-released', 'final-delivered'])
      expect((await script.status()).pendingGates).toEqual([])
    })
  })

  it('holds the default text when the child needs no Read', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { read: false })
      await run.helper
      const receipt = await run.agent
      expect(receipt.childResponses).toEqual([DEFAULT_FINAL])
      expectOrder(run.log, ['gate-released', 'final-delivered'])
    })
  })

  it('sends the native yield tool response after the child Read and holds only that response', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { childResponse: YIELD_FINAL, read: true })
      await run.helper
      const receipt = await run.agent
      expectMarkerRead(receipt)
      expect(receipt.childResponses).toHaveLength(2)
      expect(receipt.childResponses[1]).toEqual(YIELD_FINAL)
      // The Read answer arrives at once. Only the final answer waits for the helper's release.
      expectOrder(run.log, ['read-delivered', 'gate-released', 'final-delivered'])
      expect((await script.status()).pendingGates).toEqual([])
    })
  })

  it('sends a custom text response after the child Read', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const custom = { text: 'The custom native child report.' }
      const run = startLiveChild(serverURL, script, { childResponse: custom, read: true })
      await run.helper
      const receipt = await run.agent
      expectMarkerRead(receipt)
      expect(receipt.childResponses[1]).toEqual(custom)
      expectOrder(run.log, ['read-delivered', 'gate-released', 'final-delivered'])
    })
  })

  it('sends the default text after the child Read', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { read: true })
      await run.helper
      const receipt = await run.agent
      expectMarkerRead(receipt)
      expect(receipt.childResponses[1]).toEqual(DEFAULT_FINAL)
      expectOrder(run.log, ['read-delivered', 'gate-released', 'final-delivered'])
      // A spec that leaves expandResult unset never expands a result view.
      expect(browser.expansions).toEqual([])
    })
  })

  it('expands the result view of the child Read before it looks for a marker below the collapsed rows', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { read: true, collapseRead: true, expandResult: true })
      await run.helper
      const receipt = await run.agent
      expectMarkerRead(receipt)
      // The helper selects the result of the exact Read call that it scripted, in the visible copy of the row.
      expect(browser.expansions).toEqual([resultBubbleSelector(receipt.read?.callId ?? '')])
      expectOrder(run.log, ['read-delivered', 'result-expanded', 'gate-released', 'final-delivered'])
      expect((await script.status()).pendingGates).toEqual([])
    })
  })

  it('does not expand a result view that the spec leaves alone, so a marker below the collapsed rows fails the helper', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { read: true, collapseRead: true })
      await expect(run.helper).rejects.toThrow(`in tab ${CHILD_AGENT}`)
      const receipt = await run.agent
      expectMarkerRead(receipt)
      expect(browser.expansions).toEqual([])
      expect(run.log).not.toContain('gate-released')
      expectOrder(run.log, ['read-delivered', 'cleanup-release', 'final-delivered'])
      expect((await script.status()).pendingGates).toEqual([])
    })
  })

  it('releases the held final response when the parent transcript holds the child marker', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { read: true, leakReadToParent: true })
      await expect(run.helper).rejects.toThrow(`in tab ${PARENT_TAB}`)
      const receipt = await run.agent
      expectMarkerRead(receipt)
      expect(receipt.childResponses[1]).toEqual(DEFAULT_FINAL)
      expect(run.log).not.toContain('gate-released')
      expectOrder(run.log, ['read-delivered', 'cleanup-release', 'final-delivered'])
      expect((await script.status()).pendingGates).toEqual([])
    })
  })

  it('reports the transcript failure and the failed release of the held response together', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const releaseFailure = new Error('The mock model refused the gate release.')
      const run = startLiveChild(serverURL, script, { read: true, leakReadToParent: true, failRelease: releaseFailure })
      const failure: unknown = await run.helper.then(() => undefined, (error: unknown) => error)
      expect(failure).toBeInstanceOf(AggregateError)
      const errors: unknown[] = failure instanceof AggregateError ? failure.errors : []
      expect(errors).toHaveLength(2)
      expect(String(errors[0])).toContain(`in tab ${PARENT_TAB}`)
      expect(errors[1]).toBeInstanceOf(AggregateError)
      expect(errors[1] instanceof AggregateError ? errors[1].errors : []).toEqual([releaseFailure])
      // The failed release leaves the final child answer held, so the failure is real.
      const held = (await script.status()).pendingGates
      expect(held).toHaveLength(1)
      await script.releaseGate(held[0] ?? '')
      const receipt = await run.agent
      expect(receipt.childResponses[1]).toEqual(DEFAULT_FINAL)
    })
  })
})
