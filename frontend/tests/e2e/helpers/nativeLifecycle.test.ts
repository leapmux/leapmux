import type { Locator, Page } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeResumeEvidence } from './nativeLifecycle'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { createMockModelServer } from './mockModelServer'
import { startModelScript } from './modelScriptFixture'
import { exerciseSessionResume, releaseNativeTurnGate } from './nativeLifecycle'

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
  dialog: vi.fn<() => Promise<void>>(),
  worker: vi.fn<() => Promise<void>>(),
  workingDir: vi.fn<(page: Page, path: string) => Promise<void>>(),
  menu: vi.fn<() => Promise<void>>(),
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
vi.mock('./runDirectory', async importOriginal => ({
  ...await importOriginal<typeof import('./runDirectory')>(),
  createTestDirectory: resume.directory,
}))
vi.mock('./worktree', async importOriginal => ({
  ...await importOriginal<typeof import('./worktree')>(),
  closeAgentViaAPI: resume.close,
  openNewAgentDialog: resume.dialog,
  waitForWorker: resume.worker,
  setWorkingDir: resume.workingDir,
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
    }),
  }
}

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  assistantBubbles: () => rows('assistant'),
  userBubbles: () => rows('user'),
  tabById: (_page: Page, id: string) => control(id),
  openMenu: resume.menu,
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
  resume.dialog.mockImplementation(async () => {
    resume.events.push('dialog:open')
  })
  resume.worker.mockImplementation(async () => {
    resume.events.push('worker:ready')
  })
  resume.workingDir.mockImplementation(async (_page, path) => {
    expect(path).toBe(prior.workingDir)
    resume.events.push('working-directory:set')
  })
  resume.menu.mockImplementation(async () => {
    resume.events.push('menu:open')
  })
  return { context, prior, resumedRequest, reopened }
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
    const { context, prior, resumedRequest } = scenario()
    expect(await exerciseSessionResume(context, options())).toBe(resumedRequest)
    expect(resume.events).toEqual([
      'prepare',
      'answer:original',
      'agent:original',
      'assert:original-session',
      'keeper:directory',
      'keeper:create',
      'original:close',
      'click:keeper-worker-agent',
      'agent:keeper',
      'dialog:open',
      'worker:ready',
      'click:agent-provider-selector-trigger',
      `click:agent-provider-option-${context.provider}`,
      'working-directory:set',
      'menu:open',
      `visible:loading-menu-option-${prior.agentSessionId}`,
      `click:loading-menu-option-${prior.agentSessionId}`,
      'click:Create',
      'count:user',
      'count:assistant',
      'answer:resumed',
      'agent:resumed',
      'assert:resumed-session',
    ])
    expect(resume.send).toHaveBeenCalledTimes(2)
    expect(resume.current).toHaveBeenCalledTimes(3)
    expect(resume.open).toHaveBeenCalledTimes(1)
    expect(resume.close).toHaveBeenCalledTimes(1)
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
    expect(resume.events.indexOf('evidence:opened')).toBeGreaterThan(resume.events.indexOf('click:Create'))
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
      expect(await held.pending).toBe(held.fixture.resumedRequest)
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
    }
    if (phase !== 'continued')
      expect(resume.send).toHaveBeenCalledTimes(1)
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
  })
})

describe('releaseNativeTurnGate', () => {
  it('does not turn a successfully cancelled native response into a cleanup failure', async () => {
    const server = await createMockModelServer({ models: ['native-cleanup-unit'] })
    const lifecycle = await startModelScript(server.url)
    const script = lifecycle.script
    const controller = new AbortController()
    try {
      await script.queue({ text: 'The interrupted answer must not complete.', gate: 'native-cancelled-response' })
      const pending = fetch(`${server.url}/v1/messages`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', 'x-api-key': 'leapmux-e2e-model-key' },
        body: JSON.stringify({ model: 'native-cleanup-unit', max_tokens: 100, stream: true, messages: [{ role: 'user', content: script.prompt('Hold the native cleanup test response.') }] }),
        signal: controller.signal,
      }).then(() => null, error => error)
      await script.waitForGate('native-cancelled-response')
      controller.abort()
      await pending
      await expect.poll(async () => (await script.status()).pendingGates).toEqual([])
      await expect(releaseNativeTurnGate(script, 'native-cancelled-response')).resolves.toBeUndefined()
    }
    finally {
      controller.abort()
      await lifecycle.finish(false)
      await server.close()
    }
  })
})
