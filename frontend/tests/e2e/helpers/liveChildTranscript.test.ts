import type { Locator, Page } from '@playwright/test'
import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { LiveChild, LiveChildSpec } from './liveChildTranscript'
import type { MockModelScenarioStatus, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { HeldNativeChild } from './runningChildProof'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { markdownToPlainText } from '../../../src/lib/markdownPlainText'
import { deferred } from '../../../src/test-support/async'
import { withCleanup } from './cleanup'
import { completeLiveChildTranscript, exerciseLiveChildTranscript, expectChildToolOutputDeferred, LIVE_CHILD_SHELL_CALL_ID, writeChildMarkerFile } from './liveChildTranscript'
import { MOCK_MODEL_IDS, MOCK_MODELS } from './mockAgentEnvironment'
import { createMockModelServer } from './mockModelServer'
import { startModelScript } from './modelScriptFixture'
import { bashToolCall, ohMyPiYieldToolCall, readToolCall } from './providerToolCalls'

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
  /** The registry guard and the prompt send, in the order in which the helper made them. */
  events: [] as string[],
  /** How many times the helper reloaded the page. */
  reloads: 0,
}))

/** The messages that the Worker stores for the child, which the fake message read returns. */
const stored = vi.hoisted(() => ({ messages: [] as unknown[] }))

vi.mock('./nativeMessages', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeMessages')>(),
  readAllAgentMessages: async () => stored.messages,
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
  sendMessage: async (_page: Page, prompt: string) => {
    browser.events.push('send')
    return browser.send(prompt)
  },
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
  expectNoRegistryRows: async () => {
    browser.events.push('registry-guard')
  },
  requireRegistryRow: async () => registryRow(),
  openChildTabFromRow: async () => {
    selectTab(browser.childAgentId)
    return browser.childAgentId
  },
}))

// The helper reads the parent from the selected agent tab, which the fake browser tracks.
vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  selectedAgentTabId: async () => browser.selected,
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
const SHELL_TOOL = bashToolCall(PROVIDER, 'unit-shell-name', 'true').name
const SHELL_COMMAND = 'printf unit-child-live'
const DEFAULT_FINAL: Omit<MockModelStep, 'gate'> = { text: 'CHILD_LIVE_DONE' }
const YIELD_FINAL: Omit<MockModelStep, 'gate'> = { toolCalls: [ohMyPiYieldToolCall('unit-native-yield', 'CHILD_LIVE_DONE')] }

/** A child task with inline code. It holds `CHILD_TASK`, which the scripted matcher reads. */
const MARKDOWN_TASK = `${CHILD_TASK} Run \`printf unit-markdown\` once.`

/** The words that the bubble of {@link MARKDOWN_TASK} shows. */
const MARKDOWN_TASK_WORDS = `${CHILD_TASK} Run printf unit-markdown once.`

/** A held answer with strong emphasis. */
const MARKDOWN_ANSWER = { text: 'The **custom** child report.' }

/** The words that the bubble of {@link MARKDOWN_ANSWER} shows. */
const MARKDOWN_ANSWER_WORDS = 'The custom child report.'

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

/** How the simulated native agent draws its transcripts. Each field states a defect that a test injects. */
interface NativeAgentOptions {
  leakReadToParent: boolean
  collapseRead: boolean
  /** The child tab shows this assistant text before the child asks its model, as a premature final answer would. */
  earlyAnswer?: string
  /** The child tab never shows the final answer. */
  hideFinal: boolean
  /** The child tab never shows the row of the shell command. */
  hideShellRow: boolean
}

/** Parse one Chat Completions answer of the actual mock model server. */
function parseChatAnswer(text: string): ReceivedStep {
  const body: unknown = JSON.parse(text)
  const choice: unknown = isObject(body) && Array.isArray(body.choices) ? body.choices[0] : undefined
  const message: unknown = isObject(choice) ? choice.message : undefined
  if (!isObject(message) || message.role !== 'assistant' || (message.content !== null && typeof message.content !== 'string'))
    throw new Error(`The mock model answer holds no assistant message: ${text}`)
  const calls: unknown = message.tool_calls ?? []
  if (!Array.isArray(calls))
    throw new Error(`The mock model answer holds malformed tool calls: ${text}`)
  const toolCalls = calls.map((call: unknown): MockModelToolCall => {
    const fn: unknown = isObject(call) ? call.function : undefined
    if (!isObject(call) || typeof call.id !== 'string' || !isObject(fn) || typeof fn.name !== 'string' || typeof fn.arguments !== 'string')
      throw new Error(`The mock model answer holds a malformed tool call: ${text}`)
    const args: unknown = JSON.parse(fn.arguments)
    if (!isObject(args))
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
  const children = Array.isArray(value) ? value : isObject(value) ? Object.values(value) : []
  for (const child of children) {
    const found = findString(child, accept)
    if (found !== undefined)
      return found
  }
  return undefined
}

/**
 * Add one visible row to a tab.
 * The chat renders the text of a user or assistant row as Markdown, so such a row shows the words of its text without
 * the Markdown syntax, such as the backticks of inline code. The fake draws those rows the same way.
 */
function appendRow(tab: string, kind: RowKind, text: string, hiddenUntilExpanded?: string): void {
  const transcript = browser.tabs.get(tab)
  if (!transcript)
    throw new Error(`The fake browser has no tab ${tab}.`)
  const shown = kind === 'tool' ? text : markdownToPlainText(text)
  transcript.push({ kind, text: shown, ...(hiddenUntilExpanded === undefined ? {} : { hiddenUntilExpanded }) })
}

/**
 * Act as a native parent and its native child against the actual mock model server.
 * The child performs a real file Read when its answer asks for one.
 * It writes each visible row before its next model request, so a held request finds the rows in place.
 */
async function runNativeAgent(serverURL: string, parentPrompt: string, log: string[], options: NativeAgentOptions): Promise<NativeAgentReceipt> {
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
  if (options.earlyAnswer !== undefined)
    appendRow(CHILD_AGENT, 'assistant', options.earlyAnswer)

  const receipt: NativeAgentReceipt = { childResponses: [] }
  const childMessages: unknown[] = [{ role: 'user', content: childPrompt }]
  for (;;) {
    const answer = await chat(serverURL, childMessages)
    receipt.childResponses.push(answer)
    const shellCall = answer.toolCalls?.find(call => call.name === SHELL_TOOL)
    if (shellCall) {
      log.push('shell-delivered')
      if (!options.hideShellRow)
        appendRow(CHILD_AGENT, 'tool', JSON.stringify(shellCall.arguments))
      childMessages.push(assistantMessage(answer), { role: 'tool', tool_call_id: shellCall.id, content: 'unit-child-live' })
      continue
    }
    const readCall = answer.toolCalls?.find(call => call.name === READ_TOOL)
    if (!readCall) {
      log.push('final-delivered')
      if (!options.hideFinal)
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
  // The fake supplies only the two page queries that the helper makes directly, and a reload that it counts.
  return Object.assign({} as Page, {
    reload: async () => {
      browser.reloads += 1
    },
    locator: (selector: string) => {
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
  helper: Promise<LiveChild>
  /** The simulated native agent. It finishes after the helper releases the held child answer. */
  agent: Promise<NativeAgentReceipt>
  /** The order of the native deliveries and the helper's gate releases. */
  log: string[]
}

let workingDir = ''

function startLiveChild(serverURL: string, script: ModelScript, options: {
  /** The task of the child. It must hold `CHILD_TASK`, which the scripted matcher and the simulated parent read. */
  childTask?: string
  childResponse?: Omit<MockModelStep, 'gate'>
  /** The tool that the spec asks the child to run. */
  tool?: 'read' | 'shell'
  leakReadToParent?: boolean
  /** The native Read result puts header rows before the file text, so its collapsed view hides the marker. */
  collapseRead?: boolean
  /** The spec asks the helper to expand the result view of the Read. */
  expandResult?: boolean
  failRelease?: Error
  earlyAnswer?: string
  hideFinal?: boolean
  hideShellRow?: boolean
  finalAnswerInChildTab?: boolean
}): LiveChildRun {
  const log: string[] = []
  browser.onExpand = () => log.push('result-expanded')
  const agent = deferred<NativeAgentReceipt>()
  // The test reads this promise later. This branch keeps an early failure from becoming an unhandled rejection.
  agent.promise.catch(() => {})
  browser.send.mockImplementationOnce(async (prompt) => {
    runNativeAgent(serverURL, prompt, log, {
      leakReadToParent: options.leakReadToParent ?? false,
      collapseRead: options.collapseRead ?? false,
      ...(options.earlyAnswer === undefined ? {} : { earlyAnswer: options.earlyAnswer }),
      hideFinal: options.hideFinal ?? false,
      hideShellRow: options.hideShellRow ?? false,
    }).then(agent.resolve, agent.reject)
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
    childWhen: { user: CHILD_TASK },
    childTask: options.childTask ?? CHILD_TASK,
    parentTask: PARENT_TASK,
    ...(options.childResponse ? { childResponse: options.childResponse } : {}),
    ...(options.tool === 'read' ? { toolProof: { read: { workingDir, ...(options.expandResult ? { expandResult: true } : {}) } } } : {}),
    ...(options.tool === 'shell' ? { toolProof: { shell: { command: SHELL_COMMAND } } } : {}),
    ...(options.finalAnswerInChildTab === undefined ? {} : { finalAnswerInChildTab: options.finalAnswerInChildTab }),
  }
  const context = { page: fakePage(), modelScript: observed, provider: PROVIDER, leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' } }
  return { helper: exerciseLiveChildTranscript(context, spec), agent: agent.promise, log }
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
    browser.events = []
  })

  afterEach(() => rmSync(workingDir, { recursive: true, force: true }))

  it('holds the native yield tool response when the child needs no Read', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { childResponse: YIELD_FINAL })
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
      const run = startLiveChild(serverURL, script, {})
      await run.helper
      const receipt = await run.agent
      expect(receipt.childResponses).toEqual([DEFAULT_FINAL])
      expectOrder(run.log, ['gate-released', 'final-delivered'])
    })
  })

  it('sends the native yield tool response after the child Read and holds only that response', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { childResponse: YIELD_FINAL, tool: 'read' })
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
      const run = startLiveChild(serverURL, script, { childResponse: custom, tool: 'read' })
      await run.helper
      const receipt = await run.agent
      expectMarkerRead(receipt)
      expect(receipt.childResponses[1]).toEqual(custom)
      expectOrder(run.log, ['read-delivered', 'gate-released', 'final-delivered'])
    })
  })

  it('sends the default text after the child Read', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { tool: 'read' })
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
      const run = startLiveChild(serverURL, script, { tool: 'read', collapseRead: true, expandResult: true })
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
      const run = startLiveChild(serverURL, script, { tool: 'read', collapseRead: true })
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
      const run = startLiveChild(serverURL, script, { tool: 'read', leakReadToParent: true })
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
      const run = startLiveChild(serverURL, script, { tool: 'read', leakReadToParent: true, failRelease: releaseFailure })
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

  it('reads the Worker registry before it sends the parent prompt, and returns the child and its parent', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, {})
      expect(await run.helper).toMatchObject({ childId: CHILD_AGENT, parentId: PARENT_TAB })
      await run.agent
      expect(browser.events).toEqual(['registry-guard', 'send'])
    })
  })

  it('shows the shell command of the child in its tab, and holds only the final response', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { tool: 'shell' })
      await run.helper
      const receipt = await run.agent
      expect(receipt.childResponses[0]).toEqual({ toolCalls: [bashToolCall(PROVIDER, LIVE_CHILD_SHELL_CALL_ID, SHELL_COMMAND)] })
      expect(receipt.childResponses[1]).toEqual(DEFAULT_FINAL)
      // The shell answer arrives at once. Only the final answer waits for the helper's release.
      expectOrder(run.log, ['shell-delivered', 'gate-released', 'final-delivered'])
      expect((await script.status()).pendingGates).toEqual([])
    })
  })

  it('fails and releases the held response when the child tab shows no row of the shell command', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { tool: 'shell', hideShellRow: true })
      await expect(run.helper).rejects.toThrow(`the tool rows that hold ${SHELL_COMMAND} in tab ${CHILD_AGENT}`)
      await run.agent
      expect(run.log).not.toContain('gate-released')
      expectOrder(run.log, ['shell-delivered', 'cleanup-release', 'final-delivered'])
    })
  })

  it('fails when the held custom answer shows in the child tab before the release', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const custom = { text: 'The custom native child report.' }
      const run = startLiveChild(serverURL, script, { childResponse: custom, earlyAnswer: custom.text })
      await expect(run.helper).rejects.toThrow(`the assistant rows that hold ${custom.text} in tab ${CHILD_AGENT}`)
      await run.agent
      expect(run.log).not.toContain('gate-released')
    })
  })

  it('checks the held answer text, so the default text in the child tab does not fail a custom answer', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { childResponse: { text: 'The custom native child report.' }, earlyAnswer: 'CHILD_LIVE_DONE' })
      await run.helper
      await run.agent
      expectOrder(run.log, ['gate-released', 'final-delivered'])
    })
  })

  it('requires the final answer in the child tab after the release', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { hideFinal: true })
      await expect(run.helper).rejects.toThrow(`the assistant rows that hold CHILD_LIVE_DONE in tab ${CHILD_AGENT}`)
      await run.agent
      expectOrder(run.log, ['gate-released', 'final-delivered'])
    })
  })

  it('leaves out the final answer check when the spec turns it off', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { hideFinal: true, finalAnswerInChildTab: false })
      await run.helper
      await run.agent
      expectOrder(run.log, ['gate-released', 'final-delivered'])
    })
  })

  it('finds a child task with Markdown syntax by the words that its bubble shows', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { childTask: MARKDOWN_TASK })
      await run.helper
      await run.agent
      // The bubble drops the backticks of the inline code, so the task text of the spec is not in the child tab.
      const shown = (browser.tabs.get(CHILD_AGENT) ?? []).filter(row => row.kind === 'user').map(row => row.text)
      expect(shown.some(text => text.includes(MARKDOWN_TASK_WORDS))).toBe(true)
      expect(shown.some(text => text.includes(MARKDOWN_TASK))).toBe(false)
      expectOrder(run.log, ['gate-released', 'final-delivered'])
    })
  })

  it('fails when the words of a held answer with Markdown syntax show in the child tab before the release', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { childResponse: MARKDOWN_ANSWER, earlyAnswer: MARKDOWN_ANSWER.text })
      await expect(run.helper).rejects.toThrow(`the assistant rows that hold ${MARKDOWN_ANSWER_WORDS} in tab ${CHILD_AGENT}`)
      await run.agent
      expect(run.log).not.toContain('gate-released')
    })
  })

  it('requires the words of a final answer with Markdown syntax in the child tab after the release', async () => {
    await withLiveChildScenario(async (serverURL, script) => {
      const run = startLiveChild(serverURL, script, { childResponse: MARKDOWN_ANSWER })
      await run.helper
      const receipt = await run.agent
      expect(receipt.childResponses).toEqual([MARKDOWN_ANSWER])
      expectOrder(run.log, ['gate-released', 'final-delivered'])
    })
  })

  it.each(['', '   ', '![](child.png)'])('refuses a child task that shows no words, before it acts: %j', async (childTask) => {
    // The guard must refuse the task before the helper uses the model script, which this fake leaves empty.
    const context = { page: fakePage(), modelScript: {} as ModelScript, provider: PROVIDER, leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' } }
    await expect(exerciseLiveChildTranscript(context, { childWhen: { user: CHILD_TASK }, childTask, parentTask: PARENT_TASK }))
      .rejects
      .toThrow('A live child needs a task that shows words in its tab')
    expect(browser.events).toEqual([])
  })
})

const MARKER_FILE = 'native-child-live.txt'
const READ_CALL_ID = 'native-child-read'

describe('expectChildToolOutputDeferred', () => {
  const encoder = new TextEncoder()
  let marker = ''

  beforeEach(() => {
    marker = `NATIVECHILDREAD${crypto.randomUUID().replaceAll('-', '')}`
    browser.tabs = new Map<string, FakeRow[]>([[PARENT_TAB, []], [CHILD_AGENT, [{ kind: 'user', text: CHILD_TASK }]]])
    browser.selected = PARENT_TAB
    browser.childAgentId = CHILD_AGENT
    browser.childStatus = 'running'
    browser.events = []
    browser.reloads = 0
    stored.messages = []
  })

  /** One stored Worker message of the child with `text` as its uncompressed content. */
  function storedMessage(id: string, text: string): AgentChatMessage {
    return create(AgentChatMessageSchema, { id, contentCompression: ContentCompression.NONE, content: encoder.encode(text) })
  }

  /**
   * A held child whose held request carries `readResult` as the result of its Read.
   * `restore` makes the completed child tab show the Read result, as a provider that restores its transcript does.
   */
  function heldChild(options: { readResult: string, restore: boolean }): HeldNativeChild {
    return {
      row: registryRow() as unknown as Locator,
      childId: CHILD_AGENT,
      parentId: PARENT_TAB,
      heldRequest: async () => ({
        protocol: 'openai-chat-completions',
        path: '/v1/chat/completions',
        rule: 'the fake held child answer',
        body: { messages: [{ role: 'tool', tool_call_id: READ_CALL_ID, content: options.readResult }] },
      }),
      finish: async () => {
        browser.events.push('finish')
        browser.childStatus = 'completed'
        if (options.restore)
          appendRow(CHILD_AGENT, 'tool', `${MARKER_FILE}\n${marker}`)
      },
    }
  }

  const context = () => ({ page: fakePage(), leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' } })
  const options = (restoredAfterCompletion: boolean) => ({ marker, fileName: MARKER_FILE, readCallId: READ_CALL_ID, restoredAfterCompletion })

  it('proves the deferred Read result, then finishes the child', async () => {
    stored.messages = [storedMessage('prompt', CHILD_TASK)]
    await expectChildToolOutputDeferred(context(), heldChild({ readResult: marker, restore: false }), options(false))
    expect(browser.events).toEqual(['finish'])
    expect(browser.reloads).toBe(0)
  })

  it('fails and finishes the child when the child tab shows the Read result', async () => {
    appendRow(CHILD_AGENT, 'tool', `${MARKER_FILE}\n${marker}`)
    await expect(expectChildToolOutputDeferred(context(), heldChild({ readResult: marker, restore: false }), options(false))).rejects.toThrow(`in tab ${CHILD_AGENT}`)
    expect(browser.events).toEqual(['finish'])
  })

  it('fails when a stored Worker message of the child holds the Read result', async () => {
    stored.messages = [storedMessage('prompt', CHILD_TASK), storedMessage('read-result', `The file holds ${marker}.`)]
    await expect(expectChildToolOutputDeferred(context(), heldChild({ readResult: marker, restore: false }), options(false))).rejects.toThrow('no stored Worker message of agent')
    expect(browser.events).toEqual(['finish'])
  })

  it('fails when the held request carries no Read result with the marker', async () => {
    await expect(expectChildToolOutputDeferred(context(), heldChild({ readResult: 'another file', restore: false }), options(false))).rejects.toThrow('the native Read result reached the model of the child')
    expect(browser.events).toEqual(['finish'])
  })

  it('requires the Read result after the completion and after a reload when the provider restores it', async () => {
    await expectChildToolOutputDeferred(context(), heldChild({ readResult: marker, restore: true }), options(true))
    expect(browser.events).toEqual(['finish'])
    expect(browser.reloads).toBe(1)
    expect(browser.selected).toBe(CHILD_AGENT)
  })

  it('fails a restored proof when the completed child tab lacks the Read result', async () => {
    await expect(expectChildToolOutputDeferred(context(), heldChild({ readResult: marker, restore: false }), options(true))).rejects.toThrow(`in tab ${CHILD_AGENT}`)
    expect(browser.reloads).toBe(0)
  })
})

describe('writeChildMarkerFile', () => {
  let directory = ''

  beforeEach(() => {
    const scratch = resolve(import.meta.dirname, '../../../../.tmp')
    mkdirSync(scratch, { recursive: true })
    directory = mkdtempSync(join(scratch, 'child-marker-file-'))
  })

  afterEach(() => rmSync(directory, { recursive: true, force: true }))

  it('writes a new unique marker into the file', () => {
    const first = writeChildMarkerFile(directory, 'first.txt')
    const second = writeChildMarkerFile(directory, 'second.txt')
    expect(first.path).toBe(join(directory, 'first.txt'))
    expect(readFileSync(first.path, 'utf8')).toBe(first.marker)
    expect(first.marker).toMatch(/^NATIVECHILDREAD[0-9a-f]{32}$/)
    expect(first.marker).not.toBe(second.marker)
  })

  it('refuses an empty working directory', () => {
    expect(() => writeChildMarkerFile(' ', 'child.txt')).toThrow('working directory')
  })

  it.each(['', 'nested/child.txt', '../child.txt'])('refuses a file name that is not plain: %j', (fileName) => {
    expect(() => writeChildMarkerFile(directory, fileName)).toThrow('plain file name')
  })
})
