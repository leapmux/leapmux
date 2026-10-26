import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/tmp/fast-agent-output-abc123/output-1.log'
const text = `native head\n[Output truncated: showing first 8 bytes and last 8 bytes of 100 bytes (~4 of ~30 tokens); omitted 84 middle bytes. The complete output is available during this session at ${path}. Use read_text_file for selected line ranges or run a targeted search against that file; avoid reading the entire file unless necessary.]\nnative tail\nprocess_id: process-1\nprocess exit code was 0`
const frame = {
  sessionUpdate: 'tool_call_update',
  toolCallId: 'root-archive-call',
  status: 'completed',
  kind: 'other',
  title: 'bash',
  rawInput: { command: 'printf native-output' },
  _meta: { 'fast-agent/root-archive': { sessionId: 'native-session', historyFile: 'history_agent.json', timestamp: '2026-10-26T00:00:00Z', callId: 'model-call', toolName: 'bash' } },
  content: [{ type: 'content', content: { type: 'text', text } }],
  rawOutput: { content: [{ type: 'text', text }], isError: false, _meta: { 'fast-agent-shell-process-metadata': { exit_code: 0 } } },
}

describe('registered native Fast Agent output paths', () => {
  it('recovers the native command result and its stated file pointer', () => {
    const before = JSON.stringify(frame)
    const call = providerToolCall(AgentProvider.FAST_AGENT, frame, { spanId: frame.toolCallId, spanType: 'bash', agentSessionId: 'native-session' })
    expect(call).toMatchObject({ kind: 'execute', status: 'completed', request: { command: 'printf native-output' } })
    expect(call?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(frame)).toBe(before)
  })

  const options = { spanId: frame.toolCallId, spanType: 'bash', agentSessionId: 'native-session' }
  const pathsFor = () => outputFilePathFixture(AgentProvider.FAST_AGENT, frame, options)
  const withText = (value: string) => ({ ...frame, rawOutput: { ...frame.rawOutput, content: [{ type: 'text', text: value }] }, content: [{ type: 'content', content: { type: 'text', text: value } }] })

  it('preserves the native preview and its structured zero exit', () => {
    const call = providerToolCall(AgentProvider.FAST_AGENT, frame, options)
    expect(call).toMatchObject({ result: { commands: [expect.objectContaining({ output: text, exitCode: 0 })] } })
    const meta = providerToolMeta(AgentProvider.FAST_AGENT, frame, { ...options, supplementalContent: { fullOutput: 'unrelated body' } })
    expect(meta?.copyableContent()).toContain('native tail')
    expect(meta?.copyableContent()).not.toContain('unrelated body')
  })

  it('uses the structured failing exit and preserves interruption', () => {
    const failed = { ...frame, rawOutput: { ...frame.rawOutput, _meta: { 'fast-agent-shell-process-metadata': { exit_code: 7 } } } }
    expect(providerToolCall(AgentProvider.FAST_AGENT, failed, options)).toMatchObject({ status: 'failed', result: { commands: [expect.objectContaining({ exitCode: 7 })] } })
    expect(providerToolCall(AgentProvider.FAST_AGENT, failed, { ...options, completion: MessageCompletion.COMPLETE })?.status).toBe('failed')
    expect(providerToolCall(AgentProvider.FAST_AGENT, failed, { ...options, completion: MessageCompletion.INTERRUPTED })?.status).toBe('cancelled')
  })

  it('keeps distinct pointers in native order and removes duplicates', () => {
    const second = path.replace('output-1', 'output-2')
    expect(pathsFor()(withText([text, text.replaceAll(path, second), text].join('\n')))).toEqual([path, second])
  })

  it('reads the quota-limited native notice', () => {
    const partial = text.replace(`The complete output is available during this session at ${path}.`, `The first 32 bytes of the output are available during this session at ${path}; the temporary-file quota was reached.`)
    expect(pathsFor()(withText(partial))).toEqual([path])
    expect(pathsFor()(withText(partial.replace('first 32 bytes of the output', 'first 0 bytes of the output')))).toEqual([path])
  })

  it.each(['request', 'none', 'other'] as const)('refuses the %s row', (role) => {
    expect(pathsFor()(frame, { role })).toEqual([])
  })

  it.each(['pending', 'in_progress', 'cancelled'])('refuses the %s native status', (status) => {
    expect(pathsFor()({ ...frame, status })).toEqual([])
  })

  it.each([null, false, 0, -1, '', [], {}].map(payload => [payload]))('refuses malformed native input: %j', (payload) => {
    expect(pathsFor()(payload)).toEqual([])
  })

  it('refuses another call and missing or foreign archive provenance', () => {
    expect(pathsFor()({ ...frame, toolCallId: 'other' })).toEqual([])
    expect(pathsFor()(frame, { agentSessionId: 'another-native-session' })).toEqual([])
    for (const field of Object.keys(frame._meta['fast-agent/root-archive'])) {
      const archive: Record<string, unknown> = { ...frame._meta['fast-agent/root-archive'] }
      delete archive[field]
      expect(pathsFor()({ ...frame, _meta: { 'fast-agent/root-archive': archive } })).toEqual([])
    }
    expect(pathsFor()({ ...frame, _meta: { 'fast-agent/root-archive': { ...frame._meta['fast-agent/root-archive'], toolName: 'read_text_file' } } })).toEqual([])
  })

  it('retains a file pointer stated by the native process tool', () => {
    const process = { ...frame, title: 'process', rawInput: { process_id: 'process-1' }, _meta: { 'fast-agent/root-archive': { ...frame._meta['fast-agent/root-archive'], toolName: 'process' } } }
    expect(pathsFor()(process)).toEqual([path])
  })

  it.each(['relative/tmp/fast-agent-output-abc123/output-1.log', 'https://host/fast-agent-output-abc123/output-1.log', 'file:///tmp/fast-agent-output-abc123/output-1.log', '/tmp/fast-agent-output-abc123/output-1.log\0', '/tmp/fast-agent-output-abc123/../output-1.log', '/tmp/fast-agent-output-abc123/output-01.log'])('refuses an invalid file path: %j', (invalid) => {
    expect(pathsFor()(withText(text.replaceAll(path, invalid)))).toEqual([])
  })

  it.each(['/native/space here/fast-agent-output-abc123/output-1.log', 'C:\\native\\fast-agent-output-abc123\\output-1.log'])('preserves the native file path: %s', (valid) => {
    expect(pathsFor()(withText(text.replaceAll(path, valid)))).toEqual([valid])
  })

  it('refuses incomplete notices and prose that quotes the locator', () => {
    const notice = text.split('\n')[1]!
    for (const value of [path, `The complete output is available during this session at ${path}.`, notice.slice(0, -1), `prefix ${notice}`])
      expect(pathsFor()(withText(value))).toEqual([])
  })

  it('refuses the two impossible combinations of complete and quota-limited notices', () => {
    const quotaSuffix = `${path}; the temporary-file quota was reached. Use`
    const completeSuffix = `${path}. Use`
    const quota = text.replace('The complete output is available', 'The first 32 bytes of the output are available').replace(completeSuffix, quotaSuffix)
    expect(pathsFor()(withText(text.replace(completeSuffix, quotaSuffix)))).toEqual([])
    expect(pathsFor()(withText(quota.replace(quotaSuffix, completeSuffix)))).toEqual([])
  })

  it('keeps empty native output and a negative structured exit code', () => {
    const empty = { ...frame, content: [], rawOutput: { content: [], isError: true, _meta: { 'fast-agent-shell-process-metadata': { exit_code: -9 } } } }
    expect(providerToolCall(AgentProvider.FAST_AGENT, empty, options)).toMatchObject({ kind: 'execute', status: 'failed', result: { commands: [expect.objectContaining({ output: '', exitCode: -9 })] } })
  })
})
