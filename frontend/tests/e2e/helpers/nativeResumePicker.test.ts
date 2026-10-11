import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelScenarioStatus, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { StoredSessionList } from './nativeResume'
import type { ResumePickerFixtures, ResumePickerNativeContext, ResumePickerOptions } from './nativeResumePicker'
import type { ManagedNativeScenarioContext, NativeModelTurn } from './nativeScenario'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AGENT_E2E_SETTINGS } from '../agentSettings'
import { stepRequest } from './mockModelScript'
import { openResumeSubject, resumePickerScenario, sessionPickerRepository } from './nativeResumePicker'

const STORED_SESSION = 'stored-native-session'
const KEEPER_ID = 'keeper-agent'
const SUBJECT_ID = 'subject-agent'
const REOPENED_ID = 'reopened-agent'
const PROMPT_MARK = 'marked:'

const picker = vi.hoisted(() => ({
  events: [] as string[],
  userRows: [] as string[],
  assistantRows: [] as string[],
  /** The model requests that the fake server recorded. A snapshot copies them, as the real status read does. */
  requests: [] as MockModelRequestRecord[],
  queued: [] as MockModelStep[],
  sent: [] as string[],
  idleTimeouts: [] as (number | undefined)[],
  /** How many reads of the agent list return no session yet. */
  emptySessionReads: 0,
  openOptions: [] as unknown[],
  /** The arguments of each picker reopen. */
  reopens: [] as { provider: AgentProvider, workingDir: string, sessionId: string, list?: StoredSessionList }[],
  conversation: [] as { agentId: string, originalAnswerRows: number, originalAnswerBubbles: number | undefined }[],
  /** The refusal of the close that waits for the Worker, when a test states one. */
  closeRefusal: undefined as Error | undefined,
  /** How many bubbles the page draws for each answer of a turn, in turn order. A turn with no entry draws one. */
  bubblesPerAnswer: [] as number[],
  /** Whether a model request restates the earlier answers. A provider that keeps its history on a service does not. */
  historyInBody: true,
}))

vi.mock('./api', () => ({
  createWorkspaceViaAPI: async (_hubUrl: string, _token: string, title: string) => {
    picker.events.push(`workspace:${title.split(' ')[0]}`)
    return 'unit-workspace'
  },
  openAgentViaAPI: async (_server: unknown, _workspaceId: string, workingDir: string, options: { title?: string }) => {
    picker.openOptions.push({ workingDir, ...options })
    picker.events.push(`open:${options.title}`)
    return options.title === 'Keeper' ? KEEPER_ID : SUBJECT_ID
  },
}))

vi.mock('./worktree', () => ({
  createGitRepo: (dataDir: string, name: string) => {
    picker.events.push(`repo:${name.split('-')[1]}`)
    return `${dataDir}/${name}`
  },
}))
vi.mock('./workerTabs', () => ({
  // The bare close waits for nothing, so the scenario must not call it.
  closeAgentViaAPI: async (_hubUrl: string, _token: string, _workerId: string, agentId: string) => {
    picker.events.push(`close-without-wait:${agentId}`)
    return { failureMessage: '', failureDetail: '' }
  },
  closeNativeAgentAndWait: async (_context: unknown, agentId: string) => {
    picker.events.push(`close:${agentId}`)
    if (picker.closeRefusal)
      throw picker.closeRefusal
  },
}))

vi.mock('./nativeResume', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeResume')>(),
  countOriginalAnswerRows: async (_context: unknown, agentId: string) => {
    picker.events.push(`count-rows:${agentId}`)
    return 2
  },
  expectReopenedNativeAgent: async (_context: unknown, stored: { agentSessionId: string }, earlierAgentIds: readonly string[]) => {
    picker.events.push(`reopened:${stored.agentSessionId}:${earlierAgentIds.join(',')}`)
    return create(AgentInfoSchema, { id: REOPENED_ID })
  },
  expectNativeResumeContext: (turns: unknown) => { picker.events.push(`context:${JSON.stringify(turns)}`) },
  expectResumedConversation: async (_context: unknown, agentId: string, _texts: unknown, originalAnswerRows: number, originalAnswerBubbles?: number) => {
    picker.events.push(`conversation:${agentId}`)
    picker.conversation.push({ agentId, originalAnswerRows, originalAnswerBubbles })
  },
  // nativeResume.test.ts proves the picker flow. This file proves what the scenario asks of it.
  reopenFromSessionPicker: async (_page: Page, options: { provider: AgentProvider, workingDir: string, sessionId: string, list?: StoredSessionList }) => {
    picker.events.push(`reopen:${options.sessionId}`)
    picker.reopens.push(options)
  },
}))

vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  nativeModelConversationTurns: (request: MockModelRequestRecord): NativeModelTurn[] => [{ role: 'assistant', text: `default reader:${request.stepIndex}` }],
  nativeAgentById: async (_context: unknown, agentId: string) => {
    picker.events.push('list-agents')
    const agentSessionId = picker.emptySessionReads > 0 ? '' : STORED_SESSION
    picker.emptySessionReads = Math.max(0, picker.emptySessionReads - 1)
    return create(AgentInfoSchema, { id: agentId, agentSessionId })
  },
}))

/** Locate chat rows in memory. A filter keeps the rows that hold its text. */
function rows(texts: () => readonly string[], filters: readonly string[] = []) {
  const matching = () => texts().filter(text => filters.every(filter => text.includes(filter)))
  return {
    resumePickerProbe: 'rows',
    readCount: () => matching().length,
    count: async () => matching().length,
    filter: ({ hasText }: { hasText: string }) => rows(texts, [...filters, hasText]),
    first: () => ({ resumePickerProbe: 'first-row', readCount: () => Math.min(1, matching().length) }),
  }
}

vi.mock('./ui', () => ({
  agentTabs: (page: Page) => page.locator('[data-testid="tab"][data-tab-type="agent"]'),
  assistantBubbles: () => rows(() => picker.assistantRows),
  userBubbles: () => rows(() => picker.userRows),
  loginViaToken: async () => { picker.events.push('login') },
  openWorkspace: async () => { picker.events.push('open-workspace') },
  sendMessage: async (_page: Page, text: string) => {
    const prompt = text.slice(PROMPT_MARK.length)
    picker.events.push(`send:${prompt.split(' ')[0]}`)
    picker.sent.push(prompt)
    picker.userRows.push(prompt)
    // The model server records the request when the prompt arrives.
    const stepIndex = picker.requests.length
    const answers = picker.historyInBody ? picker.queued.slice(0, stepIndex).map(step => step.text) : []
    picker.requests.push({ protocol: 'openai-responses', path: '/v1/responses', stepIndex, body: { prompts: [...picker.sent], answers } })
  },
  waitForAgentIdle: async (_page: Page, timeoutMs?: number) => {
    picker.idleTimeouts.push(timeoutMs)
    picker.events.push('idle')
    // The turn ends: the page shows the answer, and the native client states more of its request.
    const request = picker.requests.at(-1)
    const step = picker.queued[picker.requests.length - 1]
    if (request && step?.text !== undefined) {
      const bubbles = picker.bubblesPerAnswer[picker.requests.length - 1] ?? 1
      for (let bubble = 0; bubble < bubbles; bubble++)
        picker.assistantRows.push(step.text)
      request.body = { ...(request.body as object), stated: `after-turn:${request.stepIndex}` }
    }
  },
}))

/** A fake locator that records its clicks and answers the chained calls that the scenario makes. */
function control(label: string): Locator {
  return Object.assign({} as Locator, {
    resumePickerProbe: 'control',
    label,
    click: async () => { picker.events.push(`click:${label}`) },
    filter: () => control(label),
    first: () => control(label),
  })
}

vi.mock('@playwright/test', async (importOriginal) => {
  const original = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown) => {
    if (typeof value !== 'object' || value === null || !('resumePickerProbe' in value))
      return original.expect(value)
    const readCount = 'readCount' in value && typeof value.readCount === 'function' ? value.readCount : undefined
    return {
      toHaveCount: async (expected: number) => {
        if (!readCount)
          throw new Error('The picker fixture holds no count for this locator.')
        picker.events.push(`count:${expected}`)
        expect(readCount()).toBe(expected)
      },
      toBeVisible: async () => {
        if (!readCount || value.resumePickerProbe !== 'first-row')
          throw new Error('The picker fixture checks the visibility of a first row only.')
        picker.events.push('visible')
        expect(readCount()).toBe(1)
      },
    }
  }
  const poll = (read: () => Promise<unknown>) => ({
    not: {
      toBe: async (expected: unknown) => {
        for (let attempt = 0; attempt < 5; attempt++) {
          if (!Object.is(await read(), expected))
            return
        }
        throw new Error('The picker poll never changed.')
      },
    },
  })
  return { ...original, expect: Object.assign(check, { poll }) }
})

/** One fake model script. `status` and `waitForSteps` copy the recorded requests at the time of the call. */
function fakeModelScript(): ModelScript {
  const snapshot = (): MockModelScenarioStatus => ({
    complete: false,
    nextStep: picker.requests.length,
    stepCount: picker.requests.length,
    ruleMatches: {},
    pendingGates: [],
    requests: structuredClone(picker.requests),
    unexpectedRequests: [],
  })
  const unused = (name: string) => async (): Promise<never> => {
    throw new Error(`The picker fixture does not use the model script member ${name}.`)
  }
  return {
    id: 'picker-unit',
    testDeadline: () => undefined,
    prompt: text => `${PROMPT_MARK}${text}`,
    queue: async (...steps) => {
      const first = picker.queued.length
      picker.queued.push(...steps)
      picker.events.push(`queue:${steps.length}`)
      return first
    },
    requestAt: async (stepIndex) => {
      picker.events.push(`request-at:${stepIndex}`)
      return stepRequest(snapshot(), stepIndex)
    },
    rule: async (...rules) => { picker.events.push(`rule:${rules.length}`) },
    fallback: unused('fallback'),
    status: async () => {
      picker.events.push('status')
      return snapshot()
    },
    waitForSteps: async (count) => {
      picker.events.push(`steps:${count}`)
      return snapshot()
    },
    waitForGate: unused('waitForGate'),
    releaseGate: unused('releaseGate'),
    releaseGateIfHeld: unused('releaseGateIfHeld'),
    allowUnconsumed: () => {},
  }
}

function pickerFixtures(): ResumePickerFixtures {
  const page = Object.assign({} as Page, {
    locator: (selector: string) => control(selector),
  })
  return {
    page,
    modelScript: fakeModelScript(),
    leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unit-token', workerId: 'unit-worker', dataDir: '/unit/data' },
  }
}

/** The `nativeContext` of a Codex-like provider directory, with the provider fields that a test states. */
function nativeContext(fields: Partial<ManagedNativeScenarioContext> = {}): ResumePickerNativeContext {
  return async (fixtures) => {
    picker.events.push(`native-context:${fixtures.workspaceId}`)
    return { ...fixtures, provider: AgentProvider.CODEX, providerAgent: { provider: AgentProvider.CODEX, prefix: 'native-e2e' }, ...fields }
  }
}

function run(options: Partial<ResumePickerOptions> = {}, fields: Partial<ManagedNativeScenarioContext> = {}) {
  return resumePickerScenario(pickerFixtures(), nativeContext(fields), { label: 'Unit', ...options })
}

beforeEach(() => {
  picker.events.length = 0
  picker.userRows.length = 0
  picker.assistantRows.length = 0
  picker.requests.length = 0
  picker.queued.length = 0
  picker.sent.length = 0
  picker.idleTimeouts.length = 0
  picker.openOptions.length = 0
  picker.conversation.length = 0
  picker.reopens.length = 0
  picker.emptySessionReads = 0
  picker.closeRefusal = undefined
  picker.bubblesPerAnswer = []
  picker.historyInBody = true
})

describe('sessionPickerRepository', () => {
  it('creates a repository of its own under the data directory, with a new name at each call', () => {
    const first = sessionPickerRepository('/unit/data', 'picker-one-')
    const second = sessionPickerRepository('/unit/data', 'picker-one-')
    expect(first).toMatch(/^\/unit\/data\/picker-one-[0-9a-f-]{36}$/)
    expect(second).toMatch(/^\/unit\/data\/picker-one-[0-9a-f-]{36}$/)
    expect(first).not.toBe(second)
    expect(picker.events).toEqual(['repo:one', 'repo:one'])
  })

  it('refuses a data directory that is not an absolute path', () => {
    expect(() => sessionPickerRepository('unit/data', 'picker-one-')).toThrow('must be an absolute path')
  })
})

describe('openResumeSubject', () => {
  it('opens the keeper and the subject in repositories of their own, and selects the subject', async () => {
    const subject = await openResumeSubject(pickerFixtures(), { label: 'Unit' })
    expect(subject).toEqual({ workspaceId: 'unit-workspace', keeperId: KEEPER_ID, subjectId: SUBJECT_ID, subjectDir: expect.stringContaining('/unit/data/resume-subject-') })
    expect(picker.events).toEqual(['repo:keeper', 'repo:subject', 'workspace:Unit', 'open:Keeper', 'open:Subject', 'login', 'open-workspace', 'click:[data-testid="tab"][data-tab-type="agent"]'])
    // With no subject options, the subject opens with the Worker default.
    expect(picker.openOptions[1]).toEqual({ workingDir: subject.subjectDir, title: 'Subject' })
  })

  it('takes the subject options from the workspace before either agent opens', async () => {
    const subjectOptions = vi.fn(async (workspaceId: string) => {
      picker.events.push(`options:${workspaceId}`)
      return { agentProvider: AgentProvider.CODEX, model: 'unit-model', optionValues: { effort: 'low' } }
    })
    await openResumeSubject(pickerFixtures(), { label: 'Unit', subjectOptions })
    expect(picker.events.indexOf('options:unit-workspace')).toBeGreaterThan(picker.events.indexOf('workspace:Unit'))
    expect(picker.events.indexOf('options:unit-workspace')).toBeLessThan(picker.events.indexOf('open:Keeper'))
    expect(picker.openOptions[0]).toEqual({ workingDir: expect.stringContaining('resume-keeper-'), title: 'Keeper' })
    expect(picker.openOptions[1]).toMatchObject({ agentProvider: AgentProvider.CODEX, model: 'unit-model', optionValues: { effort: 'low' }, title: 'Subject' })
  })
})

describe('resumePickerScenario', () => {
  it('runs the shared flow in order and proves the reopened agent after the resumed turn ends', async () => {
    const result = await run()
    expect(picker.events).toEqual([
      'repo:keeper',
      'repo:subject',
      'workspace:Unit',
      'native-context:unit-workspace',
      'open:Keeper',
      'open:Subject',
      'login',
      'open-workspace',
      'click:[data-testid="tab"][data-tab-type="agent"]',
      'queue:1',
      'send:Keep',
      'steps:1',
      'idle',
      'count:1',
      'visible',
      'request-at:0',
      'list-agents',
      'count-rows:subject-agent',
      `close:${SUBJECT_ID}`,
      `reopen:${STORED_SESSION}`,
      `reopened:${STORED_SESSION}:${KEEPER_ID},${SUBJECT_ID}`,
      'count:1',
      'count:1',
      'queue:1',
      'send:Reply',
      'steps:2',
      'idle',
      'request-at:1',
      expect.stringMatching(/^context:/),
      'count:1',
      `conversation:${REOPENED_ID}`,
    ])
    expect(result.request.stepIndex).toBe(1)
  })

  it('fails before the reopen when the Worker refuses the close of the subject', async () => {
    picker.closeRefusal = new Error('The Worker refused to close agent subject-agent: Failed to close agent (database is locked)')
    await expect(run()).rejects.toBe(picker.closeRefusal)
    expect(picker.events).toContain(`close:${SUBJECT_ID}`)
    expect(picker.reopens).toEqual([])
  })

  it('returns the original request, read after its turn ended, beside the resumed request', async () => {
    const result = await run()
    expect(result.originalRequest.stepIndex).toBe(0)
    expect(result.originalRequest.body).toMatchObject({ stated: 'after-turn:0' })
    expect(result.request.stepIndex).toBe(1)
  })

  it('returns the texts of the scenario with the settled resumed request', async () => {
    const result = await run()
    expect(result.marker).toMatch(/^[a-f0-9]{32}$/)
    expect(result.originalAnswer).toBe(`RESUMEANSWER${result.marker}`)
    expect(picker.sent[0]).toBe(result.originalPrompt)
    expect(picker.sent[1]).toBe(result.resumedPrompt)
    expect(picker.queued.map(step => step.text)).toEqual([result.originalAnswer, result.resumedAnswer])
    expect(result.request.stepIndex).toBe(1)
  })

  it('reads the resumed request after the turn ends, with the fields that the native client states late', async () => {
    const seen: MockModelRequestRecord[] = []
    const result = await run({
      onResumedRequest: (request) => {
        seen.push(request)
      },
    })
    // The record that waitForSteps returned lacks the late field, because the mock counts a step when its request arrives.
    expect(seen).toHaveLength(1)
    expect(seen[0]?.body).toMatchObject({ stated: 'after-turn:1' })
    expect(result.request).toBe(seen[0])
    const read = picker.events.lastIndexOf('request-at:1')
    expect(read).toBeGreaterThan(picker.events.lastIndexOf('idle'))
    expect(read).toBeGreaterThan(picker.events.lastIndexOf('steps:2'))
  })

  it('hands the original-turn request to onFirstTurn before that turn ends', async () => {
    const seen: MockModelRequestRecord[] = []
    await run({
      onFirstTurn: (request) => {
        seen.push(request)
        picker.events.push('first-turn')
      },
    })
    expect(seen).toHaveLength(1)
    expect(seen[0]?.stepIndex).toBe(0)
    // The first-turn hook sees the request before the turn settles, so it holds no late field.
    expect(seen[0]?.body).not.toHaveProperty('stated')
    expect(picker.events.indexOf('first-turn')).toBeGreaterThan(picker.events.indexOf('steps:1'))
    expect(picker.events.indexOf('first-turn')).toBeLessThan(picker.events.indexOf('idle'))
  })

  it('runs the provider assertion on the resumed request before the context proof and the conversation proof', async () => {
    await run({
      onResumedRequest: () => {
        picker.events.push('provider-assertion')
      },
    })
    const provider = picker.events.indexOf('provider-assertion')
    expect(provider).toBeGreaterThan(picker.events.indexOf('request-at:1'))
    expect(provider).toBeLessThan(picker.events.findIndex(event => event.startsWith('context:')))
    expect(provider).toBeLessThan(picker.events.indexOf(`conversation:${REOPENED_ID}`))
  })

  it('does not run the resumed provider assertion when the original answer is absent from the request body', async () => {
    const providerAssertion = vi.fn()
    // A request body that never repeats the original answer fails the default proof before the provider assertion.
    picker.historyInBody = false
    await expect(run({ onResumedRequest: providerAssertion })).rejects.toMatchObject({ matcherResult: { name: 'toContain', message: expect.stringContaining('RESUMEANSWER') } })
    expect(picker.events).toContain('request-at:1')
    expect(providerAssertion).not.toHaveBeenCalled()
  })

  it('accepts a request body without the original answer when the provider keeps its history on a service', async () => {
    const seen = vi.fn()
    picker.historyInBody = false
    await run({ resumedBodyHoldsOriginalAnswer: false, onResumedRequest: seen })
    expect(seen).toHaveBeenCalledOnce()
  })

  it('reads the conversation turns of the resumed request through the generic reader by default', async () => {
    await run()
    expect(picker.events.find(event => event.startsWith('context:'))).toBe('context:[{"role":"assistant","text":"default reader:1"}]')
  })

  it('reads the conversation turns through the reader of the provider context', async () => {
    const read = vi.fn((request: MockModelRequestRecord): NativeModelTurn[] => [{ role: 'assistant', text: `provider reader:${request.stepIndex}` }])
    await run({}, { readConversationTurns: read })
    expect(read).toHaveBeenCalledOnce()
    expect(picker.events.find(event => event.startsWith('context:'))).toBe('context:[{"role":"assistant","text":"provider reader:1"}]')
  })

  it('refuses a resumed prompt that reached no model request', async () => {
    const fixtures = pickerFixtures()
    const withoutRequests = async () => ({ ...(await fixtures.modelScript.status()), requests: [] })
    const script: ModelScript = {
      ...fixtures.modelScript,
      status: withoutRequests,
      waitForSteps: withoutRequests,
      // The original turn keeps its request. Only the resumed prompt reaches no request.
      requestAt: async stepIndex => stepIndex === 0 ? fixtures.modelScript.requestAt(0) : stepRequest(await withoutRequests(), stepIndex),
    }
    const scenario = resumePickerScenario({ ...fixtures, modelScript: script }, nativeContext(), { label: 'Unit' })
    await expect(scenario).rejects.toThrow('The model script holds no request for step 1')
    expect(picker.events).not.toContain(`conversation:${REOPENED_ID}`)
  })

  describe('session list', () => {
    it('reopens the stored session of the subject directory, and lists it by its id by default', async () => {
      await run()
      expect(picker.reopens).toEqual([{ provider: AgentProvider.CODEX, workingDir: expect.stringContaining('/unit/data/resume-subject-'), sessionId: STORED_SESSION }])
    })

    it('passes the stated session list to the picker', async () => {
      await run({ sessionList: 'sole-session' })
      expect(picker.reopens).toEqual([expect.objectContaining({ sessionId: STORED_SESSION, list: 'sole-session' })])
    })
  })

  describe('conversation bubbles', () => {
    it('checks one original prompt bubble and a drawn original answer before the close', async () => {
      await run()
      const close = picker.events.indexOf(`close:${SUBJECT_ID}`)
      expect(picker.events.slice(picker.events.indexOf('idle'), close).filter(event => event.startsWith('count:') || event === 'visible')).toEqual(['count:1', 'visible'])
    })

    it('fails before the close when the live transcript draws no original answer', async () => {
      picker.bubblesPerAnswer = [0]
      // The bubble count of the original answer fails: none where the proof requires one.
      await expect(run()).rejects.toThrow('expected +0 to be 1')
      expect(picker.events).not.toContain(`close:${SUBJECT_ID}`)
    })

    it('requires the resumed answer to draw as many bubbles as the original answer drew', async () => {
      picker.bubblesPerAnswer = [3, 3]
      await run()
      const afterResume = picker.events.slice(picker.events.indexOf('steps:2'))
      expect(afterResume.filter(event => event.startsWith('count:'))).toEqual(['count:3'])
      expect(picker.conversation).toEqual([{ agentId: REOPENED_ID, originalAnswerRows: 2, originalAnswerBubbles: 3 }])
    })

    it('fails when the resumed answer draws another number of bubbles than the original answer', async () => {
      picker.bubblesPerAnswer = [2, 1]
      // The bubble count of the resumed answer fails: one where the original answer drew two.
      await expect(run()).rejects.toThrow('expected 1 to be 2')
      expect(picker.events).not.toContain(`conversation:${REOPENED_ID}`)
    })

    it('passes the pre-close bubble count and the stored row count to the conversation proof', async () => {
      await run()
      expect(picker.conversation).toEqual([{ agentId: REOPENED_ID, originalAnswerRows: 2, originalAnswerBubbles: 1 }])
    })
  })

  describe('idle timeout', () => {
    it('leaves the helper default for both turns when the spec gives none', async () => {
      await run()
      expect(picker.idleTimeouts).toEqual([undefined, undefined])
    })
  })

  describe('answers and rules', () => {
    it('queues a plain text answer for each turn by default and registers no rule', async () => {
      await run()
      expect(picker.queued).toHaveLength(2)
      for (const step of picker.queued)
        expect(Object.keys(step)).toEqual(['text'])
      expect(picker.events.some(event => event.startsWith('rule:'))).toBe(false)
    })

    it('queues the answer step of the provider context for each turn', async () => {
      const answers: string[] = []
      const result = await run({}, {
        textStep: (text) => {
          answers.push(text)
          return { text, toolCalls: [] }
        },
      })
      expect(answers).toEqual([result.originalAnswer, result.resumedAnswer])
      expect(picker.queued.every(step => step.toolCalls?.length === 0)).toBe(true)
    })

    it('builds the provider context for the workspace that it opens, before it opens an agent there', async () => {
      await run()
      expect(picker.events.indexOf('native-context:unit-workspace')).toBeGreaterThan(picker.events.indexOf('workspace:Unit'))
      expect(picker.events.indexOf('native-context:unit-workspace')).toBeLessThan(picker.events.indexOf('open:Keeper'))
    })
  })

  describe('worker session', () => {
    it('polls the Worker until the subject agent states its native session', async () => {
      picker.emptySessionReads = 3
      await run()
      expect(picker.events.filter(event => event === 'list-agents')).toHaveLength(4)
      expect(picker.events).toContain(`reopened:${STORED_SESSION}:${KEEPER_ID},${SUBJECT_ID}`)
    })

    it('gives the subject agent the provider defaults and the keeper no provider', async () => {
      await run()
      const [keeper, subject] = picker.openOptions
      expect(keeper).toMatchObject({ title: 'Keeper' })
      expect(keeper).not.toHaveProperty('agentProvider')
      const pinned = AGENT_E2E_SETTINGS[AgentProvider.CODEX]
      expect(subject).toMatchObject({ title: 'Subject', agentProvider: AgentProvider.CODEX, model: pinned.model, optionValues: { effort: pinned.effort } })
    })

    it('uses separate working directories for the keeper and the subject', async () => {
      await run()
      const [keeper, subject] = picker.openOptions as { workingDir: string }[]
      expect(keeper?.workingDir).toContain('resume-keeper-')
      expect(subject?.workingDir).toContain('resume-subject-')
      expect(keeper?.workingDir).not.toBe(subject?.workingDir)
    })

    it('merges the subject option values over the provider defaults', async () => {
      await run({ subjectOptionValues: { effort: 'low', mode: 'plan' } })
      expect(picker.openOptions[1]).toMatchObject({ optionValues: { effort: 'low', mode: 'plan' } })
    })

    it('keeps the provider option values when the spec gives none', async () => {
      await run({ subjectOptionValues: {} })
      expect(picker.openOptions[1]).toMatchObject({ optionValues: { effort: AGENT_E2E_SETTINGS[AgentProvider.CODEX].effort } })
    })
  })
})
