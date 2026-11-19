import { describe, expect, it } from 'vitest'
import { KIMI_CONVERSATION_MEDIA, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiConversationToolRecord } from '~/test-support/kimiFixtures'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { input } from '../../testUtils'
import { kimiAgentRuns, kimiFileChanges, kimiSwarmRuns, kimiToolRow } from './toolCall'
import '../../index'

const CALL = 'call_1'

/** Form the public result and its paired request from the same native declaration. */
function resultCall(
  name: string,
  args: Record<string, unknown>,
  output: string,
  extra: { outcome?: 'completed' | 'failed', truncated?: boolean, media?: Record<string, unknown>[] } = {},
  display?: Record<string, unknown>,
) {
  const item = {
    item_id: CALL,
    name,
    input: args,
    display,
  }
  const outcome = extra.outcome ?? 'completed'
  const result = kimiConversationToolRecord('result', {
    ...item,
    status: 'ended',
    outcome,
    ...(outcome === 'failed'
      ? { error: output }
      : { output }),
    media: extra.media,
  })
  return providerToolCall(AgentProvider.KIMI_CODE, {
    ...result,
    ...(extra.truncated
      ? { output_truncated: true }
      : {}),
  }, {
    request: input(kimiConversationToolRecord('request', item), undefined, AgentProvider.KIMI_CODE),
    spanType: name,
  })
}

describe('kimiToolRow', () => {
  it('keeps a retained running result incomplete when the native item carried no output', () => {
    const empty = kimiConversationToolRecord('result', {
      name: KIMI_TOOL.Bash,
      input: { command: 'sleep 9' },
    })
    expect(kimiToolRow(empty, MessageCompletion.COMPLETE)).toMatchObject({
      result: null,
      retained: true,
      finished: true,
      lifecycle: { frameStatus: 'in_progress', resultFrameLanded: false },
    })
    expect(providerToolCall(AgentProvider.KIMI_CODE, empty, { completion: MessageCompletion.COMPLETE })?.status).toBe('incomplete')
  })

  it('renders exact native session media only from its scoped supplement', () => {
    const fileId = `f_${'a'.repeat(64)}`
    const data = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aY9sAAAAASUVORK5CYII='
    const frame = kimiConversationToolRecord('result', {
      item_id: 'call_1',
      name: 'ReadMediaFile',
      status: 'ended',
      outcome: 'completed',
      input: { path: 'original.png' },
      display: { path: '/work/original.png' },
      media: [
        { kind: 'image', file_id: fileId },
      ],
    })
    const receipt = {
      session_id: 'session_1',
      agent_id: 'main',
      turn_id: 't0',
      item_id: 'call_1',
      files: {
        [fileId]: { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'image/png', data },
      },
    }
    const before = structuredClone(frame)
    const call = providerToolCall(AgentProvider.KIMI_CODE, frame, {
      supplementalContent: { kimi_media: receipt },
      agentSessionId: 'session_1',
    })
    expect(call?.images).toMatchObject([
      { mimeType: 'image/png', data },
    ])
    expect(frame).toStrictEqual(before)
    const foreign = providerToolCall(AgentProvider.KIMI_CODE, frame, {
      supplementalContent: {
        kimi_media: { ...receipt, item_id: 'other' },
      },
      agentSessionId: 'session_1',
    })
    expect(foreign?.images).not.toMatchObject([
      { data },
    ])
  })

  it('preserves the native status and runtime role of each tool record', () => {
    const item = {
      item_id: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'printf ok' },
    }
    const request = kimiConversationToolRecord('request', item)
    for (const completion of [undefined, MessageCompletion.UNSPECIFIED, MessageCompletion.INTERRUPTED]) {
      const row = kimiToolRow(request, completion)
      expect(row).toMatchObject({
        toolCallId: CALL,
        toolName: KIMI_TOOL.Bash,
        args: item.input,
        result: null,
        finished: false,
        retained: false,
        lifecycle: {
          frameStatus: 'in_progress',
          retainedOutcome: null,
          rowFinal: false,
          resultFrameLanded: false,
        },
      })
    }
    const finished = kimiConversationToolRecord('result', {
      ...item,
      status: 'ended',
      outcome: 'completed',
      output: 'ok',
    })
    expect(kimiToolRow(finished, undefined)).toMatchObject({
      args: item.input,
      result: finished.item,
      finished: true,
      retained: false,
      lifecycle: {
        frameStatus: 'completed',
        providerOutcome: null,
        rowFinal: true,
        resultFrameLanded: true,
      },
    })
    const failed = kimiConversationToolRecord('result', {
      ...item,
      status: 'ended',
      outcome: 'failed',
      error: 'boom',
    })
    expect(kimiToolRow(failed, undefined)).toMatchObject({
      args: item.input,
      result: failed.item,
      finished: true,
      retained: false,
      lifecycle: {
        frameStatus: 'failed',
        providerOutcome: 'failed',
        rowFinal: true,
        resultFrameLanded: true,
      },
    })
    const interrupted = kimiConversationToolRecord('result', {
      ...item,
      status: 'ended',
      outcome: 'interrupted',
      error: 'stopped',
    })
    expect(kimiToolRow(interrupted, undefined)?.lifecycle).toMatchObject({
      frameStatus: 'cancelled',
      providerOutcome: 'interrupted',
      rowFinal: true,
      resultFrameLanded: true,
    })
  })

  it('reads the original command outcome only from the same public identity', () => {
    const item = {
      item_id: CALL,
      step_index: 1,
      name: KIMI_TOOL.Bash,
      input: { command: 'printf ok' },
      status: 'ended',
      outcome: 'completed',
      output: 'ok',
    }
    const result = kimiConversationToolRecord('result', item)
    const display = kimiConversationToolRecord('result', { ...item, output: 'recovered display' })
    expect(kimiToolRow(display, undefined, result)?.originalResult).toEqual(result.item)
    for (const foreign of [
      kimiConversationToolRecord('result', item, { agentId: 'child' }),
      kimiConversationToolRecord('result', item, { sessionId: 'session_other' }),
      kimiConversationToolRecord('result', item, { turnId: 't1' }),
      kimiConversationToolRecord('result', { ...item, step_index: 2 }),
      kimiConversationToolRecord('result', { ...item, item_id: 'other' }),
      kimiConversationToolRecord('request', item),
    ]) {
      expect(kimiToolRow(display, undefined, foreign)?.originalResult).toEqual(display.item)
    }
  })

  it('reads a complete result when its opening row is outside the window', () => {
    const row = kimiToolRow(kimiConversationToolRecord('result', {
      item_id: CALL,
      name: KIMI_TOOL.Read,
      input: {},
      status: 'ended',
      outcome: 'completed',
      output: 'ok',
    }), undefined)
    expect(row).toMatchObject({
      toolCallId: CALL,
      toolName: KIMI_TOOL.Read,
      args: {},
      display: undefined,
      finished: true,
      retained: false,
    })
  })

  it('rejects a public result without its native tool name', () => {
    expect(kimiToolRow(kimiConversationToolRecord('result', {
      item_id: CALL,
      input: {},
      status: 'ended',
      outcome: 'completed',
      output: 'ok',
    }), undefined)).toBeNull()
  })

  it.each([
    0,
    42,
    false,
    { text: 'x' },
    null,
    ['not native output'],
  ])('rejects non-string public output %j', (output) => {
    expect(kimiToolRow(kimiConversationToolRecord('result', {
      item_id: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'x' },
      output,
    }), MessageCompletion.INTERRUPTED)).toBeNull()
  })

  it('uses the local completion of a retained running result', () => {
    const result = kimiConversationToolRecord('result', {
      item_id: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'sleep 9' },
    })
    for (const [completion, outcome, status] of [
      [MessageCompletion.COMPLETE, 'succeeded', 'incomplete'],
      [MessageCompletion.ERROR, 'failed', 'failed'],
      [MessageCompletion.INTERRUPTED, 'interrupted', 'cancelled'],
    ] as const) {
      expect(kimiToolRow(result, completion)).toMatchObject({
        finished: true,
        retained: true,
        result: null,
        lifecycle: { retainedOutcome: outcome, resultFrameLanded: false },
      })
      const call = providerToolCall(AgentProvider.KIMI_CODE, result, { spanType: KIMI_TOOL.Bash, completion })
      expect(call?.status).toBe(status)
      expect(call?.result).toBeUndefined()
    }
  })

  it('reads the text retained by an interrupted command and a failed read', () => {
    const command = kimiConversationToolRecord('result', {
      item_id: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'make' },
      output: 'building...\n',
    })
    const retainedCommand = providerToolCall(AgentProvider.KIMI_CODE, command, { spanType: KIMI_TOOL.Bash, completion: MessageCompletion.INTERRUPTED })
    expect(retainedCommand?.status).toBe('cancelled')
    expect(retainedCommand?.result).toStrictEqual({
      commands: [
        { output: 'building...\n' },
      ],
      unresolvedTerminals: [],
    })
    const read = kimiConversationToolRecord('result', {
      item_id: CALL,
      name: KIMI_TOOL.Read,
      input: { path: 'a.go' },
      output: '1\tpackage a',
    })
    expect(kimiToolRow(read, MessageCompletion.ERROR)).toMatchObject({
      result: { output: '1\tpackage a' },
      retained: true,
      lifecycle: { resultFrameLanded: false },
    })
    const retainedRead = providerToolCall(AgentProvider.KIMI_CODE, read, { spanType: KIMI_TOOL.Read, completion: MessageCompletion.ERROR })
    expect(retainedRead?.status).toBe('failed')
    expect(retainedRead?.result).toMatchObject({
      lines: [
        { num: 1, text: 'package a' },
      ],
    })
  })

  it('reads no row that is not a tool event', () => {
    expect(kimiToolRow({ type: 'turn.ended' }, undefined)).toBeNull()
  })
})

describe('kimi execute calls', () => {
  it('reads a successful command with its exit code of zero', () => {
    const call = resultCall(KIMI_TOOL.Bash, { command: 'echo hi', description: 'Say hi' }, 'hi\n', {}, {
      kind: 'command',
      command: 'echo hi',
      cwd: '/work',
      language: 'bash',
    })
    expect(call?.kind).toBe('execute')
    expect(call?.status).toBe('completed')
    expect(call?.request).toStrictEqual({
      command: 'echo hi',
      description: 'Say hi',
      cwd: '/work',
      language: 'bash',
    })
    expect(call?.result).toStrictEqual({
      commands: [
        { output: 'hi\n', exitCode: 0 },
      ],
      unresolvedTerminals: [],
    })
  })

  it('reads the exit code a failed command states and drops the trailer', () => {
    const call = resultCall(KIMI_TOOL.Bash, { command: 'false' }, 'oops\nCommand failed with exit code: 2.', { outcome: 'failed' })
    expect(call?.status).toBe('failed')
    expect(call?.result).toStrictEqual({
      commands: [
        { output: 'oops', exitCode: 2 },
      ],
      unresolvedTerminals: [],
    })
  })

  it('reads a command the server stopped as cancelled', () => {
    for (const trailer of ['Command killed by timeout (60s)', 'Interrupted by user']) {
      const call = resultCall(KIMI_TOOL.Bash, { command: 'sleep 99' }, `partial\n${trailer}`, { outcome: 'failed' })
      expect(call?.status, trailer).toBe('cancelled')
    }
  })

  it('states no result while the command runs', () => {
    const call = providerToolCall(AgentProvider.KIMI_CODE, kimiConversationToolRecord('request', {
      item_id: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'ls' },
    }), { spanType: KIMI_TOOL.Bash, role: 'request' })
    expect(call?.result).toBeUndefined()
  })

  it('ignores a language the command body cannot highlight', () => {
    const call = resultCall(KIMI_TOOL.Bash, { command: 'x' }, 'ok', {}, { kind: 'command', language: 'cobol' })
    expect(call?.request).toStrictEqual({ command: 'x' })
  })

  it('reads the command from the display when the arguments state none, and the argument directory first', () => {
    const call = resultCall(KIMI_TOOL.Bash, { cwd: '/args' }, 'ok', {}, { kind: 'command', command: 'ls', cwd: '/display' })
    expect(call?.request).toStrictEqual({ command: 'ls', cwd: '/args' })
  })

  it('reads a negative exit code from the trailer', () => {
    const call = resultCall(KIMI_TOOL.Bash, { command: 'kill -9 $$' }, 'Command failed with exit code: -1.', { outcome: 'failed' })
    expect(call?.result).toStrictEqual({
      commands: [
        { output: '', exitCode: -1 },
      ],
      unresolvedTerminals: [],
    })
  })

  it('reads the trailer only at the end of the output', () => {
    const output = 'Command failed with exit code: 2.\nmore output'
    const call = resultCall(KIMI_TOOL.Bash, { command: 'x' }, output, { outcome: 'failed' })
    // A failed command whose output states no trailer states no exit code.
    expect(call?.status).toBe('failed')
    expect(call?.result).toStrictEqual({
      commands: [
        { output },
      ],
      unresolvedTerminals: [],
    })
  })

  it('reads a stop notice in the output of a command that succeeded as plain output', () => {
    const call = resultCall(KIMI_TOOL.Bash, { command: 'x' }, 'out\nInterrupted by user')
    expect(call?.status).toBe('completed')
    expect(call?.result).toStrictEqual({
      commands: [
        { output: 'out\nInterrupted by user', exitCode: 0 },
      ],
      unresolvedTerminals: [],
    })
  })
})

describe('kimi file calls', () => {
  it('reads a read with its numbered lines and its system note', () => {
    const call = resultCall(KIMI_TOOL.Read, { path: 'a.go', line_offset: 3, n_lines: 2 }, '3\tpackage a\n4\tfunc A() {}\n<system>2 lines read from file.</system>', {}, { kind: 'file_io', path: '/work/a.go' })
    expect(call?.request).toStrictEqual({ path: '/work/a.go', offset: 3, limit: 2 })
    expect(call?.result).toMatchObject({
      lines: [
        { num: 3, text: 'package a' },
        { num: 4, text: 'func A() {}' },
      ],
    })
  })

  it('reads a write and an edit as the change they asked for', () => {
    const write = resultCall(KIMI_TOOL.Write, { path: 'goal.txt', content: 'ok\n' }, 'Wrote 3 bytes', {}, { kind: 'file_io', path: '/work/goal.txt' })
    expect(write?.result).toStrictEqual({
      changes: [
        {
          filePath: '/work/goal.txt',
          operation: 'add',
          oldStr: '',
          newStr: 'ok\n',
          structuredPatch: null,
        },
      ],
    })
    const edit = resultCall(KIMI_TOOL.Edit, {
      path: 'a.go',
      old_string: 'x',
      new_string: 'y',
      replace_all: true,
    }, 'Edited')
    expect(edit?.request).toStrictEqual({
      changes: [
        {
          filePath: 'a.go',
          operation: 'edit',
          oldStr: 'x',
          newStr: 'y',
          structuredPatch: null,
        },
      ],
      replaceAll: true,
    })
  })

  it('reads a read with no window, and ignores a window that is not a number', () => {
    expect(resultCall(KIMI_TOOL.Read, { path: 'a.go' }, '1\tx')?.request).toStrictEqual({ path: 'a.go' })
    expect(resultCall(KIMI_TOOL.Read, { path: 'a.go', line_offset: '3', n_lines: null }, '1\tx')?.request).toStrictEqual({ path: 'a.go' })
  })

  it('reads a media read as the picture it returned', () => {
    const call = resultCall(KIMI_TOOL.ReadMediaFile, { path: 'a.png' }, 'Read image a.png', {
      media: [
        { kind: 'image', url: 'data:image/png;base64,QUJD' },
      ],
    })
    expect(call?.kind).toBe('read')
    expect(call?.result).toStrictEqual({ lines: null, fallbackContent: 'Read image a.png' })
    expect(call?.images).toStrictEqual([
      { mimeType: 'image/png', data: 'QUJD' },
    ])
  })

  it('reads a file change that states no file as the generic card', () => {
    const call = resultCall(KIMI_TOOL.Write, { content: 'x' }, 'Wrote')
    expect(call?.kind).toBe('other')
    expect(resultCall(KIMI_TOOL.Edit, { old_string: 'x', new_string: 'y' }, 'Edited')?.kind).toBe('other')
  })

  it('states the change of each kind', () => {
    expect(kimiFileChanges('write', {})).toStrictEqual([])
    expect(kimiFileChanges('edit', { path: 'a' })).toStrictEqual([
      {
        filePath: 'a',
        operation: 'edit',
        oldStr: '',
        newStr: '',
        structuredPatch: null,
      },
    ])
  })
})

describe('kimi search calls', () => {
  it('reads a glob as its paths', () => {
    const call = resultCall(KIMI_TOOL.Glob, { pattern: '*.go' }, 'a.go\nb.go\n')
    expect(call?.result).toMatchObject({
      filenames: ['a.go', 'b.go'],
      numFiles: 2,
      empty: false,
    })
  })

  it('reads a glob that found nothing as empty', () => {
    const call = resultCall(KIMI_TOOL.Glob, { pattern: '*.rs' }, 'No files found.')
    expect(call?.result).toMatchObject({ filenames: [], numFiles: 0, empty: true })
  })

  it('reads the grep output of each mode', () => {
    const content = resultCall(KIMI_TOOL.Grep, { pattern: 'x' }, 'a.go:3:x\nb.go:9:x')
    expect(content?.result).toMatchObject({
      filenames: ['a.go', 'b.go'],
      numLines: 2,
      mode: 'content',
      empty: false,
    })
    const files = resultCall(KIMI_TOOL.Grep, { pattern: 'x', output_mode: 'files_with_matches' }, 'a.go')
    expect(files?.result).toMatchObject({
      filenames: ['a.go'],
      mode: 'files_with_matches',
    })
    const count = resultCall(KIMI_TOOL.Grep, { pattern: 'x', output_mode: 'count_matches' }, 'a.go:2')
    expect(count?.result).toMatchObject({ mode: 'count' })
    const none = resultCall(KIMI_TOOL.Grep, { pattern: 'x' }, 'No matches found.')
    expect(none?.result).toMatchObject({ empty: true, numLines: 0 })
  })

  // A `<system>` note is the server's own words, not a match, and a line with no
  // `path:` prefix names no file.
  it('counts no system note and no file for a line that states none', () => {
    const output = 'a.go:1:x\nnot a match line\n<system>Output truncated.</system>'
    const call = resultCall(KIMI_TOOL.Grep, { pattern: 'x' }, output, { truncated: true })
    expect(call?.result).toStrictEqual({
      filenames: ['a.go'],
      content: 'a.go:1:x\nnot a match line',
      numFiles: 1,
      numLines: 2,
      truncated: true,
      fallbackContent: output,
      empty: false,
      mode: 'content',
    })
  })

  it('reads a file-list grep that found nothing as empty', () => {
    const call = resultCall(KIMI_TOOL.Grep, { pattern: 'x', output_mode: 'files_with_matches' }, 'No files found.')
    expect(call?.result).toMatchObject({
      filenames: [],
      numFiles: 0,
      empty: true,
      mode: 'files_with_matches',
    })
  })

  it('reads a glob whose output is only a system note as empty', () => {
    const call = resultCall(KIMI_TOOL.Glob, { pattern: '*.rs' }, '<system>No files matched.</system>\n')
    expect(call?.result).toMatchObject({ filenames: [], numFiles: 0, empty: true })
  })
})

describe('kimi fetch and web search calls', () => {
  it('reads a fetch as the page it returned', () => {
    const call = resultCall(KIMI_TOOL.FetchURL, { url: 'https://example.com' }, '# Example')
    expect(call?.result).toStrictEqual({ result: '# Example' })
  })

  it('reads a web search as its summary, with no links', () => {
    const call = resultCall(KIMI_TOOL.WebSearch, { query: 'kimi' }, 'Kimi Code is a coding agent.')
    expect(call?.request).toStrictEqual({ query: 'kimi' })
    expect(call?.result).toStrictEqual({ links: [], summary: 'Kimi Code is a coding agent.' })
  })
})

describe('kimi subagent calls', () => {
  it('reads an Agent result as its run', () => {
    const call = resultCall(KIMI_TOOL.Agent, { prompt: 'Look.', description: 'Probe', subagent_type: 'explore' }, 'agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\n\n[summary]\nDone.\n\nresume_hint: Continue.')
    expect(call?.request).toStrictEqual({ description: 'Probe', agentType: 'explore', prompt: 'Look.' })
    expect(call?.result).toStrictEqual({
      agents: [
        {
          description: 'Probe',
          agentId: 'agent-0',
          outcome: 'completed',
          metadata: [
            { label: 'Type', value: 'explore' },
          ],
          body: 'Done.',
        },
      ],
    })
  })

  it('reads a swarm result as one run for each member', () => {
    const runs = kimiSwarmRuns('<agent_swarm_result>\n<subagent agent_id="agent-0" item="alpha" outcome="completed">a</subagent>\n<subagent agent_id="agent-1" item="beta" outcome="failed">b</subagent>\n<subagent agent_id="agent-2" outcome="suspended">c</subagent>\n</agent_swarm_result>')
    expect(runs.map(run => [
      run.agentId,
      run.description,
      run.outcome,
      run.statusLabel,
    ])).toStrictEqual([
      [
        'agent-0',
        'alpha',
        'completed',
        undefined,
      ],
      ['agent-1', 'beta', 'failed', undefined],
      [
        'agent-2',
        'agent-2',
        'unknown',
        'suspended',
      ],
    ])
  })

  it('reads the outcome words of a run', () => {
    for (const [status, outcome] of [
      ['failed', 'failed'],
      ['timed_out', 'failed'],
      ['cancelled', 'stopped'],
      ['killed', 'stopped'],
      ['running', 'running'],
      ['odd', 'unknown'],
    ] as const)
      expect(kimiAgentRuns(`agent_id: a\nstatus: ${status}\n\n[summary]\nx`, 'd')[0]?.outcome, status).toBe(outcome)
    expect(kimiAgentRuns('no header here', 'd')).toStrictEqual([])
  })

  it('keeps an Agent result it cannot read as the words', () => {
    const call = resultCall(KIMI_TOOL.Agent, { prompt: 'Look.', description: 'Probe' }, 'The subagent is running in the background.')
    expect(call?.result).toStrictEqual({ unparsed: true, text: 'The subagent is running in the background.' })
  })

  it('states no result for an Agent result that states nothing', () => {
    const call = resultCall(KIMI_TOOL.Agent, { prompt: 'Look.', description: 'Probe' }, '')
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
  })

  // A swarm states a template and items rather than one prompt.
  it('reads a swarm request by its description and its template', () => {
    const call = resultCall(KIMI_TOOL.AgentSwarm, {
      description: 'Probe swarm',
      prompt_template: 'Reply with {{item}}.',
      items: ['alpha'],
    }, '<subagent agent_id="agent-0" item="alpha" outcome="completed">alpha done</subagent>')
    expect(call?.request).toStrictEqual({ description: 'Probe swarm', prompt: 'Reply with {{item}}.' })
    expect(call?.title).toBe('Probe swarm')
    expect(call?.result).toStrictEqual({
      agents: [
        {
          description: 'alpha',
          agentId: 'agent-0',
          outcome: 'completed',
          metadata: [],
          body: 'alpha done',
        },
      ],
    })
  })

  it('reads the agent name the display states when the call states no description', () => {
    const call = resultCall(KIMI_TOOL.Agent, { prompt: 'Look.' }, 'agent_id: agent-3\nstatus: running', {}, { agent_name: 'Explorer' })
    expect(call?.request).toStrictEqual({ description: 'Explorer', prompt: 'Look.' })
    expect(call?.result).toStrictEqual({
      agents: [
        {
          description: 'Explorer',
          agentId: 'agent-3',
          outcome: 'running',
          metadata: [],
          body: '',
        },
      ],
    })
  })

  it('reads no swarm run from a result with no subagent element', () => {
    expect(kimiSwarmRuns('')).toStrictEqual([])
    expect(kimiSwarmRuns('<agent_swarm_result><summary>completed: 0</summary></agent_swarm_result>')).toStrictEqual([])
  })
})

describe('kimi question and to-do calls', () => {
  it('reads the answers an AskUserQuestion result states', () => {
    const call = resultCall(KIMI_TOOL.AskUserQuestion, {
      questions: [
        {
          question: 'Which?',
          header: 'Pick',
          options: [
            { label: 'A' },
            { label: 'B' },
          ],
        },
      ],
    }, '{"answers":{"Which?":"A"}}')
    expect(call?.request).toStrictEqual({
      questions: [
        {
          header: 'Pick',
          question: 'Which?',
          options: [
            { label: 'A' },
            { label: 'B' },
          ],
        },
      ],
    })
    expect(call?.result).toStrictEqual({
      answers: [
        { header: 'Which?', answer: 'A' },
      ],
    })
  })

  // Kimi Code 2.x states a dismissal, which an interrupt causes too, as an empty map and
  // a note. The note is all the result row can draw.
  it('reads a dismissed question as no answers and the note that states the dismissal', () => {
    const call = resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '{"answers":{},"note":"User dismissed the question without answering."}')
    expect(call?.result).toStrictEqual({ answers: [], note: 'User dismissed the question without answering.' })
  })

  it('keeps the note beside the answers, and drops a note that holds no text', () => {
    expect(resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '{"answers":{"Which?":"A"},"note":"Answered."}')?.result)
      .toStrictEqual({
        answers: [
          { header: 'Which?', answer: 'A' },
        ],
        note: 'Answered.',
      })
    expect(resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '{"answers":{},"note":"  "}')?.result).toStrictEqual({ answers: [] })
    expect(resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '{"answers":{},"note":7}')?.result).toStrictEqual({ answers: [] })
  })

  it('keeps a question result it cannot read as the words', () => {
    expect(resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, 'not json')?.result).toStrictEqual({ unparsed: true, text: 'not json' })
    expect(resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '{"note":"no answers"}')?.result).toStrictEqual({ unparsed: true, text: '{"note":"no answers"}' })
    expect(resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '["Which?"]')?.result).toStrictEqual({ unparsed: true, text: '["Which?"]' })
  })

  it('reads an answer that is not a string as no answer', () => {
    const call = resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '{"answers":{"Which?":["A","B"]}}')
    expect(call?.result).toStrictEqual({
      answers: [
        { header: 'Which?', answer: null },
      ],
    })
  })

  it('states no result for a question result that states nothing', () => {
    const call = resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '')
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
  })

  it('reads a to-do list call as its items', () => {
    const call = resultCall(KIMI_TOOL.TodoList, {
      todos: [
        { title: 'A', status: 'done' },
        { title: 'B', status: 'pending' },
      ],
    }, 'Todo list updated.')
    expect(call?.kind).toBe('todo')
    expect(call?.request).toMatchObject({
      items: [
        { content: 'A', status: 'completed' },
        { content: 'B', status: 'pending' },
      ],
    })
    // Each call states the whole list, so the result is the list the call wrote.
    expect(call?.result).toStrictEqual({ items: (call?.request as { items: unknown[] }).items })
  })

  it('reads an empty to-do list as a list that holds nothing', () => {
    const call = resultCall(KIMI_TOOL.TodoList, { todos: [] }, 'Todo list updated.')
    expect(call?.kind).toBe('todo')
    expect(call?.request).toStrictEqual({ items: [] })
  })

  it('reads a to-do call that only reads the list as the generic card', () => {
    expect(resultCall(KIMI_TOOL.TodoList, {}, 'Current todo list: (empty)')?.kind).toBe('other')
  })
})

describe('kimi task, trigger and wait calls', () => {
  it('reads the action each task tool states', () => {
    expect(resultCall(KIMI_TOOL.TaskOutput, { task_id: 'bash-1' }, 'x')?.request).toStrictEqual({ action: 'output', taskId: 'bash-1' })
    expect(resultCall(KIMI_TOOL.TaskStop, { task_id: 'bash-1' }, 'x')?.request).toStrictEqual({ action: 'stop', taskId: 'bash-1' })
    expect(resultCall(KIMI_TOOL.TaskList, {}, 'x')?.request).toStrictEqual({ action: 'list' })
  })

  it('reads the action and the schedule of a cron call', () => {
    expect(resultCall(KIMI_TOOL.CronCreate, { cron: '0 9 * * *', prompt: 'Check.' }, 'ok')?.request).toStrictEqual({ action: 'create', schedule: '0 9 * * *', name: 'Check.' })
    expect(resultCall(KIMI_TOOL.CronDelete, { id: 'cron-1' }, 'ok')?.request).toStrictEqual({ action: 'delete', triggerId: 'cron-1' })
  })

  it('reads a wait limit in seconds', () => {
    expect(resultCall(KIMI_TOOL.WaitFor, { timeout: 30 }, 'ok')?.request).toStrictEqual({ durationMs: 30000 })
    expect(resultCall(KIMI_TOOL.WaitFor, {}, 'ok')?.request).toStrictEqual({})
  })

  it('reads what a task tool printed as its output', () => {
    const call = resultCall(KIMI_TOOL.TaskOutput, { task_id: 'bash-1' }, 'status: running\noutput: hi')
    expect(call?.title).toBe(KIMI_TOOL.TaskOutput)
    expect(call?.result).toStrictEqual({ outcome: 'completed', output: 'status: running\noutput: hi' })
  })

  it('reads the list action of a cron call, and a stated name before the prompt', () => {
    expect(resultCall(KIMI_TOOL.CronList, {}, 'none')?.request).toStrictEqual({ action: 'list' })
    expect(resultCall(KIMI_TOOL.CronCreate, { cron: '0 9 * * *', name: 'daily', prompt: 'Check.' }, 'ok')?.request)
      .toStrictEqual({ action: 'create', schedule: '0 9 * * *', name: 'daily' })
    expect(resultCall(KIMI_TOOL.CronCreate, { cron: '0 9 * * *' }, 'ok')?.request).toStrictEqual({ action: 'create', schedule: '0 9 * * *' })
  })

  it('reads a wait limit of zero seconds as zero, and ignores a limit that is not a number', () => {
    expect(resultCall(KIMI_TOOL.WaitFor, { timeout: 0 }, 'ok')?.request).toStrictEqual({ durationMs: 0 })
    expect(resultCall(KIMI_TOOL.WaitFor, { timeout: '30' }, 'ok')?.request).toStrictEqual({})
  })

  it('reads a Model Context Protocol call by its prefixed name', () => {
    const call = resultCall('mcp__github__search_repos', { q: 'x' }, 'found')
    expect(call?.kind).toBe('mcp')
    expect(call?.request).toMatchObject({ server: 'github', tool: 'search_repos' })
  })

  it('reads the words and the pictures a Model Context Protocol call returned', () => {
    const call = resultCall('mcp__s__t', { q: 1 }, 'hi', {
      media: [
        { kind: 'image', url: 'data:image/png;base64,QUJD' },
      ],
    })
    expect(call?.request).toStrictEqual({
      args: { q: 1 },
      server: 's',
      tool: 't',
    })
    expect(call?.result).toStrictEqual({
      content: [
        { type: 'text', text: 'hi' },
        {
          type: 'image',
          source: { mimeType: 'image/png', data: 'QUJD' },
        },
      ],
    })
  })
})

describe('kimi mode, message and report calls', () => {
  it('keeps the removed model-entry tool uncategorized', () => {
    expect(resultCall('EnterPlanMode', {}, 'Tool not found')?.kind).toBe('other')
  })

  it('reads the native exit from Plan mode', () => {
    // An ExitPlanMode that proposes no plan is an ordinary switch back.
    const exit = resultCall(KIMI_TOOL.ExitPlanMode, {}, 'Exited plan mode.')
    expect(exit?.kind).toBe('switch_mode')
    expect(exit?.request).toStrictEqual({ mode: 'default' })
    expect(exit?.result).toStrictEqual({ text: 'Exited plan mode.', format: 'plain' })
  })

  it('reads the text of a notice from the first field that states one', () => {
    expect(resultCall(KIMI_TOOL.NotifyUser, { message: 'M', body: 'B', title: 'T' }, 'ok')?.request).toStrictEqual({ text: 'M' })
    expect(resultCall(KIMI_TOOL.NotifyUser, { body: 'B', title: 'T' }, 'ok')?.request).toStrictEqual({ text: 'B' })
    expect(resultCall(KIMI_TOOL.NotifyUser, { title: 'T' }, 'ok')?.request).toStrictEqual({ text: 'T' })
  })

  it('titles a call by the display description, then the argument description, then the tool name', () => {
    expect(resultCall(KIMI_TOOL.GetGoal, { description: 'From args' }, '{}', {}, { description: 'From display' })?.title).toBe('From display')
    expect(resultCall(KIMI_TOOL.GetGoal, { description: 'From args' }, '{}')?.title).toBe('From args')
    expect(resultCall(KIMI_TOOL.GetGoal, {}, '{}')?.title).toBe(KIMI_TOOL.GetGoal)
  })

  it('reads a goal call as a report of the words it returned', () => {
    const call = resultCall(KIMI_TOOL.GetGoal, {}, '{"objective":"Ship it","status":"active"}')
    expect(call?.kind).toBe('report')
    expect(call?.result).toStrictEqual({ text: '{"objective":"Ship it","status":"active"}', format: 'plain' })
  })
})

describe('kimi uncategorized calls', () => {
  it('reads a tool no table lists as the generic card, labeled by its name', () => {
    const call = resultCall('FutureTool', { q: 1 }, 'hi')
    expect(call?.kind).toBe('other')
    expect(call?.label).toBe('FutureTool')
    expect(call?.request).toStrictEqual({
      args: { q: 1 },
    })
    expect(call?.result).toStrictEqual({
      content: [
        { type: 'text', text: 'hi' },
      ],
    })
  })

  it('reads an empty output of the generic card as no content', () => {
    expect(resultCall('FutureTool', {}, '')?.result).toStrictEqual({ content: [] })
  })

  it('reads a call that states no tool name as the generic card with no label', () => {
    const call = providerToolCall(AgentProvider.KIMI_CODE, kimiConversationToolRecord('request', {
      item_id: CALL,
      name: '',
      input: { a: 1 },
    }), { spanType: '', role: 'request' })
    expect(call?.kind).toBe('other')
    expect(call?.label).toBeUndefined()
    expect(call?.request).toStrictEqual({
      args: { a: 1 },
    })
  })
})

describe('public Kimi media', () => {
  it('renders verified inline images and presents audio and video URLs as text', () => {
    const call = resultCall('mcp__s__media', {}, 'Text', {
      media: [
        { kind: 'image', url: 'https://example.com/a.png' },
        { kind: 'image', url: 'data:image/png;base64,QUJD' },
        { kind: 'audio', url: 'file:///clip.mp3' },
        { kind: 'video', url: 'file:///clip.mp4' },
        { kind: 'audio' },
        { kind: 'video', url: '' },
        { kind: 'image' },
      ],
    })
    expect(call?.result).toStrictEqual({
      content: [
        { type: 'text', text: 'Text\nfile:///clip.mp3\nfile:///clip.mp4' },
        {
          type: 'image',
          source: { mimeType: 'image/png', data: 'QUJD' },
        },
      ],
    })
  })
})
