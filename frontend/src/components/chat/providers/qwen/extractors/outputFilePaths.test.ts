import type { ProviderRowOptions } from '~/test-support/toolCallFixture'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import { typedResult } from '../../../model/toolCall'
import { input } from '../../testUtils'
import { qwenOutputNoticePath } from './outputFilePaths'
import '~/components/chat/providers'

const CALL_ID = 'native-path-call'
const FILE_PATH = '/native/runtime/run_shell_command_123456abcdef.output'
const INLINE = 'The provider kept this inline preview.'

function shellResult(paths: unknown, preview = INLINE): Record<string, unknown> {
  return {
    type: 'shell_result',
    version: 1,
    directory: '/native/workspace',
    exitCode: 0,
    signal: null,
    pid: null,
    outcome: 'completed',
    output: preview,
    text: preview,
    error: null,
    truncated: true,
    outputFiles: paths,
  }
}

function resultFrame(patch: Record<string, unknown> = {}): Record<string, unknown> {
  return Object.fromEntries(Object.entries({
    sessionUpdate: 'tool_call_update',
    toolCallId: CALL_ID,
    status: 'completed',
    kind: 'execute',
    rawInput: { command: 'printf native', description: 'Show native output' },
    content: [{ type: 'content', content: { type: 'text', text: INLINE } }],
    rawOutput: shellResult([FILE_PATH]),
    _meta: { toolName: 'run_shell_command', provenance: 'builtin' },
    ...patch,
  }).filter(([, value]) => value !== undefined))
}

function pathsFrom(frame: Record<string, unknown>, options: ProviderRowOptions = {}): readonly string[] {
  return providerToolCall(AgentProvider.QWEN_CODE, frame, options)?.outputFilePaths ?? []
}

function notice(path: string): string {
  return `Tool output was too large and has been truncated.\nThe full output has been saved to: ${path}\nOriginal native preview.`
}

describe('qwenOutputFilePaths', () => {
  it('reads a root path through the registered row extractor and keeps the inline preview', () => {
    const frame = resultFrame()
    const before = structuredClone(frame)
    const call = providerToolCall(AgentProvider.QWEN_CODE, frame)
    expect(call?.outputFilePaths).toEqual([FILE_PATH])
    if (!call || call.kind !== 'execute')
      throw new Error('The native Qwen shell result requires an execute row.')
    expect(typedResult(call)?.commands[0]?.output).toBe(INLINE)
    expect(typedResult(call)?.commands[0]?.exitCode).toBe(0)
    expect(providerToolMeta(AgentProvider.QWEN_CODE, frame)?.copyableContent()).toBe(INLINE)
    expect(frame).toEqual(before)
  })

  it('reads foreground child paths without changing native parent metadata', () => {
    const frame = resultFrame({ _meta: { toolName: 'run_shell_command', provenance: 'subagent', parentToolCallId: 'native-spawn' } })
    const before = structuredClone(frame)
    expect(pathsFrom(frame)).toEqual([FILE_PATH])
    expect(frame).toEqual(before)
  })

  it('keeps the native path order and removes exact duplicates', () => {
    expect(pathsFrom(resultFrame({ rawOutput: shellResult([FILE_PATH, '/native/second.output', FILE_PATH]) }))).toEqual([FILE_PATH, '/native/second.output'])
  })

  it.each([
    ['POSIX', '/native/file.output'],
    ['Windows', 'C:\\native\\file.output'],
    ['UNC', '\\\\server\\share\\file.output'],
    ['Unicode', '/native/한글😀.output'],
    ['leading filename space', '/native/ file.output'],
    ['trailing space', '/native/file.output '],
    ['internal space', '/native/file output.output'],
  ])('preserves the exact path spelling for %s', (_kind, path) => {
    expect(pathsFrom(resultFrame({ rawOutput: shellResult([path]) }))).toEqual([path])
  })

  it.each(['', '0', 'false'])('keeps an inline preview of %j', (preview) => {
    const frame = resultFrame({ rawOutput: shellResult([FILE_PATH], preview) })
    const call = providerToolCall(AgentProvider.QWEN_CODE, frame)
    expect(call?.outputFilePaths).toEqual([FILE_PATH])
    if (!call || call.kind !== 'execute')
      throw new Error('The native Qwen preview requires an execute row.')
    expect(typedResult(call)?.commands[0]?.output).toBe(preview)
  })

  it('keeps a path and original preview from a native failed result', () => {
    const native = resultFrame({ status: 'failed' })
    expect(pathsFrom(native)).toEqual([FILE_PATH])
    expect(providerToolMeta(AgentProvider.QWEN_CODE, native)?.copyableContent()).toBe(INLINE)
  })

  it.each(['cancelled', 'timed_out'])('keeps a native %s shell outcome under failed result status', (outcome) => {
    const native = resultFrame({ status: 'failed', rawOutput: { ...shellResult([FILE_PATH]), outcome } })
    const before = structuredClone(native)
    expect(pathsFrom(native)).toEqual([FILE_PATH])
    const pathsFor = outputFilePathFixture(AgentProvider.QWEN_CODE, resultFrame())
    expect(pathsFor(native)).toEqual([FILE_PATH])
    expect(providerToolMeta(AgentProvider.QWEN_CODE, native)?.copyableContent()).toBe(INLINE)
    expect(native).toEqual(before)
  })

  it('keeps a genuine completed native pointer after an interrupted retained completion', () => {
    const native = resultFrame()
    const options = { completion: MessageCompletion.INTERRUPTED }
    expect(pathsFrom(native, options)).toEqual([FILE_PATH])
    expect(providerToolMeta(AgentProvider.QWEN_CODE, native, options)?.copyableContent()).toBe(INLINE)
  })

  it('reads a background notice without a structured shell result', () => {
    const preview = notice(FILE_PATH)
    const frame = resultFrame({ rawOutput: undefined, _meta: { toolName: 'run_shell_command' }, content: [{ type: 'content', content: { type: 'text', text: preview } }] })
    const before = structuredClone(frame)
    expect(pathsFrom(frame)).toEqual([FILE_PATH])
    expect(providerToolMeta(AgentProvider.QWEN_CODE, frame)?.copyableContent()).toBe(preview)
    expect(frame).toEqual(before)
  })

  it('ignores a path-shaped line in the notice preview', () => {
    const preview = `${notice(FILE_PATH)}\nThe full output has been saved to: /native/echoed.output`
    expect(pathsFrom(resultFrame({ rawOutput: undefined, content: [{ type: 'content', content: { type: 'text', text: preview } }] }))).toEqual([FILE_PATH])
  })

  it('uses matching opener metadata when the result omits the tool name', () => {
    const opener = input({ sessionUpdate: 'tool_call', toolCallId: CALL_ID, status: 'pending', kind: 'execute', rawInput: { command: 'printf native' }, _meta: { toolName: 'run_shell_command' } }, null, AgentProvider.QWEN_CODE)
    expect(pathsFrom(resultFrame({ _meta: undefined }), { request: opener })).toEqual([FILE_PATH])
  })

  it.each([
    { label: 'missing result type', rawOutput: { outputFiles: [FILE_PATH] } },
    { label: 'foreign result type', rawOutput: { ...shellResult([FILE_PATH]), type: 'foreign_result' } },
    { label: 'unsupported version', rawOutput: { ...shellResult([FILE_PATH]), version: 2 } },
    { label: 'string version', rawOutput: { ...shellResult([FILE_PATH]), version: '1' } },
    { label: 'missing path array', rawOutput: shellResult(undefined) },
    { label: 'non-array paths', rawOutput: shellResult(FILE_PATH) },
    { label: 'mixed path types', rawOutput: shellResult([FILE_PATH, 0]) },
    { label: 'null path', rawOutput: shellResult([null]) },
    { label: 'empty path', rawOutput: shellResult(['']) },
    { label: 'NUL path', rawOutput: shellResult(['/native/invalid\0.output']) },
  ])('refuses $label', ({ rawOutput }) => {
    expect(pathsFrom(resultFrame({ rawOutput }))).toEqual([])
  })

  it.each([
    { label: 'missing call ID', patch: { toolCallId: undefined } },
    { label: 'other native tool', patch: { _meta: { toolName: 'read_file' } } },
    { label: 'MCP tool', patch: { _meta: { toolName: 'mcp__native__tool' } } },
  ])('refuses $label identity', ({ patch }) => {
    expect(pathsFrom(resultFrame(patch))).toEqual([])
  })

  it('refuses a foreign opener', () => {
    const opener = input({ sessionUpdate: 'tool_call', toolCallId: 'foreign-call', status: 'pending', kind: 'execute', _meta: { toolName: 'run_shell_command' } }, null, AgentProvider.QWEN_CODE)
    expect(pathsFrom(resultFrame({ _meta: undefined }), { request: opener })).toEqual([])
  })

  it('reads no output fields from the matching request', () => {
    const opener = input({ sessionUpdate: 'tool_call', toolCallId: CALL_ID, status: 'pending', kind: 'execute', rawOutput: shellResult([FILE_PATH]), _meta: { toolName: 'run_shell_command' } }, null, AgentProvider.QWEN_CODE)
    expect(pathsFrom(resultFrame({ rawOutput: undefined }), { request: opener })).toEqual([])
  })

  it.each(['request', 'none'] as const)('adds no paths to a %s row', (role) => {
    expect(pathsFrom(resultFrame(), { role })).toEqual([])
  })

  it('reads no path from an outer or removed recovery supplement', () => {
    const frame = resultFrame({ rawOutput: undefined })
    const supplementalContent = {
      sessionUpdate: frame.sessionUpdate,
      toolCallId: frame.toolCallId,
      status: frame.status,
      outputFiles: [FILE_PATH],
      qwenOutputFile: { path: FILE_PATH, text: 'Removed recovery content' },
      rawOutput: { qwenToolRecord: { outputFiles: [FILE_PATH] } },
    }
    expect(pathsFrom(frame, { supplementalContent })).toEqual([])
  })

  it.each([
    { label: 'embedded notice', text: `Original output.\n${notice(FILE_PATH)}` },
    { label: 'missing native prefix', text: `The full output has been saved to: ${FILE_PATH}` },
    { label: 'empty native path', text: notice('') },
    { label: 'NUL native path', text: notice('/native/invalid\0.output') },
  ])('refuses a background $label', ({ text }) => {
    expect(pathsFrom(resultFrame({ rawOutput: undefined, content: [{ type: 'content', content: { type: 'text', text } }] }))).toEqual([])
  })

  it('refuses two native notice blocks', () => {
    const content = [FILE_PATH, '/native/other.output'].map(path => ({ type: 'content', content: { type: 'text', text: notice(path) } }))
    expect(pathsFrom(resultFrame({ rawOutput: undefined, content }))).toEqual([])
  })

  it('keeps a malformed structured result separate from background notice parsing', () => {
    expect(pathsFrom(resultFrame({ rawOutput: { type: 'foreign_result' }, content: [{ type: 'content', content: { type: 'text', text: notice(FILE_PATH) } }] }))).toEqual([])
  })
})

describe('native filesystem and completed-result boundaries', () => {
  it.each(['https://example.com/output', 'file:///native/output', 'zcode-artifact://session/opaque', ' ', '\t', 'relative/output', ' /native/leading-relative.output'])('refuses a non-native filesystem pointer in all native routes: %j', (path) => {
    const pathsFor = outputFilePathFixture(AgentProvider.QWEN_CODE, resultFrame())
    const structured = resultFrame({ rawOutput: shellResult([path]) })
    const background = resultFrame({ rawOutput: undefined, content: [{ type: 'content', content: { type: 'text', text: notice(path) } }] })
    expect(pathsFrom(structured)).toEqual([])
    expect(pathsFor(structured)).toEqual([])
    expect(pathsFrom(background)).toEqual([])
    expect(pathsFor(background)).toEqual([])
    expect(qwenOutputNoticePath(notice(path))).toBeUndefined()
  })

  it.each(['pending', 'in_progress', 'cancelled', 'timed_out', '', 'foreign', undefined, null])('refuses a non-final native result status in both routes: %j', (status) => {
    const pathsFor = outputFilePathFixture(AgentProvider.QWEN_CODE, resultFrame())
    const structured = resultFrame({ status })
    const background = resultFrame({ status, rawOutput: undefined, content: [{ type: 'content', content: { type: 'text', text: notice(FILE_PATH) } }] })
    expect(pathsFrom(structured)).toEqual([])
    expect(pathsFor(structured)).toEqual([])
    expect(pathsFrom(background)).toEqual([])
    expect(pathsFor(background)).toEqual([])
  })

  it.each(['/native/ file.output', '/native/internal space.output', '/native/trailing.output ', String.raw`C:\native files\file.output`, String.raw`\\server\share\file.output`])('keeps native absolute roots and filename spaces in both routes: %j', (path) => {
    const pathsFor = outputFilePathFixture(AgentProvider.QWEN_CODE, resultFrame())
    const structured = resultFrame({ rawOutput: shellResult([path]) })
    const background = resultFrame({ rawOutput: undefined, content: [{ type: 'content', content: { type: 'text', text: notice(path) } }] })
    expect(pathsFrom(structured)).toEqual([path])
    expect(pathsFor(structured)).toEqual([path])
    expect(pathsFrom(background)).toEqual([path])
    expect(pathsFor(background)).toEqual([path])
  })
})
