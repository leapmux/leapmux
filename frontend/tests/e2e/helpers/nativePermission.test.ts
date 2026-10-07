import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { GatedOutput, OutputGate } from './outputGate'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { allowNativeOperation, createNativePermissionFileWrite, declinedToolCallId, exerciseAllowThenFeedbackRejection, exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseNativePermissionWrite, exerciseNativeToolWrite, exerciseRememberedAllow, expectDeclinedToolRow, expectDeclinedToolRowAcrossReload, expectSavedRefusalFeedback, toolResultCallId } from './nativePermission'
import { createOutputGate } from './outputGate'

const native = vi.hoisted(() => ({ directory: '', currentAgent: vi.fn() }))
vi.mock('./server', async (importOriginal) => {
  const original = await importOriginal<typeof import('./server')>()
  return { ...original, getGlobalState: () => ({ tmpDir: native.directory }) }
})
const declinedRow = vi.hoisted(() => ({ assertions: [] as string[] }))
/** The browser steps of a decision flow, in order. */
const flow = vi.hoisted(() => ({ events: [] as string[], onAnswer: undefined as ((decision: string) => void) | undefined, feedback: '' }))
/** What the fake bubble list was asked: each filter, in order, and the call ID attribute that its row states. */
const rowQuery = vi.hoisted(() => ({ filters: [] as unknown[], attribute: null as string | null }))
vi.mock('./ui', async (importOriginal) => {
  const original = await importOriginal<typeof import('./ui')>()
  const probe = { declinedRowProbe: true }
  // A filtered bubble list is itself a fake row, so a check with or without `first()` reaches the recording `expect`.
  const bubbles: { declinedRowProbe: true, filter: (options: unknown) => unknown, first: () => unknown, and: () => unknown, getAttribute: () => Promise<string | null> } = {
    declinedRowProbe: true,
    filter: (options) => {
      rowQuery.filters.push(options)
      return bubbles
    },
    first: () => probe,
    and: () => bubbles,
    getAttribute: async () => rowQuery.attribute,
  }
  // The control actions record which scope radio a helper selects, through its group and its own name.
  const radio = {
    declinedRowProbe: true,
    click: async () => {
      flow.events.push('click')
    },
  }
  const actions = {
    getByRole: (role: string, options: { name: string }) => {
      flow.events.push(`${role}:${options.name}`)
      return { getByRole: (inner: string, innerOptions: { name: string, exact?: boolean }) => {
        flow.events.push(`${inner}:${innerOptions.name}:${innerOptions.exact === true ? 'exact' : 'loose'}`)
        return radio
      } }
    },
  }
  return {
    ...original,
    sendMessage: async () => { flow.events.push('send') },
    waitForControlBanner: async () => {
      flow.events.push('banner')
      return probe
    },
    answerControl: async (_page: unknown, decision: string) => {
      flow.events.push(`answer:${decision}`)
      flow.onAnswer?.(decision)
    },
    enterControlFeedback: async (_page: unknown, text: string) => {
      flow.feedback = text
      flow.events.push('feedback')
    },
    waitForAgentIdle: async () => { flow.events.push('idle') },
    expectNoControlBanner: async () => { flow.events.push('no-banner') },
    openWorkspace: async (_page: unknown, workspaceId: string) => { declinedRow.assertions.push(`open:${workspaceId}`) },
    controlButton: (_page: unknown, action: string) => {
      flow.events.push(`button:${action}`)
      return probe
    },
    controlActions: () => actions,
    assistantBubbles: () => bubbles,
    messageBubbles: () => bubbles,
    userBubbles: () => bubbles,
  }
})
vi.mock('./nativeScenario', async (importOriginal) => {
  const original = await importOriginal<typeof import('./nativeScenario')>()
  return { ...original, currentNativeAgent: native.currentAgent }
})
vi.mock('@playwright/test', async (importOriginal) => {
  const original = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...original,
    // The fake result row records each assertion that the helper makes. Every other value reaches the real `expect`,
    // with the message of the helper.
    expect: (value: unknown, message?: string) => typeof value === 'object' && value !== null && 'declinedRowProbe' in value
      ? {
          toHaveCount: async (count: number) => { declinedRow.assertions.push(`count:${count}`) },
          toHaveAttribute: async (name: string, text: string) => { declinedRow.assertions.push(`attribute:${name}=${text}`) },
          toContainText: async (text: string) => { declinedRow.assertions.push(`text:${text}`) },
          toHaveText: async (text: string) => { declinedRow.assertions.push(`exact:${text}`) },
          toBeVisible: async () => { declinedRow.assertions.push('visible') },
          toBeChecked: async () => { declinedRow.assertions.push('checked') },
        }
      : original.expect(value, message),
  }
})
vi.mock('./providerToolCalls', () => ({ bashToolCall: (_provider: AgentProvider, id: string, command: string) => ({ id, name: 'unit-native-shell', arguments: { command } }) }))
const toolTurn = vi.hoisted(() => ({ run: vi.fn(), noControl: vi.fn() }))
vi.mock('./nativeToolExecution', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeToolExecution')>(),
  runNativeToolTurn: toolTurn.run,
}))
vi.mock('./nativeControlObservation', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeControlObservation')>(),
  expectNoNativeControl: toolTurn.noControl,
}))

const scratchRoot = resolve(process.cwd(), '../.tmp')
const context: ManagedNativeScenarioContext = {
  provider: AgentProvider.CLAUDE_CODE,
  providerAgent: { provider: AgentProvider.CLAUDE_CODE, prefix: 'native-e2e' },
  workspaceId: 'native-permission-unit',
  leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
  get page(): Page { throw new Error('The native permission plan must not access the browser.') },
  get modelScript(): ModelScript { throw new Error('The native permission plan must not access the model.') },
}

beforeEach(() => {
  vi.clearAllMocks()
  declinedRow.assertions.length = 0
  flow.events.length = 0
  flow.feedback = ''
  flow.onAnswer = undefined
  rowQuery.filters.length = 0
  rowQuery.attribute = null
  delete context.readToolResult
  mkdirSync(scratchRoot, { recursive: true })
  native.directory = mkdtempSync(join(scratchRoot, 'native-permission-plan-unit-'))
  native.currentAgent.mockResolvedValue({ workingDir: native.directory })
})
afterEach(() => rmSync(native.directory, { recursive: true, force: true }))

function commandFrom(plan: Awaited<ReturnType<typeof createNativePermissionFileWrite>>): string {
  const command = plan.toolCall.arguments?.command
  if (typeof command !== 'string')
    throw new Error('The prepared native permission operation contains no shell command.')
  return command
}

function result(callId: string, output: string): MockModelRequestRecord {
  return { protocol: 'openai-chat-completions', path: '/chat/completions', body: { messages: [{ role: 'tool', tool_call_id: callId, content: output }] } }
}

describe('createNativePermissionFileWrite', () => {
  it.runIf(existsSync('/bin/sh'))('keeps creation absent until execution and proves the actual calculated bytes', async () => {
    const fileName = 'literal $(touch expanded-marker) \'quote\';.txt'
    const plan = await createNativePermissionFileWrite(context, { fileName, callId: 'create-native', outputPrefix: 'NATIVECONTROL' })
    const path = join(native.directory, fileName)
    expect(existsSync(path)).toBe(false)
    await plan.beforeDecision()
    const output = execFileSync('/bin/sh', ['-c', commandFrom(plan)], { cwd: native.directory, encoding: 'utf8' })
    expect(output).toBe('NATIVECONTROL42\n')
    expect(readFileSync(path, 'utf8')).toBe('NATIVECONTROL42\n')
    expect(existsSync(join(native.directory, 'expanded-marker'))).toBe(false)
    await plan.nativeProof(result('create-native', output))
  })

  it.runIf(existsSync('/bin/sh'))('keeps the seeded replacement unchanged before execution', async () => {
    const plan = await createNativePermissionFileWrite(context, { fileName: 'replace.txt', callId: 'replace-native', outputPrefix: 'REPLACED', initialContent: 'original bytes' })
    const path = join(native.directory, 'replace.txt')
    expect(readFileSync(path, 'utf8')).toBe('original bytes')
    await plan.beforeDecision()
    const output = execFileSync('/bin/sh', ['-c', commandFrom(plan)], { cwd: native.directory, encoding: 'utf8' })
    expect(readFileSync(path, 'utf8')).toBe('REPLACED42\n')
    await plan.nativeProof(result('replace-native', output))
  })

  it('preserves an empty seeded value as a replacement', async () => {
    const plan = await createNativePermissionFileWrite(context, { fileName: 'empty.txt', callId: 'empty-native', outputPrefix: 'EMPTY', initialContent: '' })
    expect(existsSync(join(native.directory, 'empty.txt'))).toBe(true)
    expect(readFileSync(join(native.directory, 'empty.txt'), 'utf8')).toBe('')
    await plan.beforeDecision()
  })

  it('rejects an already existing creation target without changing its bytes', async () => {
    const path = join(native.directory, 'exists.txt')
    writeFileSync(path, 'unchanged')
    // Playwright's `expect` fails with the values that it compared: the target must not exist.
    await expect(createNativePermissionFileWrite(context, { fileName: 'exists.txt', callId: 'existing-native', outputPrefix: 'EXISTS' })).rejects.toMatchObject({ matcherResult: { actual: true, expected: false } })
    expect(readFileSync(path, 'utf8')).toBe('unchanged')
  })

  it.runIf(existsSync('/bin/sh'))('rejects the wrong native result ID after the real file write', async () => {
    const plan = await createNativePermissionFileWrite(context, { fileName: 'result.txt', callId: 'expected-native', outputPrefix: 'RESULT' })
    const output = execFileSync('/bin/sh', ['-c', commandFrom(plan)], { cwd: native.directory, encoding: 'utf8' })
    await expect(plan.nativeProof(result('wrong-native', output))).rejects.toThrow(/result|call/)
    expect(readFileSync(join(native.directory, 'result.txt'), 'utf8')).toBe('RESULT42\n')
  })

  it.runIf(existsSync('/bin/sh'))('dispatches the exact request and ID to a provider-owned reader', async () => {
    let output = ''
    const readToolResult = vi.fn(() => ({ text: output }))
    context.readToolResult = readToolResult
    const plan = await createNativePermissionFileWrite(context, { fileName: 'owned.txt', callId: 'owned-native', outputPrefix: 'OWNED' })
    output = execFileSync('/bin/sh', ['-c', commandFrom(plan)], { cwd: native.directory, encoding: 'utf8' })
    const request: MockModelRequestRecord = { protocol: 'aws-event-stream', path: '/', body: { opaque: true } }
    await plan.nativeProof(request)
    expect(readToolResult).toHaveBeenCalledWith(request, 'owned-native')
  })

  it.each(['', '.', '..', '../escape', 'nested/file', 'nested\\file', '\0'])('rejects an invalid filename before Worker access: %j', async (fileName) => {
    await expect(createNativePermissionFileWrite(context, { fileName, callId: 'invalid-native', outputPrefix: 'INVALID' })).rejects.toThrow('one filename component')
    expect(native.currentAgent).not.toHaveBeenCalled()
    expect(readdirSync(native.directory)).toEqual([])
  })

  it.each([{ callId: '', outputPrefix: 'OUTPUT' }, { callId: 'call', outputPrefix: '' }])('rejects an absent operation identifier before Worker access: %j', async (options) => {
    await expect(createNativePermissionFileWrite(context, { fileName: 'file.txt', ...options })).rejects.toThrow('call ID and output prefix')
    expect(native.currentAgent).not.toHaveBeenCalled()
    expect(readdirSync(native.directory)).toEqual([])
  })

  it('rejects an absent actual native working directory', async () => {
    native.currentAgent.mockResolvedValue({ workingDir: '' })
    await expect(createNativePermissionFileWrite(context, { fileName: 'file.txt', callId: 'call', outputPrefix: 'OUTPUT' })).rejects.toThrow('requires a working directory')
    expect(readdirSync(native.directory)).toEqual([])
  })
})

/** A context whose script records each step, and whose page answers each locator with the fake row. */
function flowContext(): { context: ManagedNativeScenarioContext, request: MockModelRequestRecord } {
  const request = result('decided-native', 'DECIDED42')
  const page = Object.assign({} as Page, {
    locator: (selector: string) => {
      flow.events.push(`locator:${selector}`)
      return { declinedRowProbe: true }
    },
  })
  const modelScript = {
    prompt: (text: string) => text,
    queue: async () => {
      flow.events.push('queue')
      return 4
    },
    waitForSteps: async (count: number) => {
      flow.events.push(`steps:${count}`)
    },
    requestAt: async (index: number) => {
      flow.events.push(`request:${index}`)
      return request
    },
  } as unknown as ModelScript
  // The shared context throws on a browser or model access, so a spread of it would throw. Copy its plain fields.
  return { context: { provider: context.provider, providerAgent: context.providerAgent, workspaceId: context.workspaceId, leapmuxServer: context.leapmuxServer, page, modelScript }, request }
}

describe('exerciseNativePermissionDecision', () => {
  it('answers the request, proves the native result and the view, and returns the request after the decision', async () => {
    const { context: decided, request } = flowContext()
    const returned = await exerciseNativePermissionDecision(decided, {
      toolCall: { id: 'decided-native', name: 'unit-native-shell', arguments: { command: 'true' } },
      decision: 'deny',
      beforeDecision: () => { flow.events.push('before') },
      nativeProof: (read) => {
        expect(read).toBe(request)
        flow.events.push('native')
      },
      viewProof: async () => { flow.events.push('view') },
    })
    expect(returned).toBe(request)
    expect(flow.events).toEqual([
      'queue',
      'send',
      'steps:5',
      'banner',
      'before',
      'locator:[data-testid="dialog-editor"]:visible',
      'answer:deny',
      'steps:6',
      'idle',
      'request:5',
      'native',
      'view',
    ])
    expect(declinedRow.assertions).toEqual(['count:0', 'visible'])
  })

  it('reaches no view proof when the native proof fails', async () => {
    const { context: decided } = flowContext()
    const viewProof = vi.fn(async () => {})
    await expect(exerciseNativePermissionDecision(decided, {
      toolCall: { id: 'decided-native', name: 'unit-native-shell', arguments: { command: 'true' } },
      decision: 'allow',
      nativeProof: () => {
        throw new Error('the native result is absent')
      },
      viewProof,
    })).rejects.toThrow('the native result is absent')
    expect(viewProof).not.toHaveBeenCalled()
  })

  it('refuses an output gate on a denial before it touches the model or the browser', async () => {
    const outputGate = { gate: createOutputGate(native.directory), shown: vi.fn(async () => {}) }
    await expect(exerciseNativePermissionDecision(context, {
      toolCall: { id: 'denied-native', name: 'unit-native-shell', arguments: { command: 'true' } },
      decision: 'deny',
      outputGate,
      nativeProof: () => {},
    })).rejects.toThrow('A denied command prints no output')
    expect(outputGate.shown).not.toHaveBeenCalled()
  })
})

describe('allowNativeOperation', () => {
  /** An operation that records its guard and its proof. */
  function operation(outputGate?: GatedOutput): NativePermissionOperationPlan {
    return {
      toolCall: { id: 'decided-native', name: 'unit-native-shell', arguments: { command: 'true' } },
      ...(outputGate ? { outputGate } : {}),
      beforeDecision: () => { flow.events.push('guard') },
      nativeProof: () => { flow.events.push('operation proof') },
    }
  }

  it('reads the target before the banner check, allows, and proves the operation before the extra proof', async () => {
    const { context: decided, request } = flowContext()
    const allow = allowNativeOperation(decided, operation(), (read) => {
      expect(read).toBe(request)
      flow.events.push('extra proof')
    })
    expect(await allow(async () => {
      flow.events.push('banner check')
    })).toBe(request)
    expect(flow.events).toEqual([
      'queue',
      'send',
      'steps:5',
      'banner',
      'guard',
      'banner check',
      'locator:[data-testid="dialog-editor"]:visible',
      'answer:allow',
      'steps:6',
      'idle',
      'request:5',
      'operation proof',
      'extra proof',
    ])
  })

  it('holds the command of the operation behind its output gate until the browser shows the output', async () => {
    const { context: decided } = flowContext()
    const release = vi.fn()
    const shown = vi.fn(async () => {
      flow.events.push('output shown')
    })
    const gate = { gate: { release } as unknown as OutputGate, shown }
    await allowNativeOperation(decided, operation(gate))(async () => {})
    expect(gate.shown).toHaveBeenCalledOnce()
    expect(release).toHaveBeenCalled()
    expect(flow.events.indexOf('output shown')).toBeGreaterThan(flow.events.indexOf('answer:allow'))
  })

  it('stops before the decision when the guard of the operation fails', async () => {
    const { context: decided } = flowContext()
    const failure = new Error('The target changed before the decision.')
    const check = vi.fn(async () => {})
    await expect(allowNativeOperation(decided, { ...operation(), beforeDecision: () => {
      throw failure
    } })(check)).rejects.toBe(failure)
    expect(check).not.toHaveBeenCalled()
    expect(flow.events).not.toContain('answer:allow')
  })
})

describe('exerciseAllowThenFeedbackRejection', () => {
  /** A context whose agent writes the allowed file when the browser allows, and whose third request holds `body`. */
  function feedbackContext(body: unknown): ManagedNativeScenarioContext {
    flow.onAnswer = (decision) => {
      if (decision === 'allow')
        writeFileSync(join(native.directory, 'approved.txt'), 'approved')
    }
    const press = async (key: string) => {
      flow.events.push(`press:${key}`)
    }
    const page = Object.assign({} as Page, { keyboard: { press } })
    const modelScript = {
      prompt: (text: string) => text,
      queue: async (...steps: unknown[]) => {
        flow.events.push(`queue:${steps.length}`)
        return 4
      },
      waitForSteps: async (count: number) => { flow.events.push(`steps:${count}`) },
      requestAt: async (index: number): Promise<MockModelRequestRecord> => {
        flow.events.push(`request:${index}`)
        return { protocol: 'anthropic-messages', path: '/v1/messages', body }
      },
    } as unknown as ModelScript
    return { provider: context.provider, providerAgent: context.providerAgent, workspaceId: context.workspaceId, leapmuxServer: context.leapmuxServer, page, modelScript }
  }

  afterEach(() => {
    flow.onAnswer = undefined
  })

  it('allows the first command, refuses the second with the reason, and reads the reason in the same turn', async () => {
    flow.events = []
    declinedRow.assertions = []
    const reason = 'Do not create the second file.'
    await exerciseAllowThenFeedbackRejection(feedbackContext({ messages: [{ role: 'user', content: reason }] }), { workingDir: native.directory })
    expect(flow.events).toEqual([
      'queue:3',
      'send',
      'steps:5',
      'banner',
      'answer:allow',
      'steps:6',
      'feedback',
      'press:Meta+Enter',
      'steps:7',
      'idle',
      'request:6',
    ])
    expect(flow.feedback).toBe(reason)
    // The saved answer is the bubble that holds the lead of a feedback row and the reason.
    expect(rowQuery.filters).toContainEqual({ hasText: 'Sent feedback:' })
    expect(rowQuery.filters).toContainEqual({ hasText: reason })
    expect(declinedRow.assertions).toEqual([
      `text:printf approved > ${join(native.directory, 'approved.txt')}`,
      `text:printf rejected > ${join(native.directory, 'rejected.txt')}`,
      'count:0',
      'visible',
      'visible',
    ])
    expect(existsSync(join(native.directory, 'rejected.txt'))).toBe(false)
  })

  it('fails when the request after the refusal lacks the reason', async () => {
    await expect(exerciseAllowThenFeedbackRejection(feedbackContext({ messages: [] }), { workingDir: native.directory })).rejects.toMatchObject({ matcherResult: { name: 'toContain', message: expect.stringContaining('Do not create the second file.') } })
  })

  it.each(['relative/dir', '/path with space', '/path/$(touch marker)', '/path/\'quote\''])('refuses the working directory %j before it touches the model', async (workingDir) => {
    await expect(exerciseAllowThenFeedbackRejection(context, { workingDir })).rejects.toThrow('needs no shell quoting')
  })
})

describe('exerciseNativeToolWrite', () => {
  /** A context whose page the turn reaches only through the mocked browser helpers. */
  const writeContext = (): ManagedNativeScenarioContext => ({
    provider: context.provider,
    providerAgent: context.providerAgent,
    workspaceId: context.workspaceId,
    leapmuxServer: context.leapmuxServer,
    page: {} as Page,
    modelScript: {} as ModelScript,
  })

  /** Run the write command as the native tool does, and answer with the output that the model reads. */
  function runToolTurn(options: { runsCommand: boolean }) {
    toolTurn.run.mockImplementation(async (_context: unknown, turn: { toolCalls: Array<{ id: string, arguments: { command: string } }> }) => {
      const call = turn.toolCalls[0]
      if (!call)
        throw new Error('The native write turn holds no tool call.')
      const output = options.runsCommand ? execFileSync('/bin/sh', ['-c', call.arguments.command], { encoding: 'utf8' }) : ''
      const request = result(call.id, output)
      return { start: 0, toolRequest: request, resultRequest: request }
    })
  }

  it.runIf(existsSync('/bin/sh'))('runs the write as one native tool turn that allows each native request, and proves the native result', async () => {
    runToolTurn({ runsCommand: true })
    await exerciseNativeToolWrite(writeContext(), { permission: 'native' })
    expect(toolTurn.run).toHaveBeenCalledOnce()
    expect(toolTurn.run.mock.calls[0]?.[1]).toMatchObject({ prompt: 'Run the scripted native preset write.', answer: 'The native preset write ended.', permissions: 'allow' })
    expect(toolTurn.noControl).not.toHaveBeenCalled()
    expect(declinedRow.assertions).toEqual(['visible'])
  })

  it.runIf(existsSync('/bin/sh'))('runs the turn with no native request inside the proof that no control appears', async () => {
    runToolTurn({ runsCommand: true })
    toolTurn.noControl.mockImplementation(async (_context: unknown, options: { testId: string, relatedProof: () => Promise<unknown> }) => {
      flow.events.push(`no-control:${options.testId}`)
      await options.relatedProof()
    })
    await exerciseNativeToolWrite(writeContext(), { permission: 'absent' })
    expect(flow.events).toEqual(['no-control:control-banner'])
    expect(toolTurn.run.mock.calls[0]?.[1]).toMatchObject({ permissions: 'none' })
  })

  it('fails when the native tool did not write the file', async () => {
    runToolTurn({ runsCommand: false })
    await expect(exerciseNativeToolWrite(writeContext(), { permission: 'native' })).rejects.toMatchObject({ matcherResult: { actual: expect.stringMatching(/^BEFORE/), expected: expect.stringMatching(/^AFTER/) } })
    expect(declinedRow.assertions).toEqual([])
  })
})

describe('expectDeclinedToolRow', () => {
  /** A page that records each selector and answers every locator with the fake result row. */
  function rowPage() {
    const selectors: string[] = []
    // The call ID reaches the page unescaped here, so a helper that escapes it in the browser builds another selector.
    const page = Object.assign({} as Page, {
      evaluate: async (_read: unknown, id: string) => id,
      locator: (selector: string) => {
        selectors.push(selector)
        return { declinedRowProbe: true }
      },
    })
    return { page, selectors }
  }

  it('selects the visible result row of the call through the shared row locator', async () => {
    const { page, selectors } = rowPage()
    await expectDeclinedToolRow(page, 'call-1')
    expect(selectors).toEqual(['[data-testid="message-bubble"][data-tool-call-id="call-1"][data-tool-row-role="result"]:visible'])
  })

  it('escapes a quote and a backslash in the rendered call ID for a quoted attribute value', async () => {
    const { page, selectors } = rowPage()
    await expectDeclinedToolRow(page, 'Run"Shell\\Command__1')
    expect(selectors).toEqual(['[data-testid="message-bubble"][data-tool-call-id="Run\\"Shell\\\\Command__1"][data-tool-row-role="result"]:visible'])
  })

  it('requires one declined row and states no refusal text by default', async () => {
    const { page } = rowPage()
    await expectDeclinedToolRow(page, 'call-1')
    expect(declinedRow.assertions).toEqual(['count:1', 'attribute:data-tool-status=declined', 'text:Declined'])
  })

  it('also requires the native refusal text when the caller gives one', async () => {
    const { page } = rowPage()
    await expectDeclinedToolRow(page, 'call-1', 'The user rejected this tool call.')
    expect(declinedRow.assertions).toEqual(['count:1', 'attribute:data-tool-status=declined', 'text:Declined', 'text:The user rejected this tool call.'])
  })

  it('requires the refusal text even when it is the empty string', async () => {
    const { page } = rowPage()
    await expectDeclinedToolRow(page, 'call-1', '')
    expect(declinedRow.assertions.at(-1)).toBe('text:')
  })
})

describe('expectDeclinedToolRowAcrossReload', () => {
  it('requires the declined row, reloads and opens the workspace, and requires the row again', async () => {
    const page = Object.assign({} as Page, {
      locator: () => ({ declinedRowProbe: true }),
      reload: async () => { declinedRow.assertions.push('reload') },
    })
    await expectDeclinedToolRowAcrossReload({ page, workspaceId: 'reload-workspace' }, 'call-1', 'The user refused.')
    const declined = ['count:1', 'attribute:data-tool-status=declined', 'text:Declined', 'text:The user refused.']
    expect(declinedRow.assertions).toEqual([...declined, 'reload', 'open:reload-workspace', ...declined])
  })

  it('stops before the reload when the row is not declined', async () => {
    const failure = new Error('the row is not declined')
    const page = Object.assign({} as Page, {
      locator: () => {
        throw failure
      },
      reload: async () => { declinedRow.assertions.push('reload') },
    })
    await expect(expectDeclinedToolRowAcrossReload({ page, workspaceId: 'reload-workspace' }, 'call-1')).rejects.toBe(failure)
    expect(declinedRow.assertions).not.toContain('reload')
  })
})

describe('toolResultCallId', () => {
  const page = Object.assign({} as Page, { locator: (selector: string) => ({ selector }) })

  it('reads the call ID of the one result row that holds the text', async () => {
    rowQuery.attribute = 'TU-generated-1'
    expect(await toolResultCallId(page, 'Use the clean target.')).toBe('TU-generated-1')
    expect(rowQuery.filters).toEqual([{ hasText: 'Use the clean target.' }])
    expect(declinedRow.assertions).toEqual(['count:1'])
  })

  it.each(['', '   '])('refuses the blank text %j before it reads the page', async (text) => {
    await expect(toolResultCallId(page, text)).rejects.toThrow('needs text')
    expect(rowQuery.filters).toEqual([])
  })

  it('fails when the row states no call ID', async () => {
    rowQuery.attribute = ''
    await expect(toolResultCallId(page, 'Use the clean target.')).rejects.toThrow('states no call ID')
  })
})

describe('declinedToolCallId', () => {
  it('reads the call ID of the one declined result row, whatever text it holds', async () => {
    const selectors: string[] = []
    const page = Object.assign({} as Page, { locator: (selector: string) => {
      selectors.push(selector)
      return { selector }
    } })
    rowQuery.attribute = 'exec-generated-1'
    expect(await declinedToolCallId(page)).toBe('exec-generated-1')
    expect(selectors).toEqual(['[data-tool-row-role="result"]', '[data-tool-status="declined"]'])
    expect(rowQuery.filters).toEqual([])
    expect(declinedRow.assertions).toEqual(['count:1'])
  })

  it('fails when the declined row states no call ID', async () => {
    const page = Object.assign({} as Page, { locator: () => ({}) })
    await expect(declinedToolCallId(page)).rejects.toThrow('states a declined call states no call ID')
  })
})

describe('expectSavedRefusalFeedback', () => {
  it('requires a visible bubble that holds the feedback lead and the reason', async () => {
    await expectSavedRefusalFeedback({} as Page, 'Keep the file.')
    expect(rowQuery.filters).toEqual([{ hasText: 'Sent feedback:' }, { hasText: 'Keep the file.' }])
    expect(declinedRow.assertions).toEqual(['visible'])
  })
})

/**
 * A context whose script records each queued turn, starts each turn at the next of `starts`, and answers each request
 * index with the body that `bodies` states for the reason that the reader typed.
 */
function scriptedContext(bodies: (feedback: string) => Record<number, unknown>, starts: readonly number[] = [4]) {
  const queued: unknown[][] = []
  let turn = 0
  const modelScript = {
    prompt: (text: string) => text,
    queue: async (...steps: unknown[]) => {
      queued.push(steps)
      flow.events.push(`queue:${steps.length}`)
      return starts[turn++] ?? 4
    },
    waitForSteps: async (count: number) => { flow.events.push(`steps:${count}`) },
    requestAt: async (index: number): Promise<MockModelRequestRecord> => {
      flow.events.push(`request:${index}`)
      return { protocol: 'openai-chat-completions', path: '/chat/completions', body: bodies(flow.feedback)[index] ?? { messages: [] } }
    },
  } as unknown as ModelScript
  const scripted: ManagedNativeScenarioContext = { provider: context.provider, providerAgent: context.providerAgent, workspaceId: context.workspaceId, leapmuxServer: context.leapmuxServer, page: {} as Page, modelScript }
  return { context: scripted, queued }
}

/** A request body whose user message holds `text`. */
function holding(text: string): Record<string, unknown> {
  return { messages: [{ role: 'user', content: text }] }
}

describe('exerciseNativePermissionReason', () => {
  const toolCall = { id: 'refused-native', name: 'unit-native-shell', arguments: { command: 'true' } }

  it('refuses with the typed reason and requires it in the request that continues the refused turn', async () => {
    const { context: refused, queued } = scriptedContext(feedback => ({ 5: holding(feedback) }))
    const viewProof = vi.fn(async () => {})
    await exerciseNativePermissionReason(refused, {
      toolCall,
      route: 'native-reply',
      beforeDecision: () => { flow.events.push('before') },
      expectNotRun: () => { flow.events.push('not-run') },
      viewProof,
    })
    expect(queued).toEqual([[{ toolCalls: [toolCall] }, { text: expect.stringContaining('The refusal reached the model.') }]])
    expect(flow.events).toEqual(['queue:2', 'send', 'steps:5', 'banner', 'before', 'feedback', 'button:deny', 'answer:deny', 'steps:6', 'idle', 'not-run', 'request:5'])
    expect(flow.feedback).toMatch(/^Leave the target as it is\. REASON[0-9a-f]{32}$/)
    expect(declinedRow.assertions).toEqual(['exact:Send feedback', 'count:0', 'visible'])
    expect(viewProof).toHaveBeenCalledExactlyOnceWith(flow.feedback)
  })

  it('requires the reason in the turn of the next message, and not in the turn that the refusal continues', async () => {
    const { context: refused, queued } = scriptedContext(feedback => ({ 5: holding('The call was refused.'), 6: holding(feedback) }))
    await exerciseNativePermissionReason(refused, {
      toolCall,
      route: 'next-message',
      expectNotRun: () => {
        flow.events.push('not-run')
      },
    })
    expect(queued).toEqual([[
      { toolCalls: [toolCall] },
      { text: expect.stringContaining('The refusal reached the model.') },
      { text: expect.stringContaining('The reason reached the model.') },
    ]])
    expect(flow.events).toEqual(['queue:3', 'send', 'steps:5', 'banner', 'feedback', 'button:deny', 'answer:deny', 'steps:7', 'idle', 'not-run', 'request:5', 'request:6'])
    // The refusal answer, the user row of the reason, and the answer to the reason.
    expect(declinedRow.assertions).toEqual(['exact:Send feedback', 'count:0', 'visible', 'visible', 'visible'])
    expect(rowQuery.filters).toContainEqual({ hasText: flow.feedback })
  })

  it('requires the reason in the first request after a refusal that ends the turn', async () => {
    const { context: refused, queued } = scriptedContext(feedback => ({ 5: holding(feedback) }))
    await exerciseNativePermissionReason(refused, { toolCall, route: 'next-message', afterRefusal: 'ends', expectNotRun: () => {} })
    expect(queued).toEqual([[{ toolCalls: [toolCall] }, { text: expect.stringContaining('The reason reached the model.') }]])
    expect(flow.events).toEqual(['queue:2', 'send', 'steps:5', 'banner', 'feedback', 'button:deny', 'answer:deny', 'steps:6', 'idle', 'request:5'])
    expect(declinedRow.assertions).toEqual(['exact:Send feedback', 'count:0', 'visible', 'visible'])
  })

  it('refuses a native reply in a turn that ends before it touches the model or the browser', async () => {
    await expect(exerciseNativePermissionReason(context, { toolCall, route: 'native-reply', afterRefusal: 'ends', expectNotRun: () => {} }))
      .rejects
      .toThrow('reaches the model only in a turn that continues')
  })

  it('fails when the request that continues the refused turn lacks the reason', async () => {
    const { context: refused } = scriptedContext(() => ({ 5: holding('The call was refused.') }))
    const viewProof = vi.fn(async () => {})
    await expect(exerciseNativePermissionReason(refused, { toolCall, route: 'native-reply', expectNotRun: () => {}, viewProof }))
      .rejects
      .toMatchObject({ matcherResult: { name: 'toContain' } })
    expect(viewProof).not.toHaveBeenCalled()
  })

  it('fails when the refused turn already carries the reason that must wait for the next message', async () => {
    const { context: refused } = scriptedContext(feedback => ({ 5: holding(feedback), 6: holding(feedback) }))
    await expect(exerciseNativePermissionReason(refused, { toolCall, route: 'next-message', expectNotRun: () => {} }))
      .rejects
      .toMatchObject({ matcherResult: { name: 'toContain', pass: true } })
  })

  it('stops before the view proof when the refused call ran', async () => {
    const { context: refused } = scriptedContext(feedback => ({ 5: holding(feedback) }))
    const viewProof = vi.fn(async () => {})
    await expect(exerciseNativePermissionReason(refused, {
      toolCall,
      route: 'native-reply',
      expectNotRun: () => {
        throw new Error('the refused command wrote its file')
      },
      viewProof,
    })).rejects.toThrow('the refused command wrote its file')
    expect(viewProof).not.toHaveBeenCalled()
  })
})

describe('exerciseRememberedAllow', () => {
  const firstCall = { id: 'remembered-first', name: 'unit-native-shell', arguments: { command: 'true' } }
  const secondCall = { id: 'remembered-second', name: 'unit-native-shell', arguments: { command: 'true' } }

  beforeEach(() => {
    toolTurn.noControl.mockImplementation(async (_context: unknown, options: { testId: string, relatedProof: () => Promise<unknown> }) => {
      flow.events.push(`observe:${options.testId}`)
      await options.relatedProof()
      flow.events.push('observed')
    })
  })

  it('rejects a rule file outside the private run before it queues a turn', async () => {
    const outside = mkdtempSync(join(scratchRoot, 'outside-rule-test-'))
    const path = join(outside, 'operator.rules')
    writeFileSync(path, 'outside rule\n')
    const { context: remembered, queued } = scriptedContext(() => ({}), [4, 6])
    try {
      await expect(exerciseRememberedAllow(remembered, {
        scope: 'Always',
        firstCall,
        secondCall,
        firstProof: () => {},
        secondProof: () => {},
        ruleFiles: [path],
      })).rejects.toThrow('outside the E2E run')
      expect(queued).toEqual([])
      expect(readFileSync(path, 'utf8')).toBe('outside rule\n')
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })

  it('rejects a rule file whose parent link points outside the private run', async () => {
    const outside = mkdtempSync(join(scratchRoot, 'outside-rule-link-test-'))
    const link = join(native.directory, 'rule-link')
    const path = join(outside, 'operator.rules')
    writeFileSync(path, 'outside rule\n')
    symlinkSync(outside, link, 'junction')
    const { context: remembered, queued } = scriptedContext(() => ({}), [4, 6])
    try {
      await expect(exerciseRememberedAllow(remembered, {
        scope: 'Always',
        firstCall,
        secondCall,
        firstProof: () => {},
        secondProof: () => {},
        ruleFiles: [join(link, 'operator.rules')],
      })).rejects.toThrow('outside the E2E run')
      expect(queued).toEqual([])
      expect(readFileSync(path, 'utf8')).toBe('outside rule\n')
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })

  it('allows under the scope, then runs the covered call in a turn that the observation watches', async () => {
    const { context: remembered, queued } = scriptedContext(() => ({ 5: { turn: 'first' }, 7: { turn: 'second' } }), [4, 6])
    await exerciseRememberedAllow(remembered, {
      scope: 'Session',
      firstCall,
      secondCall,
      beforeDecision: () => { flow.events.push('before') },
      firstProof: (request) => {
        expect(request.body).toEqual({ turn: 'first' })
        flow.events.push('first-proof')
      },
      secondProof: (request) => {
        expect(request.body).toEqual({ turn: 'second' })
        flow.events.push('second-proof')
      },
      viewProof: async () => { flow.events.push('view') },
    })
    expect(queued).toEqual([
      [{ toolCalls: [firstCall] }, { text: expect.stringContaining('The first call ran.') }],
      [{ toolCalls: [secondCall] }, { text: expect.stringContaining('The covered call ran.') }],
    ])
    expect(flow.events).toEqual([
      'queue:2',
      'send',
      'steps:5',
      'banner',
      'before',
      'radiogroup:Allow scope',
      'radio:Session:exact',
      'click',
      'answer:allow',
      'steps:6',
      'idle',
      'request:5',
      'first-proof',
      'observe:control-banner',
      'queue:2',
      'send',
      'steps:8',
      'idle',
      'no-banner',
      'request:7',
      'second-proof',
      'observed',
      'view',
    ])
    expect(declinedRow.assertions).toEqual(['checked', 'count:0', 'visible', 'visible'])
  })

  it('answers each turn in the step that calls the tool, in the group that the provider draws', async () => {
    const { context: remembered, queued } = scriptedContext(() => ({}), [4, 5])
    await exerciseRememberedAllow(remembered, {
      scopeGroup: 'Allow as',
      scope: 'Command rule',
      answerStep: 'same-step',
      firstCall,
      secondCall,
      firstProof: () => { flow.events.push('first-proof') },
      secondProof: () => { flow.events.push('second-proof') },
    })
    expect(queued).toEqual([
      [{ text: expect.stringContaining('The first call ran.'), toolCalls: [firstCall] }],
      [{ text: expect.stringContaining('The covered call ran.'), toolCalls: [secondCall] }],
    ])
    expect(flow.events).toEqual([
      'queue:1',
      'send',
      'steps:5',
      'banner',
      'radiogroup:Allow as',
      'radio:Command rule:exact',
      'click',
      'answer:allow',
      'steps:5',
      'idle',
      'request:4',
      'first-proof',
      'observe:control-banner',
      'queue:1',
      'send',
      'steps:6',
      'idle',
      'no-banner',
      'request:5',
      'second-proof',
      'observed',
    ])
  })

  it('restores each rule file after the scenario, and removes one that the scenario created', async () => {
    const kept = join(native.directory, 'kept.rules')
    const created = join(native.directory, 'created.rules')
    writeFileSync(kept, 'original rule\n')
    const { context: remembered } = scriptedContext(() => ({}), [4, 6])
    await exerciseRememberedAllow(remembered, {
      scope: 'Always',
      firstCall,
      secondCall,
      firstProof: () => {
        writeFileSync(kept, 'kept rule\n')
        writeFileSync(created, 'created rule\n')
      },
      secondProof: () => {},
      ruleFiles: [kept, created],
    })
    expect(readFileSync(kept, 'utf8')).toBe('original rule\n')
    expect(existsSync(created)).toBe(false)
  })

  it('restores each rule file when the covered call fails its proof', async () => {
    const kept = join(native.directory, 'kept.rules')
    writeFileSync(kept, 'original rule\n')
    const { context: remembered } = scriptedContext(() => ({}), [4, 6])
    await expect(exerciseRememberedAllow(remembered, {
      scope: 'Always',
      firstCall,
      secondCall,
      firstProof: () => writeFileSync(kept, 'kept rule\n'),
      secondProof: () => {
        throw new Error('the covered call raised a request')
      },
      ruleFiles: [kept],
    })).rejects.toThrow('the covered call raised a request')
    expect(readFileSync(kept, 'utf8')).toBe('original rule\n')
  })

  it('refuses to restore through a link that the native scenario creates', async () => {
    const outside = mkdtempSync(join(scratchRoot, 'outside-rule-restore-test-'))
    const target = join(outside, 'operator.rules')
    const kept = join(native.directory, 'kept.rules')
    writeFileSync(target, 'outside rule\n')
    writeFileSync(kept, 'original rule\n')
    const { context: remembered } = scriptedContext(() => ({}), [4, 6])
    try {
      await expect(exerciseRememberedAllow(remembered, {
        scope: 'Always',
        firstCall,
        secondCall,
        firstProof: () => {
          rmSync(kept)
          symlinkSync(target, kept)
        },
        secondProof: () => {},
        ruleFiles: [kept],
      })).rejects.toThrow('must not be a symbolic link')
      expect(readFileSync(target, 'utf8')).toBe('outside rule\n')
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })
})

describe('exerciseNativePermissionWrite', () => {
  it.runIf(existsSync('/bin/sh'))('allows the actual write, proves its native result, and then runs the view proof', async () => {
    const queued: Array<{ toolCalls?: Array<{ id: string, arguments: { command: string } }> }> = []
    let output = ''
    flow.onAnswer = (decision) => {
      const call = queued[0]?.toolCalls?.[0]
      if (decision === 'allow' && call)
        output = execFileSync('/bin/sh', ['-c', call.arguments.command], { cwd: native.directory, encoding: 'utf8' })
    }
    const modelScript = {
      prompt: (text: string) => text,
      queue: async (...steps: typeof queued) => {
        queued.push(...steps)
        return 4
      },
      waitForSteps: async () => {},
      requestAt: async () => result(queued[0]?.toolCalls?.[0]?.id ?? '', output),
    } as unknown as ModelScript
    const viewProof = vi.fn(async () => {
      // The view proof runs after the native proof, so the write already holds its calculated bytes.
      const written = readdirSync(native.directory).filter(name => name.startsWith('native-permission-'))
      expect(written).toHaveLength(1)
      expect(readFileSync(join(native.directory, written[0]!), 'utf8')).toMatch(/^AFTER[0-9a-f]{32}42$/)
    })
    const page = Object.assign({} as Page, { locator: () => ({ declinedRowProbe: true }) })
    await exerciseNativePermissionWrite({ provider: context.provider, providerAgent: context.providerAgent, workspaceId: context.workspaceId, leapmuxServer: context.leapmuxServer, page, modelScript }, { viewProof })
    expect(viewProof).toHaveBeenCalledOnce()
  })
})
