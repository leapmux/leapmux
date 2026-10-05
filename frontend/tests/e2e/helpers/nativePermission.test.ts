import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision, expectDeclinedToolRow } from './nativePermission'
import { createOutputGate } from './outputGate'

const native = vi.hoisted(() => ({ directory: '', currentAgent: vi.fn() }))
const declinedRow = vi.hoisted(() => ({ assertions: [] as string[] }))
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
        }
      : original.expect(value),
  }
})
vi.mock('./providerToolCalls', () => ({ bashToolCall: (_provider: AgentProvider, id: string, command: string) => ({ id, name: 'unit-native-shell', arguments: { command } }) }))

const scratchRoot = resolve(process.cwd(), '../.tmp')
const context: ManagedNativeScenarioContext = {
  provider: AgentProvider.CLAUDE_CODE,
  workspaceId: 'native-permission-unit',
  leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
  get page(): Page { throw new Error('The native permission plan must not access the browser.') },
  get modelScript(): ModelScript { throw new Error('The native permission plan must not access the model.') },
}

beforeEach(() => {
  vi.clearAllMocks()
  declinedRow.assertions.length = 0
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

describe('exerciseNativePermissionDecision', () => {
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
