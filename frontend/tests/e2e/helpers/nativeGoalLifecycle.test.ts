import type { Page } from '@playwright/test'
import type { MockModelRule, MockModelToolCall } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeGoalPauseTiming } from './nativeGoalLifecycle'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from './cleanup'
import { MOCK_MODEL_IDS, MOCK_MODELS } from './mockAgentEnvironment'
import { isRecord, lastUserText, matchesRequest, systemText } from './mockModelScript'
import { createMockModelServer } from './mockModelServer'
import { startModelScript } from './modelScriptFixture'
import { exerciseNativeGoalPauseAndResume } from './nativeGoalLifecycle'

type GoalState = 'none' | 'active' | 'paused'

/** The fake browser of one test. The simulated native agent below owns the goal state. */
interface FakeBrowser {
  goal: GoalState
  /** The text of the goal editor, as the helper filled it. */
  editorText: string
  workingDir: string
  /** The limit of a fake `expect.poll`. A test that expects a failure lowers it. */
  pollTimeoutMs: number
  /** What each goal action does in the simulated native agent. */
  act: (action: string) => Promise<void>
  /** Start the goal that the editor holds. */
  submit: () => void
}

const browser = vi.hoisted((): FakeBrowser => ({
  goal: 'none',
  editorText: '',
  workingDir: '',
  pollTimeoutMs: 5000,
  act: async () => {},
  submit: () => {},
}))

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  applyPermissionPreset: async () => {},
}))

vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: async () => create(AgentInfoSchema, { workingDir: browser.workingDir }),
}))

/** Read a fake state until it satisfies a condition, as a Playwright poll does. */
async function pollFor<T>(read: () => T | Promise<T>, accept: (value: T) => boolean): Promise<void> {
  const deadline = Date.now() + browser.pollTimeoutMs
  for (;;) {
    const value = await read()
    if (accept(value))
      return
    if (Date.now() > deadline)
      throw new Error(`The fake poll never accepted its value. The last value is ${JSON.stringify(value)}.`)
    await new Promise(resolveTimer => setTimeout(resolveTimer, 10))
  }
}

vi.mock('./subagentRegistry', async importOriginal => ({
  ...await importOriginal<typeof import('./subagentRegistry')>(),
  expandGoalsAndTodosSection: async () => {},
  openGoalMenu: async () => {},
  goalAction: (_page: Page, action: string) => ({ click: async () => browser.act(action) }),
  expectGoalStatus: async (_page: Page, status: string) => pollFor(() => browser.goal, goal => goal === status),
}))

/** A fake locator that stands for a count of visible elements. */
interface CountProbe {
  countProbe: true
  count: () => number
  filter: (options: { hasText: string }) => CountProbe
}

function countProbe(count: () => number): CountProbe {
  return { countProbe: true, count, filter: () => countProbe(count) }
}

function isCountProbe(value: unknown): value is CountProbe {
  return typeof value === 'object' && value !== null && 'countProbe' in value
}

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (isCountProbe(value)) {
      return {
        toHaveCount: async (expected: number) => expect(value.count(), message).toBe(expected),
        toBeVisible: async () => expect(value.count(), message).toBeGreaterThan(0),
      }
    }
    return expect(value, message)
  }
  return {
    ...actual,
    expect: Object.assign(check, {
      poll: <T>(read: () => T | Promise<T>) => ({
        toBeGreaterThan: (floor: number) => pollFor(read, value => typeof value === 'number' && value > floor),
        not: { toContain: (item: string) => pollFor(read, value => Array.isArray(value) && !value.includes(item)) },
      }),
    }),
  }
})

const SESSION_TITLE_SYSTEM = 'You are tasked with generating the session title. The user is asking almost always software engineering related questions on their codebase.'
const GROK_ROUND_SYSTEM = 'You are Grok released by xAI. You are an interactive CLI tool that helps users with software engineering tasks.'
const QWEN_ROUND_SYSTEM = 'You are Qwen Code, a CLI agent operating through an ACP host developed by Alibaba Group, specializing in software engineering tasks.'
const GROK_ROUND_PATTERN = '^You are Grok released by xAI\\b'

/** How the simulated native agent behaves. Each field states one fact of a probed provider. */
interface AgentProfile {
  provider: AgentProvider
  /** The system prompt of a goal round request. */
  system: string
  /** Whether the agent asks for a session title before its first round, as Grok does. */
  titleFirst: boolean
  pause: NativeGoalPauseTiming
  /** A pause at once reports the pause but keeps its model request. No probed provider does this. */
  keepsRequestOnPause?: true
}

interface RoundReceipt {
  kind: 'title' | 'round'
  outcome: 'answered' | 'cancelled'
  toolCalls?: MockModelToolCall[]
}

/**
 * Act as a native agent with a goal against the actual mock model server.
 *
 * - A pause at once cancels the running model request, as Qwen Code does.
 * - A pause after the round waits for the round, as Grok Build does.
 * - A resumed round that the model answers with text only ends in a pause of the
 *   agent's own, as the Qwen no-progress rule and the Grok evaluator do.
 */
class SimulatedGoalAgent {
  readonly receipts: RoundReceipt[] = []
  private objective = ''
  private pausePending = false
  private inflight: AbortController | undefined
  private work: Promise<void> = Promise.resolve()

  constructor(private readonly serverURL: string, private readonly script: ModelScript, private readonly profile: AgentProfile) {}

  /** Wait for the round that runs now, and surface its failure. */
  settle(): Promise<void> {
    return this.work
  }

  start(): void {
    this.objective = browser.editorText
    browser.goal = 'active'
    this.work = this.runRound(true)
  }

  async act(action: string): Promise<void> {
    if (action === 'pause') {
      if (this.profile.pause === 'at-once') {
        if (!this.profile.keepsRequestOnPause) {
          this.inflight?.abort()
          // The browser learns of the pause after the agent ended its request, so wait until the model server saw the request end.
          await pollFor(async () => (await this.script.status()).pendingGates, gates => gates.length === 0)
        }
        browser.goal = 'paused'
      }
      else {
        this.pausePending = true
      }
    }
    else if (action === 'resume') {
      browser.goal = 'active'
      this.work = this.runRound(false)
    }
    else if (action === 'clear') {
      browser.goal = 'none'
    }
  }

  private async runRound(first: boolean): Promise<void> {
    if (first && this.profile.titleFirst)
      await this.request('title', [])
    const answer = await this.request('round', [])
    if (answer?.toolCalls) {
      for (const call of answer.toolCalls)
        this.runWriteTool(call)
      await this.request('round', [
        { role: 'assistant', content: null, tool_calls: answer.toolCalls.map(call => ({ id: call.id, type: 'function', function: { name: call.name, arguments: JSON.stringify(call.arguments) } })) },
        ...answer.toolCalls.map(call => ({ role: 'tool', tool_call_id: call.id, content: 'The file was written.' })),
      ])
    }
    if (answer === undefined)
      return
    if (this.pausePending || !first) {
      this.pausePending = false
      browser.goal = 'paused'
    }
  }

  /** Run the write tool of the answer: create the file whose path the call states. */
  private runWriteTool(call: MockModelToolCall): void {
    const path = Object.values(call.arguments ?? {}).find((value): value is string => typeof value === 'string' && value.startsWith('/'))
    if (path === undefined)
      throw new Error(`The write call ${call.name} states no absolute path.`)
    writeFileSync(path, 'The simulated agent ran the write tool.\n')
  }

  /** Send one model request. Returns the answer, or undefined when the agent cancelled the request. */
  private async request(kind: 'title' | 'round', extra: readonly unknown[]): Promise<{ toolCalls?: MockModelToolCall[] } | undefined> {
    const controller = new AbortController()
    this.inflight = controller
    const user = kind === 'title'
      ? `<user_query>\n<system-reminder>\nA goal has been set: ${this.objective}\n</system-reminder>\n</user_query>`
      : `<user_query>\n${this.objective}\n</user_query>`
    try {
      const response = await fetch(`${this.serverURL}/v1/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          model: MOCK_MODELS.zai,
          stream: false,
          messages: [{ role: 'system', content: kind === 'title' ? SESSION_TITLE_SYSTEM : this.profile.system }, { role: 'user', content: user }, ...extra],
        }),
        signal: controller.signal,
      })
      const body: unknown = await response.json()
      if (response.status !== 200)
        throw new Error(`The mock model refused the simulated request: ${response.status} ${JSON.stringify(body)}`)
      const choice: unknown = isRecord(body) && Array.isArray(body.choices) ? body.choices[0] : undefined
      const message: unknown = isRecord(choice) ? choice.message : undefined
      const calls: unknown[] = isRecord(message) && Array.isArray(message.tool_calls) ? message.tool_calls : []
      const toolCalls = calls.map((call): MockModelToolCall => {
        const fn: unknown = isRecord(call) ? call.function : undefined
        if (!isRecord(call) || typeof call.id !== 'string' || !isRecord(fn) || typeof fn.name !== 'string' || typeof fn.arguments !== 'string')
          throw new Error(`The mock model answer holds a malformed tool call: ${JSON.stringify(call)}`)
        const args: unknown = JSON.parse(fn.arguments)
        return { id: call.id, name: fn.name, arguments: isRecord(args) ? args : {} }
      })
      this.receipts.push({ kind, outcome: 'answered', ...(toolCalls.length > 0 ? { toolCalls } : {}) })
      return toolCalls.length > 0 ? { toolCalls } : {}
    }
    catch (error) {
      if (!controller.signal.aborted)
        throw error
      this.receipts.push({ kind, outcome: 'cancelled' })
      return undefined
    }
    finally {
      if (this.inflight === controller)
        this.inflight = undefined
    }
  }
}

function fakePage(): Page {
  // The fake supplies only the page queries that the helper makes directly.
  return Object.assign({} as Page, {
    locator: (selector: string) => {
      if (selector === '[data-testid="goal-editor"]:visible .ProseMirror')
        return { fill: async (text: string) => { browser.editorText = text } }
      if (selector === '[data-testid="set-goal-submit"]:visible')
        return { click: async () => browser.submit() }
      // The simulated agent never queues a command, so the queue holds no item.
      if (selector === '[data-testid="agent-input-queue"]:visible')
        return countProbe(() => 0)
      if (selector === '[data-testid="goal-card-empty"]:visible')
        return countProbe(() => browser.goal === 'none' ? 1 : 0)
      throw new Error(`The fake page has no locator for ${selector}.`)
    },
  })
}

interface GoalRun {
  /** The helper under test. */
  helper: Promise<void>
  agent: SimulatedGoalAgent
  script: ModelScript
  /** Every rule that the helper registered, in order. */
  rules: MockModelRule[]
  releaseGate: ReturnType<typeof vi.fn<ModelScript['releaseGate']>>
}

/** Run one test body against an actual mock model server and an actual model script. */
async function withGoalScenario(run: (serverURL: string, script: ModelScript) => Promise<void>): Promise<void> {
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

function startGoal(serverURL: string, script: ModelScript, profile: AgentProfile, options: { roundSystem?: string } = {}): GoalRun {
  const agent = new SimulatedGoalAgent(serverURL, script, profile)
  browser.goal = 'none'
  browser.editorText = ''
  browser.act = action => agent.act(action)
  browser.submit = () => agent.start()
  const rules: MockModelRule[] = []
  const releaseGate = vi.fn<ModelScript['releaseGate']>(gate => script.releaseGate(gate))
  const observed: ModelScript = {
    ...script,
    rule: async (...added) => {
      rules.push(...added)
      await script.rule(...added)
    },
    releaseGate,
  }
  const helper = exerciseNativeGoalPauseAndResume(
    { page: fakePage(), modelScript: observed, provider: profile.provider, leapmuxServer: { hubUrl: '', adminToken: '', workerId: '' }, workspaceId: '' },
    { pauseTiming: profile.pause, ...(options.roundSystem === undefined ? {} : { roundSystem: options.roundSystem }), pausedProof: async () => {} },
  )
  return { helper, agent, script: observed, rules, releaseGate }
}

const QWEN: AgentProfile = { provider: AgentProvider.QWEN_CODE, system: QWEN_ROUND_SYSTEM, titleFirst: false, pause: 'at-once' }
const GROK: AgentProfile = { provider: AgentProvider.GROK_BUILD, system: GROK_ROUND_SYSTEM, titleFirst: true, pause: 'after-the-round' }

describe('exerciseNativeGoalPauseAndResume', () => {
  beforeEach(() => {
    const scratch = resolve(import.meta.dirname, '../../../../.tmp')
    mkdirSync(scratch, { recursive: true })
    browser.workingDir = mkdtempSync(join(scratch, 'native-goal-lifecycle-'))
    browser.pollTimeoutMs = 5000
  })

  afterEach(() => rmSync(browser.workingDir, { recursive: true, force: true }))

  it('proves that a pause at once cancels the held round and releases no gate', async () => {
    await withGoalScenario(async (serverURL, script) => {
      const run = startGoal(serverURL, script, QWEN)
      await run.helper
      await run.agent.settle()
      // The pause ended the model request, so the gate holds nothing to release.
      expect(run.releaseGate).not.toHaveBeenCalled()
      expect(run.agent.receipts[0]).toEqual({ kind: 'round', outcome: 'cancelled' })
      expect(existsSync(join(browser.workingDir, 'native-goal-progress.txt'))).toBe(false)
      expect((await run.script.status()).pendingGates).toEqual([])
    })
  })

  it('fails when a pause at once leaves the round request held', async () => {
    await withGoalScenario(async (serverURL, script) => {
      browser.pollTimeoutMs = 300
      const run = startGoal(serverURL, script, { ...QWEN, keepsRequestOnPause: true })
      await expect(run.helper).rejects.toThrow('The fake poll never accepted its value')
      // The helper's cleanup released the held request, so the agent finishes its round.
      await run.agent.settle()
    })
  })

  it('holds the round of a Grok goal, not its session title', async () => {
    await withGoalScenario(async (serverURL, script) => {
      const run = startGoal(serverURL, script, GROK, { roundSystem: GROK_ROUND_PATTERN })
      await run.helper
      await run.agent.settle()
      const [title, round, followUp] = run.agent.receipts
      // The housekeeping rule answers the title at once, with the tool that Grok forces.
      expect(title).toMatchObject({ kind: 'title', outcome: 'answered', toolCalls: [{ name: 'session_title' }] })
      expect(round).toMatchObject({ kind: 'round', outcome: 'answered', toolCalls: [expect.objectContaining({ id: 'native-goal-progress' })] })
      expect(followUp).toEqual({ kind: 'round', outcome: 'answered' })
      expect(existsSync(join(browser.workingDir, 'native-goal-progress.txt'))).toBe(true)
      expect(run.releaseGate).toHaveBeenCalledOnce()
    })
  })

  it('lets the session title take the gate when no round system pattern separates it', async () => {
    await withGoalScenario(async (serverURL, script) => {
      const run = startGoal(serverURL, script, GROK)
      // The title request quotes the goal, so the gated rule answers it and the real round runs with no gate.
      await expect(run.helper).rejects.toThrow('the round before the pause finished its tool call')
      await run.agent.settle()
    })
  })

  it('applies the round system pattern to both rules of the goal', async () => {
    await withGoalScenario(async (serverURL, script) => {
      const run = startGoal(serverURL, script, GROK, { roundSystem: GROK_ROUND_PATTERN })
      await run.helper
      await run.agent.settle()
      expect(run.rules.map(rule => rule.name)).toEqual([
        expect.stringMatching(/^native-goal-first-NATIVEGOAL[0-9a-f]{32}$/),
        expect.stringMatching(/^native-goal-following-NATIVEGOAL[0-9a-f]{32}$/),
      ])
      const request = (system: string) => {
        const body = { messages: [{ role: 'system', content: system }, { role: 'user', content: browser.editorText }] }
        return { protocol: 'openai-chat-completions' as const, systemText: systemText(body), userText: lastUserText(body), body }
      }
      for (const rule of run.rules) {
        expect(matchesRequest(rule.when, request(GROK_ROUND_SYSTEM)), `${rule.name} answers a round`).toBe(true)
        expect(matchesRequest(rule.when, request(SESSION_TITLE_SYSTEM)), `${rule.name} leaves the title to housekeeping`).toBe(false)
      }
    })
  })
})
