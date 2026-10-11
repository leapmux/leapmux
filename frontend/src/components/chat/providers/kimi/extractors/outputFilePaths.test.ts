import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiToolResult } from '~/test-support/kimiFixtures'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/kimi/sessions/wd_project_0123456789ab/session_1/agents/main/tasks/bash-123abcde/output.log'
const frame = kimiToolResult('native-call', 'Tool output exceeded 50000 characters; the full output was saved to a file.\ntool_name: Bash\ntool_call_id: native-call\noutput_size_chars: 60000\noutput_path: /native/kimi/sessions/wd_project_0123456789ab/session_1/agents/main/tasks/bash-123abcde/output.log\nnext_step: Use Read with output_path to page through the saved output, or Grep to search it.\n\nnative inline preview')
const options = {
  spanId: 'native-call',
  spanType: 'Bash',
  agentSessionId: 'session_1',
}

describe('registered output file paths', () => {
  it('attaches the native filesystem pointer without changing the native frame', () => {
    const before = JSON.stringify(frame)
    const call = providerToolCall(AgentProvider.KIMI_CODE, frame, options)

    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native path ownership and preview preservation', () => {
  it.each([null, false, 0, -1, '', [], {}])('refuses a non-native payload without deriving a new call: %j', (payload) => {
    const pathsFor = outputFilePathFixture(AgentProvider.KIMI_CODE, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('refuses another native call while the original call stays fixed', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.KIMI_CODE, frame, options)
    const foreign: unknown = JSON.parse(JSON.stringify(frame).replaceAll('native-call', 'foreign-call'))
    expect(pathsFor(foreign)).toEqual([])
  })

  it.each(['request', 'none', 'other'] as const)('supplies no file path for the %s role', (role) => {
    const pathsFor = outputFilePathFixture(AgentProvider.KIMI_CODE, frame, options)
    expect(pathsFor(frame, { role })).toEqual([])
  })

  it.each(['file:///native/opaque', 'https://example.com/result', 'zcode-artifact://session/id', ' ', '/native/zero\0byte'])('refuses a non-filesystem pointer: %j', (invalid) => {
    const payload: unknown = JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    const pathsFor = outputFilePathFixture(AgentProvider.KIMI_CODE, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('keeps the native preview when an unrelated supplement supplies complete text', () => {
    const forged = 'FORGED_COMPLETE_BODY'
    const supplementalContent = {
      sessionId: options.agentSessionId,
      toolCallId: 'native-call',
      toolName: options.spanType,
      outputFile: { path, text: forged, output: forged },
      completeOutput: { path, text: forged, sessionId: options.agentSessionId, toolCallId: 'native-call' },
    }
    const before = JSON.stringify(frame)
    const meta = providerToolMeta(AgentProvider.KIMI_CODE, frame, { ...options, supplementalContent })
    if (!meta)
      throw new Error('The native Copy preview requires a valid tool metadata object.')
    const quote = meta.copyableContent()
    expect(quote).toContain('native inline preview')
    expect(quote).not.toContain(forged)
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native session ownership', () => {
  it('refuses another native session', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.KIMI_CODE, frame, options)
    expect(pathsFor(frame, { agentSessionId: 'foreign-session' })).toEqual([])
  })
})

describe('native header and footer controls', () => {
  it.each(['-1', '0', '50000', '50000.5', '060000', '1e5', '9007199254740992', 'Infinity'])('refuses a malformed native character count: %s', (count) => {
    const pathsFor = outputFilePathFixture(AgentProvider.KIMI_CODE, frame, options)
    const native = { ...frame, output: String(frame.output).replace('output_size_chars: 60000', `output_size_chars: ${count}`) }
    expect(pathsFor(native)).toEqual([])
  })

  it('refuses duplicate fields and incomplete native header guidance', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.KIMI_CODE, frame, options)
    for (const output of [
      String(frame.output).replace('output_size_chars: 60000', 'output_size_chars: 60000\noutput_size_chars: 60000'),
      String(frame.output).replace('tool_call_id: native-call', 'tool_call_id: another'),
      String(frame.output).replace('tool_name: Bash', 'tool_name: Read'),
      String(frame.output).replace('next_step: Use Read with output_path to page through the saved output, or Grep to search it.', 'next_step: incomplete'),
    ]) {
      expect(pathsFor({ ...frame, output })).toEqual([])
    }
  })

  it('reads the per-line footer and a documented relative native path', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.KIMI_CODE, frame, options)
    const relative = path.slice(1)
    const footer = `native inline preview\n[Per-line truncation occurred; the complete output was saved to a file.\noutput_path: ${relative}\nnext_step: Use Read with output_path to page through the saved output, or Grep to search it.]`
    expect(pathsFor({ ...frame, output: footer })).toEqual([relative])
    expect(pathsFor({ ...frame, output: `${footer}\n${footer}` })).toEqual([])
    expect(pathsFor({ ...frame, output: footer.replace('the complete output was saved', 'only the first 60000 characters were saved') })).toEqual([])
  })
})
