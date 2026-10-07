import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import { typedResult } from '../../../model/toolCall'
import { acpToolCall } from '../../acp/extractors/toolCall'
import { diracToolCallAdapter } from './toolCall'

import '../plugin'

const outputFileCallID = '1790904331507-2'
const outputFileSessionID = 'native-session'
const outputFilePath = '/private/tmp/dirac/large-output-1790904331563-6032d56.log'
const outputFileExcerpt = `Command executed successfully (exit code 0).\nOutput:\nnative first42\n... [Output truncated to 10.0 KB to avoid context flooding (7.2 KB truncated). Use more specific commands if you need to see more output.] ...\nnative tail42\nFull output saved to: ${outputFilePath}`
const outputFilePreview = `native first42\n... [Output truncated to 10.0 KB to avoid context flooding (7.2 KB truncated). Use more specific commands if you need to see more output.] ...\nnative tail42\nFull output saved to: ${outputFilePath}`
const outputFileComplete = `native first42\n\n${'native middle77 文\n'.repeat(1000)}native tail42\n`

describe('diracToolCallAdapter', () => {
  it('keeps the native log path and output preview in Copy without the status preamble', () => {
    const native = {
      sessionUpdate: 'tool_call_update',
      toolCallId: outputFileCallID,
      status: 'completed',
      name: 'execute_command',
      kind: 'execute',
      title: 'Executed: Node script',
      rawInput: { tool: 'execute_command', command: 'node native-script.js', language: 'node', displayName: 'Node script' },
      rawOutput: { output: outputFileExcerpt, userRejected: false, exitCode: 0, signal: null },
    }
    const supplementalContent = {
      sessionUpdate: 'tool_call_update',
      toolCallId: outputFileCallID,
      status: 'completed',
      outputFile: { sessionId: outputFileSessionID, toolCallId: outputFileCallID, path: outputFilePath, text: outputFileComplete },
    }
    const options = { spanId: outputFileCallID, spanType: 'execute_command', agentSessionId: outputFileSessionID, supplementalContent }
    const before = JSON.stringify({ native, supplementalContent })
    const call = providerToolCall(AgentProvider.DIRAC, native, options)
    expect(call?.kind).toBe('execute')
    if (!call || call.kind !== 'execute')
      throw new Error('The native Dirac command produced another tool kind.')
    expect(typedResult(call)?.commands[0]?.output).toBe(outputFilePreview)
    expect(typedResult(call)?.commands[0]?.output).not.toContain('native middle77')
    expect(call.outputFilePaths).toEqual([outputFilePath])
    const meta = providerToolMeta(AgentProvider.DIRAC, native, options)
    expect(meta?.copyableContent()).toBe(outputFilePreview)
    expect(meta?.copyableContent()).not.toContain('native middle77')
    expect(meta?.hasCopyable).toBe(true)
    expect(JSON.stringify({ native, supplementalContent })).toBe(before)
  })

  it('flattens an edit_file files[].edits[] call into one file change', () => {
    // Dirac's `edit_file` states its target as `files: [{path, edits: [...]}]`.
    // Each substitution identifies its line with an ANCHOR§CONTENT coordinate.
    // The adapter flattens the first file for the shared builder.
    // It takes the old text from the anchor's content.
    const call = acpToolCall(
      {
        sessionUpdate: 'tool_call',
        toolCallId: 'dirac-edit',
        status: 'pending',
        title: 'Edit note.txt',
        kind: 'edit',
        rawInput: {
          tool: 'edit_file',
          files: [{
            path: '/w/note.txt',
            edits: [{ edit_type: 'replace', anchor: 'Maintenance§dirac-before', end_anchor: 'Maintenance§dirac-before', text: 'dirac-after' }],
          }],
        },
      },
      diracToolCallAdapter,
      undefined,
    )
    expect(call.kind).toBe('edit')
    if (call.kind !== 'edit')
      return
    const [change] = call.request.changes
    expect(change?.filePath).toBe('/w/note.txt')
    expect(change?.oldStr).toBe('dirac-before')
    expect(change?.newStr).toBe('dirac-after')
  })

  it('takes a content diff when the call states its change that way', () => {
    const call = acpToolCall(
      {
        sessionUpdate: 'tool_call',
        toolCallId: 'dirac-edit',
        status: 'in_progress',
        title: 'Edit note.txt',
        kind: 'edit',
        content: [{ type: 'diff', path: '/w/note.txt', oldText: 'dirac-before\n', newText: 'dirac-after\n' }],
      },
      diracToolCallAdapter,
      undefined,
    )
    expect(call.kind).toBe('edit')
    if (call.kind !== 'edit')
      return
    const [change] = call.request.changes
    expect(change?.filePath).toBe('/w/note.txt')
    expect(change?.oldStr).toBe('dirac-before\n')
    expect(change?.newStr).toBe('dirac-after\n')
  })
})

const nativeExitOptions = {
  spanId: '1790904324986-4',
  spanType: 'execute_command',
  agentSessionId: 'a605fc24-fe65-41ff-98f2-06b9a993b627',
}

function nativeExitFrame(raw: Record<string, unknown>, status = 'completed') {
  return {
    sessionUpdate: 'tool_call_update',
    toolCallId: nativeExitOptions.spanId,
    name: 'execute_command',
    kind: 'execute',
    title: 'Executed: Node script',
    status,
    rawInput: { command: 'node native-script.js', displayName: 'Node script', language: 'node' },
    rawOutput: { output: 'Original native preview.', userRejected: false, ...raw },
  }
}

describe('registered native Dirac command exit', () => {
  it('keeps the reported path and native preview after reading the native zero exit', () => {
    const native = nativeExitFrame({ output: outputFileExcerpt, exitCode: 0, signal: null })
    const call = providerToolCall(AgentProvider.DIRAC, native, nativeExitOptions)
    if (!call || call.kind !== 'execute')
      throw new Error('The native path fixture requires a command call.')
    expect(call.outputFilePaths).toEqual([outputFilePath])
    expect(typedResult(call)?.commands[0]?.exitCode).toBe(0)
    expect(typedResult(call)?.commands[0]?.output).toBe(outputFilePreview)
    expect(providerToolMeta(AgentProvider.DIRAC, native, nativeExitOptions)?.copyableContent()).toBe(outputFilePreview)
  })

  it('preserves a native exit beside an interrupted retained completion', () => {
    const native = nativeExitFrame({ exitCode: 7, signal: null })
    const options = { ...nativeExitOptions, completion: MessageCompletion.INTERRUPTED }
    const call = providerToolCall(AgentProvider.DIRAC, native, options)
    if (!call || call.kind !== 'execute')
      throw new Error('The interrupted native fixture requires a command call.')
    expect(call.status).toBe('cancelled')
    expect(typedResult(call)?.commands[0]?.exitCode).toBe(7)
    expect(typedResult(call)?.commands[0]?.output).toBe('Original native preview.')
  })

  it.each([0, 7, -7, Number.MAX_SAFE_INTEGER])('preserves the native signed exit code %s without changing output or Copy', (exitCode) => {
    const native = nativeExitFrame({ exitCode, signal: null }, exitCode === 0 ? 'completed' : 'failed')
    const before = structuredClone(native)
    const call = providerToolCall(AgentProvider.DIRAC, native, nativeExitOptions)
    expect(call?.kind).toBe('execute')
    if (!call || call.kind !== 'execute')
      throw new Error('The native exit fixture requires its registered command call.')
    const result = typedResult(call)
    expect(result?.commands).toHaveLength(1)
    expect(result?.commands[0]?.exitCode).toBe(exitCode)
    expect(result?.commands[0]?.output).toBe('Original native preview.')
    expect(result?.unresolvedTerminals).toEqual([])
    expect(providerToolMeta(AgentProvider.DIRAC, native, nativeExitOptions)?.copyableContent()).toBe('Original native preview.')
    expect(native).toEqual(before)
  })

  it('preserves an explicit null native exit code', () => {
    const native = nativeExitFrame({ exitCode: null, signal: null })
    const call = providerToolCall(AgentProvider.DIRAC, native, nativeExitOptions)
    if (!call || call.kind !== 'execute')
      throw new Error('The native null exit fixture requires a command call.')
    expect(typedResult(call)?.commands[0]?.exitCode).toBeNull()
  })

  it.each([undefined, '', '7', false, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1, {}, []])('claims no numeric code for an absent or malformed native field: %j', (exitCode) => {
    const native = nativeExitFrame({ ...(exitCode === undefined ? {} : { exitCode }), signal: null })
    const before = structuredClone(native)
    const call = providerToolCall(AgentProvider.DIRAC, native, nativeExitOptions)
    if (!call || call.kind !== 'execute')
      throw new Error('The malformed native exit fixture requires a command call.')
    expect(typedResult(call)?.commands[0]?.exitCode ?? undefined).toBeUndefined()
    expect(typedResult(call)?.commands[0]?.output).toBe('Original native preview.')
    expect(native).toEqual(before)
  })

  it.each([null, 0])('preserves the native signal before a conflicting code: %j', (exitCode) => {
    const native = nativeExitFrame({ exitCode, signal: 'SIGTERM' }, 'failed')
    const call = providerToolCall(AgentProvider.DIRAC, native, nativeExitOptions)
    if (!call || call.kind !== 'execute')
      throw new Error('The native signal fixture requires a command call.')
    const command = typedResult(call)?.commands[0]
    expect(command?.signal).toBe('SIGTERM')
    expect(command?.exitCode).toBeUndefined()
    expect(command?.output).toBe('Original native preview.')
    expect(call.status).toBe('failed')
  })

  it.each([undefined, null, '', ' ', false, 7, [], {}])('claims no signal for an absent or malformed native field: %j', (signal) => {
    const native = nativeExitFrame({ exitCode: null, ...(signal === undefined ? {} : { signal }) })
    const call = providerToolCall(AgentProvider.DIRAC, native, nativeExitOptions)
    if (!call || call.kind !== 'execute')
      throw new Error('The malformed native signal fixture requires a command call.')
    expect(typedResult(call)?.commands[0]?.signal).toBeUndefined()
  })

  it('keeps a rejected native command without claiming its contradictory numeric exit', () => {
    const native = nativeExitFrame({ userRejected: true, exitCode: 0, signal: null }, 'failed')
    const before = structuredClone(native)
    const call = providerToolCall(AgentProvider.DIRAC, native, nativeExitOptions)
    if (!call || call.kind !== 'execute')
      throw new Error('The rejected native command fixture requires a command call.')
    expect(typedResult(call)?.commands[0]?.exitCode ?? undefined).toBeUndefined()
    expect(call.status).toBe('failed')
    expect(native).toEqual(before)
  })
})

describe('registered Dirac child title fallback', () => {
  it.each([
    { rawInput: { tool: 'use_subagents', prompt: 'Native child prompt.' }, title: 'Native child title', expected: 'Native child title' },
    { rawInput: { tool: 'use_subagents', task_title: '', prompt: 'Native child prompt.' }, title: 'Native child title', expected: 'Native child title' },
    { rawInput: { tool: 'use_subagents', task_title: 'Native task title', prompt: 'Native child prompt.' }, title: 'Outer title', expected: 'Native task title' },
    { rawInput: { tool: 'use_subagents', prompt: 'Native child prompt.' }, title: '', expected: 'use_subagents' },
  ])('preserves the native description fallback: %j', ({ rawInput, title, expected }) => {
    const native = { sessionUpdate: 'tool_call', toolCallId: 'native-child', kind: 'other', status: 'pending', title, rawInput }
    const before = structuredClone(native)
    const call = providerToolCall(AgentProvider.DIRAC, native, { spanId: 'native-child', spanType: 'use_subagents', role: 'request' })
    expect(call?.kind).toBe('agent')
    if (!call || call.kind !== 'agent')
      throw new Error('The native child fixture requires an agent call.')
    expect(call.request.description).toBe(expected)
    expect(call.request.prompt).toBe('Native child prompt.')
    expect(native).toEqual(before)
  })
})
