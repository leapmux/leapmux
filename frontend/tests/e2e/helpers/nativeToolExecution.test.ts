import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelScenarioStatus, MockModelStep } from './mockModelScript'
import type { MockModelServer } from './mockModelServer'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import type { NativeToolStep } from './nativeToolExecution'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeLocator } from '~/test-support/fakeLocator'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { stepRequest } from './mockModelScript'
import { createMockModelServer } from './mockModelServer'
import { startModelScript } from './modelScriptFixture'
import { createNativeToolDirectory } from './nativeToolDirectory'
import { approveNativeToolsUntil, clickNativeToolApproval, exerciseFileEditSequence, exerciseShellToolExecution, expectFileDiff, NATIVE_APPROVAL_LIMIT, nativeFileEditSequence, nativeFileReadResult, nativeFileWriteSequence, nativeToolResultAt, PARITY_AFTER, PARITY_BEFORE, processNativeToolApproval, runNativeToolSteps, runNativeToolTurn, waitForNativeToolSteps } from './nativeToolExecution'
import { quotePosixShellArgument } from './shellArguments'

const calls = vi.hoisted(() => ({ shell: vi.fn(), read: vi.fn(), edit: vi.fn(), write: vi.fn(), idle: vi.fn(), send: vi.fn(), agent: vi.fn(), noBanner: vi.fn() }))
vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  waitForAgentIdle: calls.idle,
  sendMessage: calls.send,
  expectNoControlBanner: calls.noBanner,
}))
vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: calls.agent,
}))
vi.mock('./providerToolCalls', () => ({
  bashToolCall: calls.shell,
  readToolCall: calls.read,
  editToolCall: calls.edit,
  writeToolCall: calls.write,
}))

const scratchRoot = resolve(process.cwd(), '../.tmp')
let directory: string
beforeEach(() => {
  vi.resetAllMocks()
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'native-file-sequence-unit-'))
  for (const [call, name] of [[calls.shell, 'unit-shell'], [calls.read, 'unit-read'], [calls.edit, 'unit-edit'], [calls.write, 'unit-write']] as const)
    call.mockImplementation((_provider, id) => ({ id, name }))
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

/** Keep a narrow browser fixture honest when the helper accesses a new method. */
function guardedBrowserHandle<T extends object>(methods: Partial<T>): T {
  return new Proxy(methods as T, {
    get: (target, property, receiver) => {
      if (property in target)
        return Reflect.get(target, property, receiver)
      if (typeof property === 'symbol')
        return undefined
      throw new Error(`The native tool wait fixture lacks ${String(property)}.`)
    },
  })
}

/** A page whose visible Allow buttons are `allow`. Any other button or scope fails the test. */
function allowButtonPage(allow: Locator): Page {
  return guardedBrowserHandle<Page>({ getByTestId: (testId: string | RegExp) => {
    expect(testId).toBe('control-allow-btn')
    return guardedBrowserHandle<Locator>({ filter: (options?: Parameters<Locator['filter']>[0]) => {
      expect(options).toEqual({ visible: true })
      return allow
    } })
  } })
}

describe('waitForNativeToolSteps', () => {
  function fixture() {
    const events: string[] = []
    const status: MockModelScenarioStatus = { complete: true, nextStep: 2, stepCount: 2, requests: [], unexpectedRequests: [], ruleMatches: {}, pendingGates: [] }
    const first = guardedBrowserHandle<Locator>({})
    const locator = guardedBrowserHandle<Locator>({ first: () => first })
    const page = allowButtonPage(locator)
    const context: NativeScenarioContext = {
      page,
      provider: AgentProvider.QODER,
      modelScript: {
        id: 'completed-tool-wait',
        testDeadline: () => undefined,
        prompt: text => text,
        queue: async () => 0,
        requestAt: async () => {
          throw new Error('The native tool wait reads no request.')
        },
        rule: async () => {},
        fallback: async () => {},
        status: async () => status,
        waitForSteps: async () => {
          events.push('model-completed')
          return status
        },
        waitForGate: async () => status,
        releaseGate: async () => {},
        releaseGateIfHeld: async () => false,
        allowUnconsumed: () => {},
      },
    }
    calls.idle.mockImplementation(async () => {
      events.push('idle')
    })
    // This signature lets the behavioral regression compile before the handler exists.
    const observeWait: (context: NativeScenarioContext, target: number, options: { beforeIdle: () => Promise<void> }) => Promise<void> = waitForNativeToolSteps
    return { context, events, observeWait }
  }

  it('captures native evidence after the exact model receipt and before the idle wait', async () => {
    const current = fixture()
    await current.observeWait(current.context, 2, { beforeIdle: async () => {
      current.events.push('capture')
    } })
    expect(current.events).toEqual(['model-completed', 'capture', 'idle'])
  })

  it('preserves a capture failure and does not start the idle wait', async () => {
    const current = fixture()
    const cause = new Error('The native Worker evidence read failed.')
    await expect(current.observeWait(current.context, 2, { beforeIdle: async () => {
      throw cause
    } })).rejects.toBe(cause)
    expect(calls.idle).not.toHaveBeenCalled()
  })

  it('preserves an idle failure after the native evidence handler completes', async () => {
    const current = fixture()
    const cause = new Error('The native turn remains active.')
    calls.idle.mockImplementation(async () => {
      current.events.push('idle')
      throw cause
    })
    await expect(current.observeWait(current.context, 2, { beforeIdle: async () => {
      current.events.push('capture')
    } })).rejects.toBe(cause)
    expect(current.events).toEqual(['model-completed', 'capture', 'idle'])
  })
})

describe('runNativeToolTurn', () => {
  const servers: MockModelServer[] = []
  afterEach(async () => {
    await Promise.all(servers.splice(0).map(server => server.close()))
  })

  /** A real model script, and a page whose approval control is never present. */
  async function turnContext(options: Pick<NativeScenarioContext, 'textStep'> = {}) {
    const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
    servers.push(server)
    const lifecycle = await startModelScript(server.url)
    const allow = guardedBrowserHandle<Locator>({ evaluateAll: (async () => false) as unknown as Locator['evaluateAll'] })
    const page = allowButtonPage(guardedBrowserHandle<Locator>({ first: () => allow }))
    const context: NativeScenarioContext = { page, provider: AgentProvider.CODEX, modelScript: lifecycle.script, ...options }
    /** Answer one native turn: each request repeats the marked prompt, as a native client does. */
    const nativeTurn = async (text: string, requests = 2) => {
      const replies: unknown[] = []
      for (let index = 0; index < requests; index++) {
        const response = await fetch(`${server.url}/v1/chat/completions`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ stream: false, messages: [{ role: 'user', content: text }] }),
        })
        expect(response.status).toBe(200)
        replies.push(await response.json())
      }
      return replies
    }
    return { context, lifecycle, nativeTurn }
  }

  it('queues the tool step and the answer after earlier steps, and returns both requests of the turn', async () => {
    const { context, lifecycle, nativeTurn } = await turnContext()
    await lifecycle.script.queue({ text: 'An earlier turn.' })
    await nativeTurn(lifecycle.script.prompt('The earlier prompt.'), 1)
    const replies: unknown[] = []
    calls.send.mockImplementation(async (_page: Page, text: string) => {
      replies.push(...await nativeTurn(text))
    })
    const turn = await runNativeToolTurn(context, {
      toolCalls: [{ id: 'unit-call', name: 'Bash', arguments: { command: 'printf unit' } }],
      prompt: 'Run the unit tool.',
      answer: 'The unit tool ended.',
    })
    expect(turn.start).toBe(1)
    expect(turn.toolRequest.stepIndex).toBe(1)
    expect(turn.resultRequest.stepIndex).toBe(2)
    expect(JSON.stringify(turn.toolRequest.body)).toContain('Run the unit tool.')
    expect(JSON.stringify(replies[0])).toContain('unit-call')
    expect(JSON.stringify(replies[1])).toContain('The unit tool ended.')
    expect(calls.send).toHaveBeenCalledExactlyOnceWith(context.page, lifecycle.script.prompt('Run the unit tool.'))
    expect(calls.idle).toHaveBeenCalledOnce()
    await lifecycle.finish(true)
  })

  it('answers through the provider text step and captures from the tool request', async () => {
    const { context, lifecycle, nativeTurn } = await turnContext({ textStep: text => ({ toolCalls: [{ id: 'unit-answer', name: 'answer', arguments: { text } }] }) })
    const replies: unknown[] = []
    calls.send.mockImplementation(async (_page: Page, text: string) => {
      replies.push(...await nativeTurn(text))
    })
    const send = vi.fn(async (page: Page, text: string) => calls.send(page, text))
    await runNativeToolTurn(context, {
      toolCalls: [{ id: 'unit-read', name: 'Read', arguments: { file_path: '{{target}}' } }],
      captures: { target: 'Read (\\S+) now' },
      prompt: 'Read /unit/target.txt now.',
      answer: 'The provider answer.',
      send,
    })
    expect(send).toHaveBeenCalledExactlyOnceWith(context.page, lifecycle.script.prompt('Read /unit/target.txt now.'))
    expect(JSON.stringify(replies[0])).toContain('/unit/target.txt')
    expect(JSON.stringify(replies[1])).toContain('unit-answer')
    expect(JSON.stringify(replies[1])).toContain('The provider answer.')
    await lifecycle.finish(true)
  })

  it('fails with the step of a request that the script does not hold', async () => {
    const status: MockModelScenarioStatus = { complete: true, nextStep: 2, stepCount: 2, ruleMatches: {}, pendingGates: [], unexpectedRequests: [], requests: [{ protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex: 0, body: {} }] }
    const page = allowButtonPage(guardedBrowserHandle<Locator>({ first: () => guardedBrowserHandle<Locator>({}) }))
    const context: NativeScenarioContext = {
      page,
      provider: AgentProvider.CODEX,
      modelScript: {
        id: 'dropped-result-request',
        testDeadline: () => undefined,
        prompt: text => text,
        queue: async () => 0,
        requestAt: async stepIndex => stepRequest(status, stepIndex),
        rule: async () => {},
        fallback: async () => {},
        status: async () => status,
        waitForSteps: async () => status,
        waitForGate: async () => status,
        releaseGate: async () => {},
        releaseGateIfHeld: async () => false,
        allowUnconsumed: () => {},
      },
    }
    await expect(runNativeToolTurn(context, { toolCalls: [{ id: 'unit-call', name: 'Bash' }], prompt: 'Run.', answer: 'Done.' }))
      .rejects
      .toThrow('The model script holds no request for step 1')
  })

  it('refuses a turn without a tool call before it queues a step', async () => {
    const queue = vi.fn(async () => 0)
    const context = { page: guardedBrowserHandle<Page>({}), provider: AgentProvider.CODEX, modelScript: guardedBrowserHandle<NativeScenarioContext['modelScript']>({ queue }) }
    await expect(runNativeToolTurn(context, { toolCalls: [], prompt: 'Run.', answer: 'Done.' })).rejects.toThrow('at least one tool call')
    expect(queue).not.toHaveBeenCalled()
  })

  it('clicks nothing and requires no banner after the turn when the turn expects no permission request', async () => {
    const { context: allowContext, lifecycle, nativeTurn } = await turnContext()
    // Each page access fails, so the turn can read no Allow button.
    const context: NativeScenarioContext = { ...allowContext, page: guardedBrowserHandle<Page>({}) }
    calls.send.mockImplementation(async (_page: Page, text: string) => {
      await nativeTurn(text)
    })
    const turn = await runNativeToolTurn(context, {
      toolCalls: [{ id: 'unit-call', name: 'Bash', arguments: { command: 'printf unit' } }],
      prompt: 'Run the unit tool.',
      answer: 'The unit tool ended.',
      permissions: 'none',
    })
    expect(turn.resultRequest.stepIndex).toBe(1)
    expect(calls.noBanner).toHaveBeenCalledExactlyOnceWith(context.page)
    await lifecycle.finish(true)
  })
})

describe('runNativeToolSteps', () => {
  const servers: MockModelServer[] = []
  afterEach(async () => {
    await Promise.all(servers.splice(0).map(server => server.close()))
  })

  const threeSteps: NativeToolStep[] = [
    { toolCalls: [{ id: 'unit-shell', name: 'Bash', arguments: { command: 'printf unit' } }] },
    { toolCalls: [{ id: 'unit-read', name: 'Read', arguments: { file_path: '{{target}}' } }], captures: { target: 'on (\\S+) now' } },
    { toolCalls: [{ id: 'unit-edit-a', name: 'Edit', arguments: { file_path: 'a' } }, { id: 'unit-edit-b', name: 'Edit', arguments: { file_path: 'b' } }] },
  ]

  it('queues each tool step in order after earlier steps, answers last, and returns the index of the first tool step', async () => {
    const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
    servers.push(server)
    const lifecycle = await startModelScript(server.url)
    const allow = guardedBrowserHandle<Locator>({ evaluateAll: (async () => false) as unknown as Locator['evaluateAll'] })
    const context: NativeScenarioContext = { page: allowButtonPage(guardedBrowserHandle<Locator>({ first: () => allow })), provider: AgentProvider.CODEX, modelScript: lifecycle.script }
    /** Send `count` requests, each with the marked prompt, as a native client does. */
    const nativeRequests = async (text: string, count: number) => {
      const replies: string[] = []
      for (let index = 0; index < count; index++) {
        const response = await fetch(`${server.url}/v1/chat/completions`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ stream: false, messages: [{ role: 'user', content: text }] }),
        })
        expect(response.status).toBe(200)
        replies.push(JSON.stringify(await response.json()))
      }
      return replies
    }
    await lifecycle.script.queue({ text: 'An earlier turn.' })
    await nativeRequests(lifecycle.script.prompt('The earlier prompt.'), 1)
    const replies: string[] = []
    calls.send.mockImplementation(async (_page: Page, text: string) => {
      replies.push(...await nativeRequests(text, 4))
    })
    const start = await runNativeToolSteps(context, { steps: threeSteps, prompt: 'Run the unit steps on /unit/target.txt now.', answer: 'The unit steps ended.' })
    expect(start).toBe(1)
    expect(replies).toHaveLength(4)
    expect(replies[0]).toContain('unit-shell')
    expect(replies[1]).toContain('unit-read')
    expect(replies[1]).toContain('/unit/target.txt')
    expect(replies[2]).toMatch(/unit-edit-a.*unit-edit-b/)
    expect(replies[3]).toContain('The unit steps ended.')
    expect(calls.send).toHaveBeenCalledExactlyOnceWith(context.page, lifecycle.script.prompt('Run the unit steps on /unit/target.txt now.'))
    expect(calls.idle).toHaveBeenCalledOnce()
    expect(calls.noBanner).not.toHaveBeenCalled()
    await lifecycle.finish(true)
  })

  /** A model script that records what the turn queues and waits for. Each page access fails, so no click can happen. */
  function recordingContext(options: Pick<NativeScenarioContext, 'textStep'> = {}) {
    const events: string[] = []
    const queued: MockModelStep[][] = []
    const modelScript = guardedBrowserHandle<NativeScenarioContext['modelScript']>({
      prompt: text => `marked ${text}`,
      queue: async (...steps) => {
        queued.push(steps)
        return 5
      },
      waitForSteps: async (count) => {
        if (count === undefined)
          throw new Error('The turn waits for a stated step count.')
        events.push(`steps ${count}`)
        return { complete: true, nextStep: count, stepCount: count, requests: [], unexpectedRequests: [], ruleMatches: {}, pendingGates: [] }
      },
    })
    calls.send.mockImplementation(async (_page: Page, text: string) => {
      events.push(`send ${text}`)
    })
    calls.idle.mockImplementation(async () => {
      events.push('idle')
    })
    calls.noBanner.mockImplementation(async () => {
      events.push('no banner')
    })
    const context: NativeScenarioContext = { page: guardedBrowserHandle<Page>({}), provider: AgentProvider.CODEX, modelScript, ...options }
    return { context, events, queued }
  }

  it('waits for every step with no click, then for the idle agent, then requires no banner', async () => {
    const { context, events, queued } = recordingContext({ textStep: text => ({ toolCalls: [{ id: 'unit-answer', name: 'answer', arguments: { text } }] }) })
    expect(await runNativeToolSteps(context, { steps: threeSteps, prompt: 'Run.', answer: 'Done.', permissions: 'none' })).toBe(5)
    expect(events).toEqual(['send marked Run.', 'steps 9', 'idle', 'no banner'])
    expect(calls.noBanner).toHaveBeenCalledExactlyOnceWith(context.page)
    expect(queued).toEqual([[...threeSteps, { toolCalls: [{ id: 'unit-answer', name: 'answer', arguments: { text: 'Done.' } }] }]])
  })

  it('queues copies, so a later change to the caller\'s steps cannot reach the script', async () => {
    const { context, queued } = recordingContext()
    await runNativeToolSteps(context, { steps: threeSteps, prompt: 'Run.', answer: 'Done.', permissions: 'none' })
    const [shell, read] = queued[0] ?? []
    expect(shell?.toolCalls).not.toBe(threeSteps[0]?.toolCalls)
    expect(read?.captures).not.toBe(threeSteps[1]?.captures)
    expect(shell).not.toHaveProperty('captures')
  })

  it('fails with the banner check when a banner stays after the turn', async () => {
    const { context } = recordingContext()
    const cause = new Error('the page holds no control request banner')
    calls.noBanner.mockRejectedValue(cause)
    await expect(runNativeToolSteps(context, { steps: threeSteps, prompt: 'Run.', answer: 'Done.', permissions: 'none' })).rejects.toBe(cause)
  })

  it('refuses a turn without a tool step before it queues a step', async () => {
    const { context, queued } = recordingContext()
    await expect(runNativeToolSteps(context, { steps: [], prompt: 'Run.', answer: 'Done.' })).rejects.toThrow('at least one tool step')
    expect(queued).toEqual([])
  })

  it('refuses a later tool step without a tool call before it queues a step', async () => {
    const { context, queued } = recordingContext()
    await expect(runNativeToolSteps(context, { steps: [threeSteps[0]!, { toolCalls: [] }], prompt: 'Run.', answer: 'Done.' })).rejects.toThrow('Each tool step of a native turn needs at least one tool call.')
    expect(queued).toEqual([])
  })
})

describe('approveNativeToolsUntil', () => {
  /** An Allow button whose evaluation records the limit flag that each attempt passes. */
  function allowButton(flags: boolean[], clickWhenAllowed: () => boolean) {
    return guardedBrowserHandle<Locator>({
      evaluateAll: (async (_check: unknown, allowed: boolean) => {
        flags.push(allowed)
        return allowed && clickWhenAllowed()
      }) as unknown as Locator['evaluateAll'],
    })
  }

  it('reads no button when the operation already completed', async () => {
    const flags: boolean[] = []
    await approveNativeToolsUntil(guardedBrowserHandle<Page>({}), async () => true, allowButton(flags, () => true))
    expect(flags).toEqual([])
  })

  it('allows each approval until the operation completes', async () => {
    const flags: boolean[] = []
    let clicks = 0
    await approveNativeToolsUntil(guardedBrowserHandle<Page>({}), async () => clicks >= 3, allowButton(flags, () => {
      clicks++
      return true
    }))
    expect(clicks).toBe(3)
    expect(flags).toEqual([true, true, true])
  })

  it(`passes the limit flag after ${NATIVE_APPROVAL_LIMIT} approvals, so a further ready button fails the click`, async () => {
    const flags: boolean[] = []
    await approveNativeToolsUntil(guardedBrowserHandle<Page>({}), async () => flags.length > NATIVE_APPROVAL_LIMIT, allowButton(flags, () => true))
    expect(flags).toEqual([...Array.from({ length: NATIVE_APPROVAL_LIMIT }).fill(true), false])
  })

  it('reads the first visible Allow button of the page by default', async () => {
    const flags: boolean[] = []
    let clicks = 0
    const first = allowButton(flags, () => {
      clicks++
      return true
    })
    const page = allowButtonPage(guardedBrowserHandle<Locator>({ first: () => first }))
    await approveNativeToolsUntil(page, async () => clicks >= 1)
    expect(flags).toEqual([true])
  })
})

/** A page whose diff locator records its filters, and whose first match reports `visible`. */
function diffPage(visible: boolean) {
  const log: string[] = []
  const first = fakeLocator((check) => {
    log.push(check.expression)
    return visible
  })
  const diff: Locator = guardedBrowserHandle<Locator>({
    locator: (selector: string) => {
      log.push(selector)
      return diff
    },
    filter: (options?: Parameters<Locator['filter']>[0]) => {
      log.push(`filter ${String(options?.hasText)}`)
      return diff
    },
    first: () => first,
  })
  const page = guardedBrowserHandle<Page>({ locator: (selector: string) => {
    log.push(selector)
    return diff
  } })
  return { page, log }
}

describe('expectFileDiff', () => {
  it('requires one visible diff that holds both lines', async () => {
    const { page, log } = diffPage(true)
    await expectFileDiff(page, { before: PARITY_BEFORE, after: PARITY_AFTER })
    expect(log).toEqual(['[data-testid="message-bubble"]:visible', '[data-file-diff]:visible', `filter ${PARITY_AFTER}`, `filter ${PARITY_BEFORE}`, 'to.be.visible'])
  })

  it('fails when no visible diff holds both lines', async () => {
    await expect(expectFileDiff(diffPage(false).page, { before: PARITY_BEFORE, after: PARITY_AFTER })).rejects.toThrow('one visible file diff shows the old and the new line')
  })

  it.each([
    { before: '', after: PARITY_AFTER },
    { before: PARITY_BEFORE, after: '' },
    { before: PARITY_BEFORE, after: PARITY_BEFORE },
    { before: 'const x', after: 'const x = 2' },
    { before: 'const x = 2', after: 'x = 2' },
  ])('refuses the lines $before and $after before it reads the page', async (change) => {
    await expect(expectFileDiff(guardedBrowserHandle<Page>({}), change)).rejects.toThrow('neither line may hold the other')
  })
})

describe('exerciseFileEditSequence', () => {
  /** A model script whose last wait leaves `content` in the edited file, as the native agent would. */
  function editingScript(path: string, content: string, waits: number[]) {
    return {
      prompt: (text: string) => text,
      queue: async (...steps: unknown[]) => {
        expect(steps).toHaveLength(4)
        return 7
      },
      waitForSteps: async (count: number) => {
        waits.push(count)
        writeFileSync(path, content)
      },
    }
  }

  it('returns the step index of the seed step after it checks the diff and the file', async () => {
    const waits: number[] = []
    const { page, log } = diffPage(true)
    const modelScript = editingScript(join(directory, 'parity.ts'), `${PARITY_AFTER}\n`, waits)
    const context = { page, modelScript, provider: AgentProvider.CLAUDE_CODE } as unknown as NativeScenarioContext
    expect(await exerciseFileEditSequence(context, { workingDir: directory, fileName: 'parity.ts' })).toBe(7)
    expect(waits).toEqual([11])
    expect(log.at(-1)).toBe('to.be.visible')
  })

  it('fails when the edit leaves the file without the new line', async () => {
    const modelScript = editingScript(join(directory, 'parity.ts'), `${PARITY_BEFORE}\n`, [])
    const context = { page: diffPage(true).page, modelScript, provider: AgentProvider.CLAUDE_CODE } as unknown as NativeScenarioContext
    await expect(exerciseFileEditSequence(context, { workingDir: directory, fileName: 'parity.ts' })).rejects.toThrow('the edit changed the file on disk')
  })

  it('fails when the edit keeps the seeded line on disk', async () => {
    const modelScript = editingScript(join(directory, 'parity.ts'), `${PARITY_BEFORE}\n${PARITY_AFTER}\n`, [])
    const context = { page: diffPage(true).page, modelScript, provider: AgentProvider.CLAUDE_CODE } as unknown as NativeScenarioContext
    await expect(exerciseFileEditSequence(context, { workingDir: directory, fileName: 'parity.ts' })).rejects.toThrow('the edit replaced the seeded line')
  })
})

describe('PARITY_BEFORE and PARITY_AFTER', () => {
  it('keeps each constant on one line, and neither holds the other, so a diff filter on one cannot match only the other', () => {
    for (const line of [PARITY_BEFORE, PARITY_AFTER])
      expect(line).not.toMatch(/[\r\n]/)
    expect(PARITY_BEFORE.includes(PARITY_AFTER)).toBe(false)
    expect(PARITY_AFTER.includes(PARITY_BEFORE)).toBe(false)
  })
})

describe('processNativeToolApproval', () => {
  it('uses the exact completed model receipt when the actual approval disappears before click', async () => {
    const button = document.createElement('button')
    document.body.append(button)
    const target = 2
    let nextStep = 1
    const clickIfReady = vi.fn(async () => {
      button.remove()
      nextStep = target
      return clickNativeToolApproval([])
    })
    try {
      const forbiddenVisibilityRead = vi.fn(async () => true)
      const forbiddenEnabledRead = vi.fn(async () => {
        throw new Error('The removed control cannot receive a separate enabled read.')
      })
      const control = { completed: async () => nextStep >= target, clickIfReady, isVisible: forbiddenVisibilityRead, isEnabled: forbiddenEnabledRead }
      await expect(processNativeToolApproval(control)).resolves.toBe('completed')
      expect(forbiddenVisibilityRead).not.toHaveBeenCalled()
      expect(forbiddenEnabledRead).not.toHaveBeenCalled()
      expect(nextStep).toBe(target)
      expect(button.isConnected).toBe(false)
      expect(clickIfReady).toHaveBeenCalledTimes(1)
    }
    finally {
      button.remove()
    }
  })
  it('keeps the exact model target pending when no native approval exists', async () => {
    const clickIfReady = vi.fn(async () => false)
    expect(await processNativeToolApproval({ completed: async () => false, clickIfReady })).toBe('waiting')
    expect(clickIfReady).toHaveBeenCalledTimes(1)
  })
  it('does not click after the exact native model target completes', async () => {
    const clickIfReady = vi.fn(async () => true)
    expect(await processNativeToolApproval({ completed: async () => true, clickIfReady })).toBe('completed')
    expect(clickIfReady).not.toHaveBeenCalled()
  })
  it('counts an actual approval while the exact model target remains pending', async () => {
    const clickIfReady = vi.fn(async () => true)
    expect(await processNativeToolApproval({ completed: async () => false, clickIfReady })).toBe('approval')
    expect(clickIfReady).toHaveBeenCalledTimes(1)
  })
  it('propagates an unrelated native approval operation failure', async () => {
    const cause = new Error('The browser execution context failed.')
    await expect(processNativeToolApproval({
      completed: async () => false,
      clickIfReady: async () => {
        throw cause
      },
    })).rejects.toBe(cause)
  })
})

describe('clickNativeToolApproval', () => {
  function visibleButton(): HTMLButtonElement {
    const button = document.createElement('button')
    document.body.append(button)
    const rectangle = new DOMRect(0, 0, 100, 20)
    const rectangles = Object.assign([rectangle], { item: (index: number) => index === 0 ? rectangle : null })
    vi.spyOn(button, 'getClientRects').mockReturnValue(rectangles)
    return button
  }
  it('clicks the same enabled button synchronously and counts only an actual click', () => {
    const button = visibleButton()
    let clicks = 0
    button.onclick = () => {
      clicks++
      button.disabled = true
    }
    try {
      expect(clickNativeToolApproval([button])).toBe(true)
      expect(clicks).toBe(1)
      expect(button.disabled).toBe(true)
      expect(clickNativeToolApproval([button])).toBe(false)
      expect(clicks).toBe(1)
    }
    finally {
      button.remove()
    }
  })
  it('does not repeat an approval while its actual response fieldset is disabled', () => {
    const fieldset = document.createElement('fieldset')
    const button = visibleButton()
    fieldset.append(button)
    document.body.append(fieldset)
    const click = vi.spyOn(button, 'click')
    button.onclick = () => {
      fieldset.disabled = true
    }
    try {
      expect(clickNativeToolApproval([button])).toBe(true)
      expect(button.disabled).toBe(false)
      expect(clickNativeToolApproval([button])).toBe(false)
      expect(click).toHaveBeenCalledTimes(1)
    }
    finally {
      fieldset.remove()
    }
  })
  it.each(['disabled', 'hidden', 'detached', 'display-none', 'visibility-hidden'])('does not click a %s native control', (state) => {
    const button = visibleButton()
    const click = vi.spyOn(button, 'click')
    if (state === 'disabled')
      button.disabled = true
    if (state === 'hidden')
      button.hidden = true
    if (state === 'detached')
      button.remove()
    if (state === 'display-none')
      button.style.display = 'none'
    if (state === 'visibility-hidden')
      button.style.visibility = 'hidden'
    try {
      expect(clickNativeToolApproval([button])).toBe(false)
      expect(click).not.toHaveBeenCalled()
    }
    finally {
      button.remove()
    }
  })
  it('refuses an enabled approval after the actual click limit, but permits an absent control', () => {
    const button = visibleButton()
    const click = vi.spyOn(button, 'click')
    try {
      expect(() => clickNativeToolApproval([button], false)).toThrow('approval limit')
      expect(click).not.toHaveBeenCalled()
      expect(clickNativeToolApproval([], false)).toBe(false)
    }
    finally {
      button.remove()
    }
  })
  it('refuses an ambiguous selected control', () => {
    const first = visibleButton()
    const second = visibleButton()
    try {
      expect(() => clickNativeToolApproval([first, second])).toThrow('one selected control')
    }
    finally {
      first.remove()
      second.remove()
    }
  })
  it('refuses an element that is not the actual native approval button', () => {
    const element = document.createElement('div')
    document.body.append(element)
    try {
      expect(() => clickNativeToolApproval([element])).toThrow('actual button')
    }
    finally {
      element.remove()
    }
  })
})

describe('nativeToolResultAt', () => {
  /** A script whose `requestAt` reads a fixed status through the real step lookup. */
  function modelScript(requests: MockModelRequestRecord[]) {
    const status: MockModelScenarioStatus = { complete: true, nextStep: 2, stepCount: 2, ruleMatches: {}, pendingGates: [], requests, unexpectedRequests: [] }
    return { requestAt: vi.fn(async (stepIndex: number) => stepRequest(status, stepIndex)) }
  }

  it('waits for the exact queued step and reads only its exact call result', async () => {
    const script = modelScript([
      { protocol: 'openai-chat-completions', path: '/chat/completions', stepIndex: 0, body: { messages: [{ role: 'tool', tool_call_id: 'selected', content: 'WRONG_EARLIER_RESULT' }] } },
      { protocol: 'openai-chat-completions', path: '/chat/completions', stepIndex: 1, body: { messages: [{ role: 'assistant', tool_calls: [{ id: 'selected', function: { arguments: 'ARGUMENT_ONLY_RESULT' } }] }, { role: 'tool', tool_call_id: 'other', content: 'WRONG_CALL_RESULT' }, { role: 'tool', tool_call_id: 'selected', content: 'ACTUAL_SELECTED_RESULT' }] } },
    ])
    expect(await nativeToolResultAt(script, 1, 'selected')).toBe('ACTUAL_SELECTED_RESULT')
    expect(script.requestAt).toHaveBeenCalledExactlyOnceWith(1)
  })

  it('retains queued step zero', async () => {
    const script = modelScript([{ protocol: 'openai-responses', path: '/responses', stepIndex: 0, body: { input: [{ type: 'function_call_output', call_id: 'zero', output: 'STEP_ZERO_RESULT' }] } }])
    expect(await nativeToolResultAt(script, 0, 'zero')).toBe('STEP_ZERO_RESULT')
    expect(script.requestAt).toHaveBeenCalledExactlyOnceWith(0)
  })

  it('rejects an absent queued request', async () => {
    await expect(nativeToolResultAt(modelScript([]), 1, 'selected')).rejects.toThrow('The model script holds no request for step 1')
  })

  it('rejects the wrong call ID even when its text resembles the requested result', async () => {
    const script = modelScript([{ protocol: 'anthropic-messages', path: '/v1/messages', stepIndex: 1, body: { messages: [{ role: 'user', content: [{ type: 'tool_result', tool_use_id: 'other', content: 'EXPECTED_LOOKING_RESULT' }] }] } }])
    await expect(nativeToolResultAt(script, 1, 'selected')).rejects.toThrow(/result|call/)
  })

  it('rejects duplicate results for the exact call', async () => {
    const script = modelScript([{ protocol: 'openai-chat-completions', path: '/chat/completions', stepIndex: 1, body: { messages: [{ role: 'tool', tool_call_id: 'selected', content: 'FIRST' }, { role: 'tool', tool_call_id: 'selected', content: 'SECOND' }] } }])
    await expect(nativeToolResultAt(script, 1, 'selected')).rejects.toThrow(/2 results|received 2/)
  })

  it('rejects an empty call ID before model access', async () => {
    const script = modelScript([])
    await expect(nativeToolResultAt(script, 0, '')).rejects.toThrow('tool call ID')
    expect(script.requestAt).not.toHaveBeenCalled()
  })

  it('passes the step index refusal of the model script through', async () => {
    // `ModelScript.requestAt` validates the index; its own tests cover each boundary.
    await expect(nativeToolResultAt(modelScript([]), -1, 'selected')).rejects.toThrow('nonnegative safe integer')
  })
})

describe('nativeFileReadResult', () => {
  it('rejects a stale exact Read even when the model context carries scripted NEW edit arguments', async () => {
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role: 'assistant', tool_calls: [{ id: 'native-edit', function: { arguments: '{"old_string":"OLD42","new_string":"NEW42"}' } }] },
      { role: 'tool', tool_call_id: 'native-read-after', content: 'OLD42\n' },
    ] } }
    await expect(nativeFileReadResult(request, 'native-read-after', 'NEW42', 'OLD42')).rejects.toThrow('expected current bytes')
  })
  it('reads the exact current call while excluding old results from another call', async () => {
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role: 'tool', tool_call_id: 'native-read-before', content: 'OLD42\n' },
      { role: 'tool', tool_call_id: 'native-read-after', content: 'NEW42\n' },
    ] } }
    expect(await nativeFileReadResult(request, 'native-read-after', 'NEW42', 'OLD42')).toBe('NEW42\n')
  })
  it('rejects an actual failed provider result even when its output contains the expected marker', async () => {
    const request: MockModelRequestRecord = { protocol: 'openai-responses', path: '/v1/responses', body: {} }
    await expect(nativeFileReadResult(request, 'native-read-after', 'NEW42', 'OLD42', () => ({ text: 'NEW42', exitCode: 7, failed: true }))).rejects.toThrow('Read returned a failure')
  })
})

describe('nativeFileEditSequence', () => {
  it.runIf(existsSync('/bin/sh'))('seeds the exact bytes without expanding shell characters in the filename', () => {
    const fileName = 'literal $(touch command-expanded-marker) \'quote\';.txt'
    const sequence = nativeFileEditSequence(AgentProvider.CODEBUDDY, { workingDir: directory, fileName })
    const command: unknown = calls.shell.mock.calls[0]?.[2]
    if (typeof command !== 'string')
      throw new Error('The native file seed supplied no shell command.')
    execFileSync('/bin/sh', ['-c', command], { cwd: directory })
    expect(readFileSync(sequence.filePath, 'utf8')).toBe('const parityBefore = 1\n')
    expect(existsSync(join(directory, 'command-expanded-marker'))).toBe(false)
    expect(calls.read).toHaveBeenCalledWith(AgentProvider.CODEBUDDY, 'read-file', sequence.filePath)
    expect(calls.edit).toHaveBeenCalledWith(AgentProvider.CODEBUDDY, 'edit-file', {
      path: sequence.filePath,
      before: 'const parityBefore = 1',
      after: 'const parityAfter = 2',
    })
  })

  it('preserves native shell, read, and edit call order', () => {
    const sequence = nativeFileEditSequence(AgentProvider.QODER, { workingDir: directory, fileName: 'qoder-file-probe.txt' })
    expect(sequence.steps.slice(0, 3).flatMap(step => step.toolCalls?.map(call => call.id) ?? []))
      .toEqual(['seed-file', 'read-file', 'edit-file'])
    expect(calls.shell.mock.invocationCallOrder[0]).toBeLessThan(calls.read.mock.invocationCallOrder[0]!)
    expect(calls.read.mock.invocationCallOrder[0]).toBeLessThan(calls.edit.mock.invocationCallOrder[0]!)
  })
})

describe('nativeFileWriteSequence', () => {
  it('passes the full native Write bytes to the provider vocabulary without a fixture write', () => {
    const sequence = nativeFileWriteSequence(AgentProvider.QODER, { workingDir: directory, fileName: 'qoder-written-probe.txt' })
    expect(calls.write).toHaveBeenCalledWith(AgentProvider.QODER, 'write-file', { path: sequence.filePath, content: 'written-42\n' })
    expect(existsSync(sequence.filePath)).toBe(false)
    expect(calls.shell).not.toHaveBeenCalled()
    expect(calls.read).not.toHaveBeenCalled()
    expect(calls.edit).not.toHaveBeenCalled()
  })
})

describe('native file sequence paths', () => {
  it.runIf(existsSync('/bin/sh'))('preserves the original basename inside a literal private directory', () => {
    const workingDir = createNativeToolDirectory(directory)
    const sequence = nativeFileEditSequence(AgentProvider.CODEBUDDY, { workingDir, fileName: 'parity.ts' })
    const command: unknown = calls.shell.mock.calls[0]?.[2]
    if (typeof command !== 'string')
      throw new Error('The native file seed supplied no shell command.')
    // A native shell starts in the agent directory, before the supplied child directory.
    execFileSync('/bin/sh', ['-c', command], { cwd: directory })
    expect(sequence.filePath).toBe(join(workingDir, 'parity.ts'))
    expect(readFileSync(sequence.filePath, 'utf8')).toBe('const parityBefore = 1\n')
    expect(command).toContain(quotePosixShellArgument(sequence.filePath))
    expect(calls.read).toHaveBeenCalledWith(AgentProvider.CODEBUDDY, 'read-file', sequence.filePath)
    expect(calls.edit).toHaveBeenCalledWith(AgentProvider.CODEBUDDY, 'edit-file', { path: sequence.filePath, before: 'const parityBefore = 1', after: 'const parityAfter = 2' })
    const writeSequence = nativeFileWriteSequence(AgentProvider.CODEBUDDY, { workingDir, fileName: 'written.txt' })
    expect(calls.write).toHaveBeenCalledWith(AgentProvider.CODEBUDDY, 'write-file', { path: writeSequence.filePath, content: 'written-42\n' })
    expect(existsSync(writeSequence.filePath)).toBe(false)
    expect(existsSync(join(directory, 'command-expanded-marker'))).toBe(false)
  })

  it('rejects relative directories and filename traversal before a tool is built', () => {
    for (const build of [nativeFileEditSequence, nativeFileWriteSequence]) {
      for (const workingDir of ['', '.', 'relative/path'])
        expect(() => build(AgentProvider.CODEBUDDY, { workingDir, fileName: 'file.txt' })).toThrow('absolute private working directory')
      for (const fileName of ['', '.', '..', '../escape', 'nested/file', 'nested\\file', '\0'])
        expect(() => build(AgentProvider.CODEBUDDY, { workingDir: directory, fileName })).toThrow('one filename component')
    }
    for (const call of [calls.shell, calls.read, calls.edit, calls.write])
      expect(call).not.toHaveBeenCalled()
  })
})

describe('exerciseShellToolExecution', () => {
  const stop = new Error('The unit scenario stops after it queues the first command.')

  /** Run the scenario until it sends its first prompt, and return the first command that it queued. */
  async function firstQueuedCommand(options: Parameters<typeof exerciseShellToolExecution>[1]): Promise<string> {
    const status: MockModelScenarioStatus = { complete: false, nextStep: 0, stepCount: 0, requests: [], unexpectedRequests: [], ruleMatches: {}, pendingGates: [] }
    const context: ManagedNativeScenarioContext = {
      page: guardedBrowserHandle<Page>({}),
      provider: AgentProvider.MIMO_CODE,
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      workspaceId: 'shell-unit',
      modelScript: {
        id: 'queued-shell-command',
        testDeadline: () => undefined,
        prompt: text => text,
        queue: async () => 0,
        requestAt: async () => {
          throw new Error('The shell scenario stops before it reads a request.')
        },
        rule: async () => {},
        fallback: async () => {},
        status: async () => status,
        waitForSteps: async () => status,
        waitForGate: async () => status,
        releaseGate: async () => {},
        releaseGateIfHeld: async () => false,
        allowUnconsumed: () => {},
      },
    }
    calls.agent.mockResolvedValue({ workingDir: directory })
    calls.send.mockRejectedValue(stop)
    await expect(exerciseShellToolExecution(context, options)).rejects.toBe(stop)
    const command: unknown = calls.shell.mock.calls[0]?.[2]
    if (typeof command !== 'string')
      throw new Error('The shell scenario queued no shell command.')
    return command
  }

  it('queues the command as it is when no output gate is set', async () => {
    const command = await firstQueuedCommand({ includeFailure: false })
    expect(command).toMatch(/^printf 'SHELL[0-9a-f]{32}%s\\n' 42 > .*; cat /)
    expect(command).not.toContain('trap')
  })

  it('queues the command behind an output gate when the option is set', async () => {
    const command = await firstQueuedCommand({ includeFailure: false, outputGate: true })
    expect(command).toContain('trap')
    expect(command).toMatch(/; printf 'SHELL[0-9a-f]{32}%s\\n' 42 > .*; cat /)
  })

  it('keeps the gate file inside the literal private tool directory', async () => {
    const command = await firstQueuedCommand({ includeFailure: false, outputGate: true })
    const toolDirectory = readdirSync(directory).find(name => name.startsWith('native path $(touch command-expanded-marker)'))
    if (!toolDirectory)
      throw new Error('The shell scenario created no private tool directory.')
    // The directory name holds shell metacharacters, so the hold must quote the release path. An unquoted path runs the marker command.
    const quotedRelease = /\[ ! -e (.+?) \]/.exec(command)?.[1]
    expect(quotedRelease?.startsWith(quotePosixShellArgument(join(directory, toolDirectory)).slice(0, -1))).toBe(true)
    expect(existsSync(join(directory, 'command-expanded-marker'))).toBe(false)
  })
})
