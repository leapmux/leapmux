import type { Locator, Page } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { InterruptTurnOptions, NativeResumeEvidence } from './nativeLifecycle'
import type { NativeResumeIdentity, NativeResumeTexts } from './nativeResume'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, expectTypeOf, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseInterruptTurn, exerciseSessionResume, heldToolScript } from './nativeLifecycle'
import { stopProcess } from './process'

const resume = vi.hoisted(() => ({
  events: [] as string[],
  userRows: [] as string[],
  assistantRows: [] as string[],
  priorSessionId: '1df17a24-50da-4090-a7e9-87b38feec450',
  send: vi.fn<(context: ManagedNativeScenarioContext, prompt: string, answer: string) => Promise<MockModelRequestRecord>>(),
  current: vi.fn<(context: ManagedNativeScenarioContext) => Promise<AgentInfo>>(),
  open: vi.fn<() => Promise<string>>(),
  close: vi.fn<() => Promise<{ failureMessage: string }>>(),
  directory: vi.fn<(prefix: string) => string>(),
  picker: vi.fn<(page: Page, options: { provider: AgentProvider, workingDir: string, sessionId: string }) => Promise<void>>(),
  countRows: vi.fn<(context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>, agentId: string, texts: Pick<NativeResumeTexts, 'originalAnswer'>) => Promise<number>>(),
  reopen: vi.fn<(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>, stored: NativeResumeIdentity, earlierAgentIds: readonly string[]) => Promise<AgentInfo>>(),
  conversation: vi.fn<(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>, agentId: string, texts: Pick<NativeResumeTexts, 'originalPrompt' | 'originalAnswer' | 'resumedAnswer'>, originalAnswerRows: number) => Promise<void>>(),
}))

vi.mock('./api', async importOriginal => ({
  ...await importOriginal<typeof import('./api')>(),
  openAgentViaAPI: resume.open,
}))
vi.mock('./nativeConversation', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeConversation')>(),
  sendNativeAnswer: resume.send,
}))
vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: resume.current,
}))
// nativeResume.test.ts proves each proof. This file proves where the resume scenario calls them.
vi.mock('./nativeResume', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeResume')>(),
  countOriginalAnswerRows: resume.countRows,
  expectReopenedNativeAgent: resume.reopen,
  expectResumedConversation: resume.conversation,
  reopenFromSessionPicker: resume.picker,
}))
vi.mock('./runDirectory', async importOriginal => ({
  ...await importOriginal<typeof import('./runDirectory')>(),
  createTestDirectory: resume.directory,
}))
vi.mock('./worktree', async importOriginal => ({
  ...await importOriginal<typeof import('./worktree')>(),
  closeAgentViaAPI: resume.close,
}))

function control(label: string): Locator {
  // The fixture supplies only the browser methods that the real helper calls.
  return Object.assign({} as Locator, {
    resumeProbe: 'control',
    label,
    click: async () => { resume.events.push(`click:${label}`) },
    getByTestId: (id: string) => control(id),
    getByRole: (role: string, options?: { name?: string }) => control(options?.name ?? role),
  })
}

function rows(kind: 'user' | 'assistant') {
  return {
    filter: ({ hasText }: { hasText: string }) => ({
      resumeProbe: 'rows',
      kind,
      readCount: () => (kind === 'user' ? resume.userRows : resume.assistantRows).filter(text => text.includes(hasText)).length,
      // The helper counts the pre-close answer bubbles through the locator's own count.
      count: async () => (kind === 'user' ? resume.userRows : resume.assistantRows).filter(text => text.includes(hasText)).length,
    }),
  }
}

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  assistantBubbles: () => rows('assistant'),
  userBubbles: () => rows('user'),
  tabById: (_page: Page, id: string) => control(id),
}))

vi.mock('@playwright/test', async importOriginal => ({
  ...await importOriginal<typeof import('@playwright/test')>(),
  expect: (value: unknown) => {
    if (typeof value === 'object' && value !== null && 'resumeProbe' in value) {
      if (value.resumeProbe === 'control' && 'label' in value && typeof value.label === 'string') {
        const label = value.label
        return {
          toBeVisible: async () => {
            resume.events.push(`visible:${label}`)
          },
        }
      }
      if (value.resumeProbe === 'rows' && 'kind' in value && typeof value.kind === 'string'
        && 'readCount' in value && typeof value.readCount === 'function') {
        const kind = value.kind
        const readCount = value.readCount
        return {
          toHaveCount: async (expected: number) => {
            resume.events.push(`count:${kind}`)
            expect(readCount()).toBe(expected)
          },
        }
      }
      throw new Error('The resume fixture received an unsupported browser assertion.')
    }
    if (value === resume.priorSessionId) {
      return {
        not: {
          toBe: (expected: unknown) => {
            resume.events.push('assert:original-session')
            expect(value).not.toBe(expected)
          },
        },
        toBe: (expected: unknown) => {
          resume.events.push('assert:resumed-session')
          expect(value).toBe(expected)
        },
      }
    }
    return expect(value)
  },
}))

type ResumePhase = NativeResumeEvidence['phase']

function options(observe?: (evidence: NativeResumeEvidence) => Promise<void>) {
  return {
    prepare: async () => { resume.events.push('prepare') },
    ...(observe ? { resumeEvidence: observe } : {}),
  }
}

function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>((complete) => {
    resolve = complete
  })
  return { promise, resolve }
}

function scenario() {
  const prior = create(AgentInfoSchema, {
    id: 'original-worker-agent',
    workerId: 'resume-unit-worker',
    status: AgentStatus.ACTIVE,
    agentProvider: AgentProvider.CODEX,
    agentSessionId: resume.priorSessionId,
    workingDir: '/controlled/native-resume-project',
  })
  const keeper = create(AgentInfoSchema, { ...prior, id: 'keeper-worker-agent', agentSessionId: '91a4ca8d-77a3-4a0f-ab4f-ae770ed92187' })
  const reopened = create(AgentInfoSchema, { ...prior, id: 'reopened-worker-agent' })
  const page = Object.assign({} as Page, {
    getByRole: (role: string) => control(role),
    getByTestId: (id: string) => control(id),
  })
  const context: ManagedNativeScenarioContext = {
    page,
    provider: AgentProvider.CODEX,
    workspaceId: 'resume-unit-workspace',
    leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'controlled-token', workerId: prior.workerId },
    get modelScript(): ModelScript { throw new Error('The resume unit fixture must not access a model.') },
  }
  const originalRequest: MockModelRequestRecord = { protocol: 'openai-responses', path: '/v1/responses', body: { turn: 'original' } }
  const resumedRequest: MockModelRequestRecord = { protocol: 'openai-responses', path: '/v1/responses', body: { turn: 'resumed', zero: 0, enabled: false } }
  resume.send.mockImplementationOnce(async (_context, prompt, answer) => {
    resume.events.push('answer:original')
    resume.userRows.push(prompt)
    resume.assistantRows.push(answer)
    return originalRequest
  }).mockImplementationOnce(async () => {
    resume.events.push('answer:resumed')
    return resumedRequest
  })
  for (const [phase, agent] of [
    ['original', prior],
    ['keeper', keeper],
    ['resumed', reopened],
  ] as const) {
    resume.current.mockImplementationOnce(async () => {
      resume.events.push(`agent:${phase}`)
      return agent
    })
  }
  resume.open.mockImplementation(async () => {
    resume.events.push('keeper:create')
    return keeper.id
  })
  resume.close.mockImplementation(async () => {
    resume.events.push('original:close')
    return { failureMessage: '' }
  })
  resume.directory.mockImplementation((prefix) => {
    resume.events.push('keeper:directory')
    return `/controlled/${prefix}`
  })
  resume.picker.mockImplementation(async (pickerPage, pickerOptions) => {
    expect(pickerPage).toBe(page)
    expect(pickerOptions).toEqual({ provider: AgentProvider.CODEX, workingDir: prior.workingDir, sessionId: prior.agentSessionId })
    resume.events.push('picker:reopen')
  })
  resume.countRows.mockImplementation(async (rowContext, agentId, texts) => {
    expect(rowContext).toBe(context)
    expect(agentId).toBe(prior.id)
    expect(resume.assistantRows).toContain(texts.originalAnswer)
    resume.events.push('rows:original-answer')
    return 1
  })
  resume.reopen.mockImplementation(async (reopenContext, stored, earlierAgentIds) => {
    expect(reopenContext).toBe(context)
    expect(stored).toBe(prior)
    expect(earlierAgentIds).toEqual([prior.id, keeper.id])
    resume.events.push('verdict:reopened')
    return reopened
  })
  resume.conversation.mockImplementation(async (proofContext, agentId, _texts, originalAnswerRows) => {
    expect(proofContext).toBe(context)
    expect(agentId).toBe(reopened.id)
    expect(originalAnswerRows).toBe(1)
    resume.events.push('conversation:resumed')
  })
  return { context, prior, keeper, resumedRequest, reopened }
}

async function holdEvidence(phase: ResumePhase) {
  const fixture = scenario()
  const entered = deferred()
  const release = deferred()
  const observations: NativeResumeEvidence[] = []
  const pending = exerciseSessionResume(fixture.context, options(async (evidence) => {
    observations.push(evidence)
    if (evidence.phase === phase) {
      resume.events.push(`evidence:${phase}`)
      entered.resolve()
      await release.promise
    }
  }))
  const settled = pending.then(() => 'finished' as const, error => ({ error }))
  try {
    const first = await Promise.race([entered.promise.then(() => 'entered' as const), settled])
    expect(first).toBe('entered')
    return { fixture, observations, pending, release }
  }
  catch (error) {
    release.resolve()
    await settled
    throw error
  }
}

describe('exerciseSessionResume', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    resume.events.length = 0
    resume.userRows.length = 0
    resume.assistantRows.length = 0
  })

  it('keeps the existing operation sequence when evidence is absent', async () => {
    const { context, resumedRequest } = scenario()
    expect((await exerciseSessionResume(context, options())).request).toBe(resumedRequest)
    expect(resume.events).toEqual([
      'prepare',
      'answer:original',
      'agent:original',
      'assert:original-session',
      'rows:original-answer',
      'keeper:directory',
      'keeper:create',
      'original:close',
      'click:keeper-worker-agent',
      'agent:keeper',
      'picker:reopen',
      'verdict:reopened',
      'count:user',
      'count:assistant',
      'answer:resumed',
      'agent:resumed',
      'assert:resumed-session',
      'conversation:resumed',
    ])
    expect(resume.send).toHaveBeenCalledTimes(2)
    expect(resume.current).toHaveBeenCalledTimes(3)
    expect(resume.open).toHaveBeenCalledTimes(1)
    expect(resume.close).toHaveBeenCalledTimes(1)
    expect(resume.countRows).toHaveBeenCalledTimes(1)
    expect(resume.picker).toHaveBeenCalledTimes(1)
    expect(resume.reopen).toHaveBeenCalledTimes(1)
    expect(resume.conversation).toHaveBeenCalledTimes(1)
  })

  it('returns one set of marked texts with the actual resumed request', async () => {
    const { context, resumedRequest } = scenario()
    const result = await exerciseSessionResume(context, options())
    expect(result.marker).toMatch(/^[a-f0-9]{32}$/)
    expect(result.request).toBe(resumedRequest)
    expect(result.originalPrompt).toContain(`RESUMEPROMPT${result.marker}`)
    expect(result.originalAnswer).toBe(`RESUMEANSWER${result.marker}`)
    expect(result.resumedPrompt).toContain(`RESUMEDPROMPT${result.marker}`)
    expect(result.resumedAnswer).toBe(`RESUMEDNEWANSWER${result.marker}`)
    expect(resume.send.mock.calls.map(([, prompt, answer]) => [prompt, answer])).toEqual([
      [result.originalPrompt, result.originalAnswer],
      [result.resumedPrompt, result.resumedAnswer],
    ])
    const texts: NativeResumeTexts = { marker: result.marker, originalPrompt: result.originalPrompt, originalAnswer: result.originalAnswer, resumedPrompt: result.resumedPrompt, resumedAnswer: result.resumedAnswer }
    expect(resume.countRows.mock.calls[0]?.[2]).toEqual(texts)
    expect(resume.conversation.mock.calls[0]?.[2]).toEqual(texts)
  })

  it('fails with the Worker startup error and sends no resumed input', async () => {
    const { context } = scenario()
    const observations: ResumePhase[] = []
    const failure = new Error('The Worker failed to start the resumed native session: -32603 No previous sessions found')
    resume.reopen.mockReset().mockImplementation(async () => {
      resume.events.push('verdict:failed')
      throw failure
    })
    await expect(exerciseSessionResume(context, options(async (evidence) => {
      observations.push(evidence.phase)
    }))).rejects.toBe(failure)
    expect(observations).toEqual(['stored', 'opened'])
    expect(resume.events.at(-1)).toBe('verdict:failed')
    expect(resume.events).not.toContain('count:user')
    expect(resume.events).not.toContain('count:assistant')
    expect(resume.send).toHaveBeenCalledTimes(1)
    expect(resume.current).toHaveBeenCalledTimes(2)
    expect(resume.conversation).not.toHaveBeenCalled()
  })

  it('requires the Worker verdict after opened evidence and before the copied-row checks', async () => {
    const { context } = scenario()
    await exerciseSessionResume(context, options(async (evidence) => {
      resume.events.push(`evidence:${evidence.phase}`)
    }))
    expect(resume.events.indexOf('verdict:reopened')).toBeGreaterThan(resume.events.indexOf('evidence:opened'))
    expect(resume.events.indexOf('verdict:reopened')).toBeLessThan(resume.events.indexOf('count:user'))
    expect(resume.events.indexOf('verdict:reopened')).toBeLessThan(resume.events.indexOf('answer:resumed'))
  })

  it('proves the continued conversation after continued evidence', async () => {
    const { context } = scenario()
    await exerciseSessionResume(context, options(async (evidence) => {
      resume.events.push(`evidence:${evidence.phase}`)
    }))
    expect(resume.events.indexOf('conversation:resumed')).toBeGreaterThan(resume.events.indexOf('evidence:continued'))
    expect(resume.events.at(-1)).toBe('conversation:resumed')
  })

  it('counts the original answer rows before the keeper exists', async () => {
    const { context } = scenario()
    const failure = new Error('The original agent stored no Worker row that holds the original answer.')
    resume.countRows.mockReset().mockRejectedValue(failure)
    await expect(exerciseSessionResume(context, options())).rejects.toBe(failure)
    expect(resume.open).not.toHaveBeenCalled()
    expect(resume.close).not.toHaveBeenCalled()
    expect(resume.send).toHaveBeenCalledTimes(1)
  })

  it('refuses a continued turn in an agent other than the Worker verdict', async () => {
    const { context, reopened } = scenario()
    resume.reopen.mockReset().mockResolvedValue(create(AgentInfoSchema, { ...reopened, id: 'other-worker-agent' }))
    await expect(exerciseSessionResume(context, options())).rejects.toMatchObject({ actual: reopened.id, expected: 'other-worker-agent' })
    expect(resume.conversation).not.toHaveBeenCalled()
  })

  it('propagates the conversation proof failure after the continued turn', async () => {
    const { context } = scenario()
    const observations: ResumePhase[] = []
    const failure = new Error('The resumed answer bubble must not hold the original answer.')
    resume.conversation.mockReset().mockRejectedValue(failure)
    await expect(exerciseSessionResume(context, options(async (evidence) => {
      observations.push(evidence.phase)
    }))).rejects.toBe(failure)
    expect(observations).toEqual(['stored', 'opened', 'continued'])
    expect(resume.send).toHaveBeenCalledTimes(2)
  })

  it('captures the stored identity after the original answer and before keeper creation', async () => {
    const { context, prior } = scenario()
    const observations: NativeResumeEvidence[] = []
    await exerciseSessionResume(context, options(async (evidence) => {
      observations.push(evidence)
      resume.events.push(`evidence:${evidence.phase}`)
    }))
    expect(observations.map(evidence => evidence.phase)).toEqual(['stored', 'opened', 'continued'])
    expect(observations[0]).toEqual({ phase: 'stored', prior })
    expect(resume.events.indexOf('evidence:stored')).toBeGreaterThan(resume.events.indexOf('assert:original-session'))
    expect(resume.events.indexOf('evidence:stored')).toBeLessThan(resume.events.indexOf('keeper:create'))
  })

  it('captures opened evidence before either stored-row assertion', async () => {
    const { context, prior } = scenario()
    let opened: NativeResumeEvidence | undefined
    await exerciseSessionResume(context, options(async (evidence) => {
      if (evidence.phase === 'opened') {
        opened = evidence
        resume.events.push('evidence:opened')
      }
    }))
    expect(opened).toEqual({ phase: 'opened', prior })
    expect(resume.events.indexOf('evidence:opened')).toBeGreaterThan(resume.events.indexOf('picker:reopen'))
    expect(resume.events.indexOf('evidence:opened')).toBeLessThan(resume.events.indexOf('count:user'))
    expect(resume.events.indexOf('evidence:opened')).toBeLessThan(resume.events.indexOf('count:assistant'))
  })

  it('awaits stored evidence before closing the original agent', async () => {
    const held = await holdEvidence('stored')
    try {
      expect(resume.open).not.toHaveBeenCalled()
      expect(resume.close).not.toHaveBeenCalled()
      expect(resume.send).toHaveBeenCalledTimes(1)
    }
    finally {
      held.release.resolve()
      await held.pending
    }
  })

  it('awaits opened evidence before assertions and the next model turn', async () => {
    const held = await holdEvidence('opened')
    try {
      expect(resume.events).not.toContain('count:user')
      expect(resume.events).not.toContain('count:assistant')
      expect(resume.send).toHaveBeenCalledTimes(1)
    }
    finally {
      held.release.resolve()
      await held.pending
    }
  })

  it('captures continued evidence after the unchanged session identity assertion', async () => {
    const { context, prior, resumedRequest } = scenario()
    let continued: NativeResumeEvidence | undefined
    await exerciseSessionResume(context, options(async (evidence) => {
      if (evidence.phase === 'continued') {
        continued = evidence
        resume.events.push('evidence:continued')
      }
    }))
    expect(continued).toEqual({ phase: 'continued', prior, request: resumedRequest })
    expect(resume.events.indexOf('evidence:continued')).toBeGreaterThan(resume.events.indexOf('assert:resumed-session'))
  })

  it('returns the same actual resumed request after continued evidence completes', async () => {
    const held = await holdEvidence('continued')
    try {
      expect(resume.events).toContain('assert:resumed-session')
      const evidence = held.observations.find(item => item.phase === 'continued')
      expect(evidence?.phase).toBe('continued')
      if (evidence?.phase !== 'continued')
        throw new Error('The resume callback did not receive the continued request.')
      expect(evidence.request).toBe(held.fixture.resumedRequest)
      held.release.resolve()
      expect((await held.pending).request).toBe(held.fixture.resumedRequest)
    }
    finally {
      held.release.resolve()
      await held.pending
    }
  })

  it.each(['stored', 'opened', 'continued'] as const)('propagates the %s evidence failure without starting later operations', async (phase) => {
    const { context } = scenario()
    const failure = new Error('The controlled evidence read failed.')
    const observations: ResumePhase[] = []
    const pending = exerciseSessionResume(context, options(async (evidence) => {
      observations.push(evidence.phase)
      if (evidence.phase === phase)
        throw failure
    }))
    await expect(pending).rejects.toBe(failure)
    expect(observations.at(-1)).toBe(phase)
    if (phase === 'stored') {
      expect(resume.open).not.toHaveBeenCalled()
      expect(resume.close).not.toHaveBeenCalled()
      expect(resume.countRows).not.toHaveBeenCalled()
    }
    if (phase !== 'continued') {
      expect(resume.send).toHaveBeenCalledTimes(1)
      expect(resume.reopen).not.toHaveBeenCalled()
    }
    expect(resume.conversation).not.toHaveBeenCalled()
  })

  it.each(['user', 'assistant'] as const)('keeps the duplicate %s row failure after opened evidence', async (kind) => {
    const { context } = scenario()
    const observations: ResumePhase[] = []
    resume.close.mockImplementation(async () => {
      resume.events.push('original:close')
      const values = kind === 'user' ? resume.userRows : resume.assistantRows
      const original = values[0]
      if (original === undefined)
        throw new Error('The controlled original row is absent.')
      values.push(original)
      return { failureMessage: '' }
    })
    const result = await exerciseSessionResume(context, options(async (evidence) => {
      observations.push(evidence.phase)
    })).then(() => undefined, error => error)
    expect(result).toMatchObject({ actual: 2, expected: 1 })
    expect(observations).toEqual(['stored', 'opened'])
    expect(resume.send).toHaveBeenCalledTimes(1)
    expect(resume.reopen).toHaveBeenCalledTimes(1)
  })

  it('keeps the native continuation failure without a continued event', async () => {
    const { context } = scenario()
    const observations: ResumePhase[] = []
    const failure = new Error('The actual native continuation failed.')
    resume.send.mockReset().mockImplementationOnce(async (_context, prompt, answer) => {
      resume.userRows.push(prompt)
      resume.assistantRows.push(answer)
      return { protocol: 'openai-responses', path: '/v1/responses', body: { prompt } }
    }).mockRejectedValueOnce(failure)
    await expect(exerciseSessionResume(context, options(async (evidence) => {
      observations.push(evidence.phase)
    }))).rejects.toBe(failure)
    expect(observations).toEqual(['stored', 'opened'])
    expect(resume.current).toHaveBeenCalledTimes(2)
    expect(resume.conversation).not.toHaveBeenCalled()
  })
})

describe('heldToolScript', () => {
  const SCRATCH_ROOT = resolve(process.cwd(), '../.tmp')
  /** A deep working directory, longer than the ones the E2E run creates. */
  const deepWorkingDir = `/${'deep-directory-segment/'.repeat(6)}native-e2e-wd-AbCdEf`
  const marker = '0123456789abcdef0123456789abcdef'

  it('keeps each whitespace-separated word that holds a slash within 255 bytes', () => {
    const script = heldToolScript({
      workingDir: deepWorkingDir,
      startedFile: join(deepWorkingDir, `interrupt-started-${marker}`),
      releaseFile: join(deepWorkingDir, `interrupt-release-${marker}`),
    })
    const words = script.split(/\s+/).filter(word => word.includes('/'))
    expect(words.length).toBeGreaterThan(0)
    for (const word of words)
      expect(Buffer.byteLength(word), word).toBeLessThanOrEqual(255)
  })

  it('writes its start signal, holds, and exits with 0 after the release file appears', async () => {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const workingDir = mkdtempSync(join(SCRATCH_ROOT, 'held-tool-script-'))
    const startedFile = join(workingDir, `interrupt-started-${marker}`)
    const releaseFile = join(workingDir, `interrupt-release-${marker}`)
    const child = spawn(process.execPath, ['-e', heldToolScript({ workingDir, startedFile, releaseFile })], { stdio: 'ignore' })
    try {
      const exited = new Promise<number | null>(resolveExit => child.once('exit', code => resolveExit(code)))
      await expect.poll(() => existsSync(startedFile)).toBe(true)
      expect(child.exitCode).toBeNull()
      writeFileSync(releaseFile, '')
      await expect(exited).resolves.toBe(0)
    }
    finally {
      await stopProcess(child)
      rmSync(workingDir, { recursive: true, force: true })
    }
  })
})

describe('exerciseInterruptTurn', () => {
  // Each refused call below carries `@ts-expect-error`, so the type check fails if the option type accepts it again.
  // The run-time check stays for a caller that builds its options as a wider type.
  it('refuses a held model turn end for an interrupted tool', async () => {
    // The check runs before the helper touches the context.
    const context = {} as ManagedNativeScenarioContext
    // @ts-expect-error A tool turn holds the tool, so it takes no held model turn end.
    await expect(exerciseInterruptTurn(context, { kind: 'tool', heldModelTurnEnd: 'after-answer' }))
      .rejects
      .toThrow('A held model turn end applies to an interrupted model request, not to an interrupted tool.')
    // @ts-expect-error A tool turn holds the tool, so it takes no held model turn end.
    await expect(exerciseInterruptTurn(context, { kind: 'tool', heldModelTurnEnd: 'while-held' }))
      .rejects
      .toThrow('A held model turn end applies to an interrupted model request, not to an interrupted tool.')
  })

  it.each(['before-response', 'after-first-chunk'] as const)('refuses the held model turn position %s for an interrupted tool', async (holdModelTurn) => {
    // The check runs before the helper touches the context. Without it, the tool branch ignores the position.
    const context = {} as ManagedNativeScenarioContext
    // @ts-expect-error A tool turn holds the tool, so it takes no held model turn position.
    await expect(exerciseInterruptTurn(context, { kind: 'tool', holdModelTurn }))
      .rejects
      .toThrow('A held model turn position applies to an interrupted model request, not to an interrupted tool.')
  })

  it('types the model options for a model turn only', () => {
    // The type checker reads these checks. They do nothing at run time.
    expectTypeOf<{ kind: 'model', holdModelTurn: 'after-first-chunk', heldModelTurnEnd: 'after-answer' }>().toExtend<InterruptTurnOptions>()
    expectTypeOf<{ holdModelTurn: 'before-response' }>().toExtend<InterruptTurnOptions>()
    expectTypeOf<{ kind: 'tool', prompt: string, divider: RegExp }>().toExtend<InterruptTurnOptions>()
    expectTypeOf<{ kind: 'tool', holdModelTurn: 'after-first-chunk' }>().not.toExtend<InterruptTurnOptions>()
    expectTypeOf<{ kind: 'tool', heldModelTurnEnd: 'while-held' }>().not.toExtend<InterruptTurnOptions>()
  })

  it('types a kind that holds either value, as a spec that loops over both kinds passes it', () => {
    expectTypeOf<{ kind: 'model' | 'tool' }>().toExtend<InterruptTurnOptions>()
  })
})
