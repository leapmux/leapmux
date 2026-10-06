import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { GatedOutput, OutputGate } from './outputGate'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { allowNativeOperation, createNativePermissionFileWrite, exerciseAllowThenFeedbackRejection, exerciseNativePermissionDecision, expectDeclinedToolRow } from './nativePermission'
import { createOutputGate } from './outputGate'

const native = vi.hoisted(() => ({ directory: '', currentAgent: vi.fn() }))
const declinedRow = vi.hoisted(() => ({ assertions: [] as string[] }))
/** The browser steps of a decision flow, in order. */
const flow = vi.hoisted(() => ({ events: [] as string[], onAnswer: undefined as ((decision: string) => void) | undefined }))
vi.mock('./ui', async (importOriginal) => {
  const original = await importOriginal<typeof import('./ui')>()
  const probe = { declinedRowProbe: true }
  // A filtered bubble list is itself a fake row, so a check with or without `first()` reaches the recording `expect`.
  const bubbles: { declinedRowProbe: true, filter: () => unknown, first: () => unknown } = { declinedRowProbe: true, filter: () => bubbles, first: () => probe }
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
    enterControlFeedback: async (_page: unknown, text: string) => { flow.events.push(`feedback:${text}`) },
    waitForAgentIdle: async () => { flow.events.push('idle') },
    assistantBubbles: () => bubbles,
    messageBubbles: () => bubbles,
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
    // The fake result row records each assertion that the helper makes. Every other value reaches the real `expect`.
    expect: (value: unknown) => typeof value === 'object' && value !== null && 'declinedRowProbe' in value
      ? {
          toHaveCount: async (count: number) => { declinedRow.assertions.push(`count:${count}`) },
          toHaveAttribute: async (name: string, text: string) => { declinedRow.assertions.push(`attribute:${name}=${text}`) },
          toContainText: async (text: string) => { declinedRow.assertions.push(`text:${text}`) },
          toBeVisible: async () => { declinedRow.assertions.push('visible') },
        }
      : original.expect(value),
  }
})
vi.mock('./providerToolCalls', () => ({ bashToolCall: (_provider: AgentProvider, id: string, command: string) => ({ id, name: 'unit-native-shell', arguments: { command } }) }))

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
    await expect(createNativePermissionFileWrite(context, { fileName: 'exists.txt', callId: 'existing-native', outputPrefix: 'EXISTS' })).rejects.toThrow()
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
      `feedback:${reason}`,
      'press:Meta+Enter',
      'steps:7',
      'idle',
      'request:6',
    ])
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
    await expect(exerciseAllowThenFeedbackRejection(feedbackContext({ messages: [] }), { workingDir: native.directory })).rejects.toThrow()
  })

  it.each(['relative/dir', '/path with space', '/path/$(touch marker)', '/path/\'quote\''])('refuses the working directory %j before it touches the model', async (workingDir) => {
    await expect(exerciseAllowThenFeedbackRejection(context, { workingDir })).rejects.toThrow('needs no shell quoting')
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
