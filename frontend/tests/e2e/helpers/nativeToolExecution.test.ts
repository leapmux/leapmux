import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelScenarioStatus, MockModelStep } from './mockModelScript'
import type { MockModelServer } from './mockModelServer'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import type { NativeToolStep, ShellCommand, ShellResultEvidence } from './nativeToolExecution'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeLocator, fakeLocatorTree } from '~/test-support/fakeLocator'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { stepRequest } from './mockModelScript'
import { createMockModelServer } from './mockModelServer'
import { startModelScript } from './modelScriptFixture'
import { createNativeToolDirectory } from './nativeToolDirectory'
import { approveNativeToolsUntil, clickNativeToolApproval, exerciseFileEditSequence, exerciseNativeFileEdit, exerciseNativeFileRead, exerciseNativeFileWrite, exerciseShellToolExecution, expectFileDiff, expectShellToolRows, NATIVE_APPROVAL_LIMIT, nativeFileEditSequence, nativeFileReadResult, nativeFileWriteSequence, nativeToolResultAt, PARITY_AFTER, PARITY_BEFORE, processNativeToolApproval, runNativeToolSteps, runNativeToolTurn, waitForNativeToolSteps } from './nativeToolExecution'
import { quotePosixShellArgument } from './shellArguments'

const calls = vi.hoisted(() => ({ shell: vi.fn(), read: vi.fn(), edit: vi.fn(), write: vi.fn(), idle: vi.fn(), send: vi.fn(), agent: vi.fn(), noBanner: vi.fn(), chat: vi.fn() }))
vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  waitForAgentIdle: calls.idle,
  sendMessage: calls.send,
  expectNoControlBanner: calls.noBanner,
  chatText: calls.chat,
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

  it('passes the thinking of a step to the model step, and states none for a step without it', async () => {
    const { context, queued } = recordingContext()
    const thinking: NativeToolStep = { toolCalls: [{ id: 'unit-shell', name: 'Bash' }], reasoning: 'Run the shell first.' }
    await runNativeToolSteps(context, { steps: [thinking, threeSteps[1]!], prompt: 'Run.', answer: 'Done.', permissions: 'none' })
    const [first, second] = queued[0] ?? []
    expect(first).toEqual({ reasoning: 'Run the shell first.', toolCalls: [{ id: 'unit-shell', name: 'Bash' }] })
    expect(second).not.toHaveProperty('reasoning')
  })

  it('passes the thinking of a one-step turn to its tool step', async () => {
    const { context, queued } = recordingContext()
    // The recording script holds no request, so the turn stops when it reads the two requests back.
    await expect(runNativeToolTurn(context, { toolCalls: [{ id: 'unit-shell', name: 'Bash' }], reasoning: 'Think first.', prompt: 'Run.', answer: 'Done.', permissions: 'none' }))
      .rejects
      .toThrow('lacks requestAt')
    expect(queued[0]?.[0]).toEqual({ reasoning: 'Think first.', toolCalls: [{ id: 'unit-shell', name: 'Bash' }] })
  })

  it('passes an empty thinking through, because the step states it', async () => {
    const { context, queued } = recordingContext()
    await runNativeToolSteps(context, { steps: [{ toolCalls: [{ id: 'unit-shell', name: 'Bash' }], reasoning: '' }], prompt: 'Run.', answer: 'Done.', permissions: 'none' })
    expect(queued[0]?.[0]).toHaveProperty('reasoning', '')
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
    await approveNativeToolsUntil(guardedBrowserHandle<Page>({}), async () => true, { allow: allowButton(flags, () => true) })
    expect(flags).toEqual([])
  })

  it('allows each approval until the operation completes', async () => {
    const flags: boolean[] = []
    let clicks = 0
    await approveNativeToolsUntil(guardedBrowserHandle<Page>({}), async () => clicks >= 3, { allow: allowButton(flags, () => {
      clicks++
      return true
    }) })
    expect(clicks).toBe(3)
    expect(flags).toEqual([true, true, true])
  })

  it(`passes the limit flag after ${NATIVE_APPROVAL_LIMIT} approvals, so a further ready button fails the click`, async () => {
    const flags: boolean[] = []
    await approveNativeToolsUntil(guardedBrowserHandle<Page>({}), async () => flags.length > NATIVE_APPROVAL_LIMIT, { allow: allowButton(flags, () => true) })
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

  it('clicks only after the readiness check passes, and hands both callbacks the count of approvals', async () => {
    const flags: boolean[] = []
    const seen: string[] = []
    let readyAfter = 2
    await approveNativeToolsUntil(guardedBrowserHandle<Page>({}), async (approvals) => {
      seen.push(`completed ${approvals}`)
      return approvals >= 1
    }, {
      allow: allowButton(flags, () => true),
      ready: async (approvals) => {
        seen.push(`ready ${approvals}`)
        return --readyAfter <= 0
      },
    })
    expect(flags).toEqual([true])
    expect(seen.filter(entry => entry.startsWith('ready'))).toEqual(['ready 0', 'ready 0'])
    expect(seen.at(-1)).toBe('completed 1')
  })

  it('fails with the error of the readiness check, and clicks nothing', async () => {
    const flags: boolean[] = []
    const failure = new Error('The native session changed before its permission decision.')
    await expect(approveNativeToolsUntil(guardedBrowserHandle<Page>({}), async () => false, {
      allow: allowButton(flags, () => true),
      ready: async () => {
        throw failure
      },
    })).rejects.toThrow('The native session changed before its permission decision.')
    expect(flags).toEqual([])
  })

  it('fails with the error of the completion check, such as a start before any approval', async () => {
    const flags: boolean[] = []
    await expect(approveNativeToolsUntil(guardedBrowserHandle<Page>({}), async (approvals) => {
      if (approvals === 0)
        throw new Error('The tool started before its approval.')
      return true
    }, { allow: allowButton(flags, () => true) })).rejects.toThrow('The tool started before its approval.')
    expect(flags).toEqual([])
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

describe('native file read, edit, and write scenarios', () => {
  /** A model script whose turn runs `effect`, as the native tool does, and a page that answers each check as passed. */
  function fileTurn(effect: () => void) {
    const status: MockModelScenarioStatus = { complete: true, nextStep: 1_000, stepCount: 1_000, requests: [], unexpectedRequests: [], ruleMatches: {}, pendingGates: [] }
    const log: string[] = []
    const { page } = fakeLocatorTree({ log })
    const queued: MockModelStep[][] = []
    calls.send.mockImplementation(async () => {
      effect()
    })
    const context = {
      page,
      provider: AgentProvider.CLINE,
      modelScript: guardedBrowserHandle<NativeScenarioContext['modelScript']>({
        prompt: text => text,
        queue: async (...steps: MockModelStep[]) => {
          queued.push(steps)
          return 0
        },
        status: async () => status,
        waitForSteps: async () => status,
        requestAt: async stepIndex => ({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex, body: {} }),
      }),
    } satisfies NativeScenarioContext
    return { context, queued, log }
  }

  describe('exerciseNativeFileRead', () => {
    it('seeds three lines, reads them through the native tool, and requires the lines without their numbered form', async () => {
      const { context, queued } = fileTurn(() => {})
      calls.chat.mockResolvedValue('unit-read-1 unit-read-2 unit-read-3')
      await exerciseNativeFileRead(context, { directory, linePrefix: 'unit-read', numberedLine: (line, text) => `${line} | ${text}` })
      expect(readFileSync(join(directory, 'notes.txt'), 'utf8')).toBe('unit-read-1\nunit-read-2\nunit-read-3\n')
      expect(calls.read).toHaveBeenCalledExactlyOnceWith(AgentProvider.CLINE, 'read-notes', join(directory, 'notes.txt'))
      expect(queued[0]?.[0]).toEqual({ toolCalls: [{ id: 'read-notes', name: 'unit-read' }] })
    })

    it('fails when the row draws the numbered form of the native tool', async () => {
      const { context } = fileTurn(() => {})
      calls.chat.mockResolvedValue('3 | unit-read-3')
      await expect(exerciseNativeFileRead(context, { directory, linePrefix: 'unit-read', numberedLine: (line, text) => `${line} | ${text}` }))
        .rejects
        .toThrow('not their numbered form')
    })
  })

  describe('exerciseNativeFileEdit', () => {
    it('requires the exact changed bytes and the diff of the edit', async () => {
      const path = join(directory, 'parity.ts')
      const { context, log } = fileTurn(() => writeFileSync(path, `${PARITY_AFTER}\n`))
      await exerciseNativeFileEdit(context, { directory })
      expect(calls.edit).toHaveBeenCalledExactlyOnceWith(AgentProvider.CLINE, 'parity-edit', { path, before: PARITY_BEFORE, after: PARITY_AFTER })
      expect(log.at(-1)).toContain('to.be.visible')
    })

    it('fails when the edit leaves other bytes than the changed line', async () => {
      const path = join(directory, 'parity.ts')
      const { context } = fileTurn(() => writeFileSync(path, `${PARITY_AFTER}\n// trailer\n`))
      await expect(exerciseNativeFileEdit(context, { directory })).rejects.toThrow('the edit stores exactly the changed line')
    })
  })

  describe('exerciseNativeFileWrite', () => {
    const path = () => join(directory, 'note.txt')

    it('requires exactly the written content by default, and the file name in the transcript', async () => {
      const { context } = fileTurn(() => writeFileSync(path(), 'unit was here\n'))
      calls.chat.mockResolvedValue('Wrote note.txt')
      await exerciseNativeFileWrite(context, { directory, content: 'unit was here\n' })
      expect(calls.write).toHaveBeenCalledExactlyOnceWith(AgentProvider.CLINE, 'write-call', { path: path(), content: 'unit was here\n' })
    })

    it('requires exactly the stated other bytes of a provider that changes the content', async () => {
      const { context } = fileTurn(() => writeFileSync(path(), 'unit was here\n'))
      calls.chat.mockResolvedValue('Wrote note.txt')
      await exerciseNativeFileWrite(context, { directory, content: 'unit was here', stored: 'unit was here\n' })
    })

    it('fails on bytes that differ from the stated ones', async () => {
      const { context } = fileTurn(() => writeFileSync(path(), 'unit was here, and more\n'))
      calls.chat.mockResolvedValue('Wrote note.txt')
      await expect(exerciseNativeFileWrite(context, { directory, content: 'unit was here\n' })).rejects.toThrow('the write stores exactly the stated bytes')
    })

    it('accepts the whole-file pattern of a provider with unpinned bytes, and refuses a pattern that states part of the file', async () => {
      const { context } = fileTurn(() => writeFileSync(path(), 'unit was here'))
      calls.chat.mockResolvedValue('Wrote note.txt')
      await exerciseNativeFileWrite(context, { directory, content: 'unit was here', stored: /^unit was here\n?$/ })
      await expect(exerciseNativeFileWrite(context, { directory, content: 'unit was here', stored: /unit was here/ })).rejects.toThrow('from ^ to $')
    })

    it('fails when the file exists before the write', async () => {
      writeFileSync(path(), 'earlier')
      const { context, queued } = fileTurn(() => {})
      await expect(exerciseNativeFileWrite(context, { directory, content: 'unit was here' })).rejects.toThrow('does not exist before the write')
      expect(queued).toEqual([])
    })
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
  const stop = new Error('The unit scenario stops after it queues the commands.')

  /** Run the scenario until it sends its prompt, and return the shell commands of the one step that it queued. */
  async function queuedCommands(options: Parameters<typeof exerciseShellToolExecution>[1], provider = AgentProvider.MIMO_CODE): Promise<{ commands: string[], steps: MockModelStep[][] }> {
    const status: MockModelScenarioStatus = { complete: false, nextStep: 0, stepCount: 0, requests: [], unexpectedRequests: [], ruleMatches: {}, pendingGates: [] }
    const steps: MockModelStep[][] = []
    const context: ManagedNativeScenarioContext = {
      page: guardedBrowserHandle<Page>({}),
      provider,
      providerAgent: { provider, prefix: 'native-e2e' },
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      workspaceId: 'shell-unit',
      modelScript: {
        id: 'queued-shell-command',
        testDeadline: () => undefined,
        prompt: text => text,
        queue: async (...queued) => {
          steps.push(queued)
          return 0
        },
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
    const commands = calls.shell.mock.calls.map(([, , command]: unknown[]) => {
      if (typeof command !== 'string')
        throw new Error('The shell scenario queued no shell command.')
      return command
    })
    return { commands, steps }
  }

  it('queues the successful and the failed command in one model step, then the answer', async () => {
    const { commands, steps } = await queuedCommands({})
    expect(commands).toHaveLength(2)
    expect(commands[0]).toMatch(/^printf 'SHELL[0-9a-f]{32}%s\\n' 42 > .*; cat /)
    expect(commands[1]).toMatch(/^printf 'SHELLERR[0-9a-f]{32}%s\\n' 77 >&2; exit 7$/)
    expect(steps).toHaveLength(1)
    expect(steps[0]).toHaveLength(2)
    expect(steps[0]?.[0]?.toolCalls?.map(call => call.id)).toEqual([expect.stringMatching(/^shell-[0-9a-f]{32}-0$/), expect.stringMatching(/^shell-[0-9a-f]{32}-1$/)])
  })

  it('gives each command a model answer of its own for a provider that runs one call of an answer', async () => {
    const { commands, steps } = await queuedCommands({}, AgentProvider.JUNIE)
    expect(commands).toHaveLength(2)
    expect(steps).toHaveLength(1)
    expect(steps[0]).toHaveLength(3)
    expect(steps[0]?.slice(0, 2).map(step => step.toolCalls?.map(call => call.id))).toEqual([[expect.stringMatching(/-0$/)], [expect.stringMatching(/-1$/)]])
  })

  it('queues the successful command alone when the failure is left out', async () => {
    const { commands, steps } = await queuedCommands({ includeFailure: false })
    expect(commands).toHaveLength(1)
    expect(commands[0]).not.toContain('trap')
    expect(steps[0]?.[0]?.toolCalls).toHaveLength(1)
  })

  it('holds each command behind a gate of its own when the option is set', async () => {
    const { commands } = await queuedCommands({ outputGate: true })
    expect(commands).toHaveLength(2)
    for (const command of commands)
      expect(command).toContain('trap')
    expect(commands[0]).toMatch(/; printf 'SHELL[0-9a-f]{32}%s\\n' 42 > .*; cat /)
    const releases = commands.map(command => /\[ ! -e (.+?) \]/.exec(command)?.[1])
    expect(releases[0]).toBeDefined()
    expect(releases[0]).not.toBe(releases[1])
  })

  it('keeps the gate file inside the literal private tool directory', async () => {
    const { commands } = await queuedCommands({ includeFailure: false, outputGate: true })
    const toolDirectory = readdirSync(directory).find(name => name.startsWith('native path $(touch command-expanded-marker)'))
    if (!toolDirectory)
      throw new Error('The shell scenario created no private tool directory.')
    // The directory name holds shell metacharacters, so the hold must quote the release path. An unquoted path runs the marker command.
    const quotedRelease = /\[ ! -e (.+?) \]/.exec(commands[0] ?? '')?.[1]
    expect(quotedRelease?.startsWith(quotePosixShellArgument(join(directory, toolDirectory)).slice(0, -1))).toBe(true)
    expect(existsSync(join(directory, 'command-expanded-marker'))).toBe(false)
  })

  /**
   * Run the whole scenario: the sent prompt runs each queued command in a shell, as the native tool does, and the
   * provider reader returns its output with `exitCode` from `outcome`. The page answers each check as passed and logs it.
   */
  async function runScenario(
    options: Parameters<typeof exerciseShellToolExecution>[1],
    setup: { log?: string[], provider?: AgentProvider, outcome?: (stdout: string, stderr: string, status: number) => { text: string, exitCode?: number }, evidence?: (outcome: { text: string, exitCode?: number }, command: Readonly<ShellCommand>) => ShellResultEvidence } = {},
  ) {
    const log = setup.log ?? []
    const { page } = fakeLocatorTree({ log, page: { reload: async () => {
      log.push('reload')
    } } })
    const status: MockModelScenarioStatus = { complete: true, nextStep: 1_000, stepCount: 1_000, requests: [], unexpectedRequests: [], ruleMatches: {}, pendingGates: [] }
    const queued: Array<{ callId: string, command: string }> = []
    const requested: number[] = []
    const outcomes = new Map<string, { text: string, exitCode?: number }>()
    const outcome = setup.outcome ?? ((stdout, stderr, code) => ({ text: `${stdout}${stderr}`, exitCode: code }))
    calls.shell.mockImplementation((_provider: AgentProvider, id: string, command: string) => ({ id, name: 'unit-shell', arguments: { command } }))
    calls.agent.mockResolvedValue({ workingDir: directory })
    calls.send.mockImplementation(async () => {
      if (queued.length === 0)
        throw new Error('The scenario sent a prompt before it queued a command.')
      for (const call of queued) {
        try {
          outcomes.set(call.callId, outcome(execFileSync('/bin/sh', ['-c', call.command], { encoding: 'utf8', stdio: 'pipe' }), '', 0))
        }
        catch (error) {
          const failed = error as { stdout: string, stderr: string, status: number }
          outcomes.set(call.callId, outcome(failed.stdout, failed.stderr, failed.status))
        }
      }
    })
    const context: ManagedNativeScenarioContext = {
      page,
      provider: setup.provider ?? AgentProvider.CODEX,
      providerAgent: { provider: setup.provider ?? AgentProvider.CODEX, prefix: 'native-e2e' },
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      workspaceId: 'shell-unit',
      readToolResult: (_request, callId) => {
        const result = outcomes.get(callId)
        if (!result)
          throw new Error(`The native tool has no result for ${callId}.`)
        return result
      },
      modelScript: guardedBrowserHandle<NativeScenarioContext['modelScript']>({
        prompt: text => text,
        queue: async (...steps: MockModelStep[]) => {
          for (const call of steps.flatMap(step => step.toolCalls ?? [])) {
            if (typeof call.arguments?.command !== 'string')
              throw new Error('The scenario queued a shell call without a command.')
            queued.push({ callId: call.id, command: call.arguments.command })
          }
          return 0
        },
        status: async () => status,
        waitForSteps: async () => status,
        requestAt: async (stepIndex) => {
          requested.push(stepIndex)
          return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex, body: {} }
        },
      }),
    }
    await exerciseShellToolExecution(context, {
      ...options,
      ...(setup.evidence
        ? { readResult: (_request: MockModelRequestRecord, command: Readonly<ShellCommand>) => {
            const outcome = outcomes.get(command.callId)
            if (!outcome)
              throw new Error('The executed command has no captured native result.')
            return setup.evidence!(outcome, command)
          } }
        : {}),
    })
    return { log, queued, requested }
  }

  it.runIf(existsSync('/bin/sh'))('reads both results from the one request after the shared answer', async () => {
    expect((await runScenario({})).requested).toEqual([1, 1])
  })

  it.runIf(existsSync('/bin/sh'))('reads each result from the request after its own answer for a provider that runs one call of an answer', async () => {
    expect((await runScenario({}, { provider: AgentProvider.JUNIE })).requested).toEqual([1, 2])
  })

  it.runIf(existsSync('/bin/sh'))('runs both commands and proves the rows of both after the answer', async () => {
    const { log, queued } = await runScenario({})
    expect(queued).toHaveLength(2)
    const answer = log.findIndex(entry => entry.includes('hasText=The native shell scenario ended.'))
    const firstRow = log.findIndex(entry => entry.startsWith('to.be.visible') && entry.includes('[data-tool-message]'))
    expect(answer).toBeGreaterThanOrEqual(0)
    expect(firstRow).toBeGreaterThan(answer)
    expect(log.some(entry => entry.includes('hasText=Error (exit 7)'))).toBe(true)
    expect(log).not.toContain('reload')
  })

  it.runIf(existsSync('/bin/sh'))('proves the rows again after a reload when the option is set', async () => {
    const { log } = await runScenario({ includeFailure: false, reload: true })
    const reload = log.indexOf('reload')
    expect(reload).toBeGreaterThan(0)
    const rowChecks = (entries: string[]) => entries.filter(entry => entry.startsWith('to.be.visible') && entry.includes('[data-tool-message]'))
    expect(rowChecks(log.slice(reload + 1))).toEqual(rowChecks(log.slice(0, reload)))
    expect(rowChecks(log.slice(reload + 1)).length).toBeGreaterThan(0)
  })

  it.runIf(existsSync('/bin/sh'))('fails when the native exit code of the failed command is another code', async () => {
    await expect(runScenario({}, { outcome: (stdout, stderr, code) => ({ text: `${stdout}${stderr}`, exitCode: code === 7 ? 3 : code }) }))
      .rejects
      .toThrow('the native exit code reaches LeapMux')
  })

  it.runIf(existsSync('/bin/sh'))('reads the exit code from the model text when the reader states none', async () => {
    await runScenario({}, { outcome: (stdout, stderr, code) => ({ text: `${stdout}${stderr}${code ? `\nCommand exited with code ${code}` : ''}` }) })
    await expect(runScenario({}, { outcome: (stdout, stderr) => ({ text: `${stdout}${stderr}` }) }))
      .rejects
      .toThrow('the next model request states the native exit code')
  })

  it.runIf(existsSync('/bin/sh'))('fails when the next model request lacks the output of a command', async () => {
    await expect(runScenario({ includeFailure: false }, { outcome: () => ({ text: 'other words', exitCode: 0 }) }))
      .rejects
      .toThrow('the next model request holds the output of the command')
  })

  it.runIf(existsSync('/bin/sh'))('keeps the full marker assertion for a provider result hook', async () => {
    await expect(runScenario({}, { evidence: () => ({ kind: 'output', outcome: { text: 'no captured marker', exitCode: 0 } }) }))
      .rejects
      .toThrow('the next model request holds the output of the command')
  })

  it.runIf(existsSync('/bin/sh'))('refuses an exact record exception for a successful command', async () => {
    await expect(runScenario({}, { evidence: outcome => ({ kind: 'record', record: outcome.text, outcome: { ...outcome, exitCode: 7, failed: true } }) }))
      .rejects
      .toThrow('exact nonempty failed result')
  })

  it.runIf(existsSync('/bin/sh'))('refuses a failure record that differs from the native result', async () => {
    await expect(runScenario({}, { evidence: (outcome, command) => command.exitCode === 0
      ? { kind: 'output', outcome }
      : { kind: 'record', record: 'another record', outcome: { ...outcome, exitCode: 7, failed: true } } }))
      .rejects
      .toThrow('exact nonempty failed result')
  })

  it.runIf(existsSync('/bin/sh'))('refuses a blank failure record', async () => {
    await expect(runScenario({}, { evidence: (outcome, command) => command.exitCode === 0
      ? { kind: 'output', outcome }
      : { kind: 'record', record: '', outcome: { text: '', exitCode: 7, failed: true } } }))
      .rejects
      .toThrow('exact nonempty failed result')
  })
})

describe('expectShellToolRows', () => {
  const commands = [
    { output: 'SHELL1a42', printedPrefix: 'SHELL1a', exitCode: 0 },
    { output: 'SHELLERR1a77', printedPrefix: 'SHELLERR1a', exitCode: 7 },
  ] as const

  async function checks(provider: AgentProvider, answer: (expression: string, path: string) => boolean = () => true) {
    const log: string[] = []
    const { page } = fakeLocatorTree({ log, answer })
    await expectShellToolRows({ page, provider }, commands)
    return log
  }

  it('proves the output, the command, the rail, the final status, the exit code, the absent path and the separate rows', async () => {
    const log = await checks(AgentProvider.CODEX)
    expect(log).toContain('to.be.visible page >> [data-tool-message]:visible[hasText=SHELL1a42].first')
    expect(log).toContain('to.be.visible page >> [data-tool-message]:visible[hasText=printf][hasText=SHELL1a].first')
    expect(log).toContain('to.be.visible page >> [data-span-columns]:not([data-span-columns="0"]):visible[hasText=SHELL1a42].first')
    expect(log).toContain('to.have.attribute.value page >> [data-testid="message-bubble"][data-tool-row-role="result"]:visible[hasText=SHELL1a42].first')
    expect(log).toContain('to.be.visible page >> [data-tool-message]:visible[hasText=SHELLERR1a77][hasText=Error (exit 7)].first')
    expect(log).toContain('to.have.count page >> [data-testid="message-bubble"][data-tool-row-role="result"]:visible[hasText=SHELL1a42] >> testid=tool-output-file-paths')
    expect(log).toContain('to.have.count page >> [data-testid="message-bubble"][data-tool-row-role]:visible[hasText=SHELL1a][hasText=SHELLERR1a]')
    // A successful command states no exit code in its header.
    expect(log.some(entry => entry.includes('hasText=Error (exit 0)'))).toBe(false)
  })

  it('leaves out the output path of Grok Build, which shows the path of every command', async () => {
    const log = await checks(AgentProvider.GROK_BUILD)
    expect(log.some(entry => entry.includes('tool-output-file-paths'))).toBe(false)
  })

  it('requires the output-row rail for Factory Droid', async () => {
    const log = await checks(AgentProvider.DROID)
    expect(log).toContain('to.be.visible page >> [data-span-columns]:not([data-span-columns="0"]):visible[hasText=SHELL1a42].first')
  })

  it('fails with the message of the first part that the row does not draw', async () => {
    await expect(checks(AgentProvider.CODEX, (_expression, path) => !path.includes('Error (exit 7)')))
      .rejects
      .toThrow('the header of the failed row states the exit code')
  })

  /** A page whose result rows report `collapsed` expand controls, and which records each action. */
  function expandablePage(collapsed: boolean, text = '') {
    const log: string[] = []
    const node = (path: string): Locator => fakeLocator(() => true, {
      locator: (selector: string) => node(`${path} >> ${selector}`),
      filter: (filter: { hasText?: string }) => node(`${path}[${String(filter.hasText)}]`),
      first: () => node(`${path}.first`),
      getByTestId: (testId: string) => node(`${path} >> testid=${testId}`),
      getByRole: (role: string, options: { name?: string | RegExp }) => node(`${path} >> role=${role}[${String(options.name)}]`),
      all: async () => [node(`${path}#0`)],
      textContent: async () => text,
      allTextContents: async () => [text],
      count: async () => collapsed && path.endsWith('role=button[/^Expand(?: output)?$/]') ? 1 : 0,
      hover: async () => {
        log.push(`hover ${path}`)
      },
      click: async () => {
        log.push(`click ${path}`)
      },
    })
    return { page: node('page') as unknown as Page, log }
  }

  it('expands a collapsed output row before it reads the text of the chat', async () => {
    const { page, log } = expandablePage(true)
    calls.chat.mockResolvedValue('SHELL1a42 Error (exit 7) SHELLERR1a77')
    await expectShellToolRows({ page, provider: AgentProvider.CODEX }, commands, { absentRowText: ['Command exited with code'] })
    expect(log.filter(entry => entry.startsWith('click'))).toHaveLength(2)
    expect(calls.chat).toHaveBeenCalledOnce()
  })

  it('clicks nothing on a row that shows all of its text', async () => {
    const { page, log } = expandablePage(false)
    calls.chat.mockResolvedValue('SHELL1a42')
    await expectShellToolRows({ page, provider: AgentProvider.CODEX }, [commands[0]], { absentRowText: ['Exit code'] })
    expect(log.filter(entry => entry.startsWith('click'))).toEqual([])
  })

  it('fails when a row draws a text that the provider adds for its model', async () => {
    const { page } = expandablePage(false)
    calls.chat.mockResolvedValue('SHELLERR1a77\n\nCommand exited with code 7')
    await expect(expectShellToolRows({ page, provider: AgentProvider.CODEX }, commands, { absentRowText: ['Command exited with code'] }))
      .rejects
      .toThrow('no row draws the native text "Command exited with code"')
  })

  it('refuses a blank absent text, which every chat holds', async () => {
    const { page } = expandablePage(false)
    calls.chat.mockResolvedValue('SHELL1a42')
    await expect(expectShellToolRows({ page, provider: AgentProvider.CODEX }, [commands[0]], { absentRowText: [' '] }))
      .rejects
      .toThrow('nonblank fixed part')
  })

  it('reads no chat text when no absent text is stated', async () => {
    const { page } = expandablePage(true)
    await expectShellToolRows({ page, provider: AgentProvider.CODEX }, commands)
    expect(calls.chat).not.toHaveBeenCalled()
  })

  it('expands the row and preserves an exact native record without a missing marker', async () => {
    const record = 'Command: printf MARK%s 77 >&2; exit 7\nStdout: (empty)\nStderr: (empty)\nExit Code: 7\nSignal: (none)'
    const { page, log } = expandablePage(true, record)
    await expectShellToolRows({ page, provider: AgentProvider.CODEBUDDY }, [{ ...commands[1], printedPrefix: 'MARK', output: record, exactOutput: record }])
    expect(log.filter(entry => entry.startsWith('click'))).toHaveLength(1)
    expect(record).not.toContain(commands[1].output)
  })

  it('fails when the displayed native record loses any contents', async () => {
    const record = 'Command: printf SHELLERR1a%s 77 >&2; exit 7\nStdout: (empty)\nStderr: (empty)\nExit Code: 7\nSignal: (none)'
    const { page } = expandablePage(false, record.replace('\nSignal: (none)', ''))
    await expect(expectShellToolRows({ page, provider: AgentProvider.CODEBUDDY }, [{ ...commands[1], output: record, exactOutput: record }]))
      .rejects
      .toThrow('the row preserves the exact native record')
  })

  it('checks native notice fragments on one result without refusing another preserved record', async () => {
    const { page } = expandablePage(false, 'actual output')
    await expectShellToolRows({ page, provider: AgentProvider.CODEBUDDY }, [{ ...commands[0], absentRowText: ['Stdout:', 'Stderr:'] }])
    const wrong = expandablePage(false, 'Stdout: actual output')
    await expect(expectShellToolRows({ page: wrong.page, provider: AgentProvider.CODEBUDDY }, [{ ...commands[0], absentRowText: ['Stdout:'] }]))
      .rejects
      .toThrow('the result body omits its native notice')
  })
})
