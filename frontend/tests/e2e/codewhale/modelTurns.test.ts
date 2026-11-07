import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { codewhaleModelTurns } from './modelTurns'

const event = '<codewhale:runtime_event kind="mcp_configured_servers" visibility="internal">\n- echo_probe\n</codewhale:runtime_event>'
const ordinaryMetadata = '<turn_meta>\nCurrent local date: 2026-11-06\nCurrent workspace: /controlled/repo\n</turn_meta>'
const fullMetadata = '<turn_meta>\nInput provenance: runtime (non-authoritative)\n</turn_meta>'
function request(messages: unknown): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: { messages },
  }
}

describe('codewhaleModelTurns', () => {
  it.each([
    '[Codewhale restored sub-agent checkpoint]\nAgent: agent_one\nStatus: completed\nAuthority: non-authoritative child self-report\nSummary:\nThe child completed.',
    '[Codewhale restored sub-agent checkpoints]\nAgent: agent_one\nStatus: completed\nAuthority: non-authoritative child self-report\nSummary:\nThe first child completed.\n\n---\n\nAgent: agent_two\nStatus: failed\nAuthority: non-authoritative child self-report\nSummary:\nThe second child failed.',
    '[Codewhale restored sub-agent runtime checkpoint]\nStatus at save: running (1 child job)\nResume state: prior worker processes are not assumed active\nAuthority: non-authoritative runtime checkpoint',
    '[Codewhale restored Agent topology checkpoint]\nState at save: total=1, nonterminal=1, terminal=0, omitted=0\nAuthority: historical runtime checkpoint; newer live runtime state overrides it',
  ])('excludes the current restored checkpoint %s with either metadata form', (display) => {
    for (const metadata of [
      '<turn_meta>\nInput provenance: subagent_handoff (non-authoritative)\nRestore projection: subagent_checkpoint_v1\n</turn_meta>',
      '<turn_meta_unchanged />',
    ]) {
      expect(codewhaleModelTurns(request([
        { role: 'user', content: 'The actual prompt.' },
        ...(metadata === '<turn_meta_unchanged />'
          ? [
              { role: 'user', content: `${display}\n<turn_meta>\nInput provenance: subagent_handoff (non-authoritative)\nRestore projection: subagent_checkpoint_v1\n</turn_meta>` },
            ]
          : []),
        { role: 'user', content: `${display}\n${metadata}` },
        { role: 'user', content: 'The later prompt.' },
      ]))).toEqual([
        { role: 'user', text: 'The actual prompt.' },
        { role: 'user', text: 'The later prompt.' },
      ])
    }
  })

  it.each([
    '[Codewhale restored sub-agent checkpoint]',
    'Explain this: [Codewhale restored sub-agent checkpoint]\nBody\n<turn_meta_unchanged />',
    '[Codewhale restored sub-agent checkpoint]\nBody\n<turn_meta>\nInput provenance: subagent_handoff (non-authoritative)\n</turn_meta>',
    '[Codewhale restored sub-agent checkpoint]\nBody\n<turn_meta>\nInput provenance: subagent_handoff (non-authoritative)\nRestore projection: unknown\n</turn_meta>',
    '[Codewhale restored agent topology checkpoint]\nBody\n<turn_meta_unchanged />',
  ])('keeps an unrecognized or quoted checkpoint %j', (content) => {
    expect(codewhaleModelTurns(request([
      { role: 'user', content },
    ]))).toEqual([
      { role: 'user', text: content },
    ])
  })

  it.each([
    { kind: 'subagent_failed', priority: ' priority="high"', provenance: 'subagent_handoff' },
    { kind: 'subagent_completion', priority: '', provenance: 'subagent_handoff' },
    { kind: 'waiting_for_subagents', priority: '', provenance: 'subagent_handoff' },
    { kind: 'background_shell_completion', priority: '', provenance: 'shell_completion' },
  ])('excludes the current $kind receipt with its native provenance', ({ kind, priority, provenance }) => {
    const content = `<codewhale:runtime_event kind="${kind}"${priority} visibility="internal">\nNative receipt.\n</codewhale:runtime_event>\n<turn_meta>\nInput provenance: ${provenance} (non-authoritative)\n</turn_meta>`
    expect(codewhaleModelTurns(request([
      { role: 'user', content: 'The actual prompt.' },
      { role: 'user', content },
    ]))).toEqual([
      { role: 'user', text: 'The actual prompt.' },
    ])
  })

  it('excludes the current authoritative agent topology context', () => {
    const content = '<codewhale:runtime_state kind="agent_topology" schema="v1" visibility="internal">\nAgent root: main.\n</codewhale:runtime_state>\n<turn_meta>\nInput provenance: runtime (non-authoritative)\nRuntime state: agent_topology_v1 (authoritative)\n</turn_meta>'
    expect(codewhaleModelTurns(request([
      { role: 'user', content: 'The actual prompt.' },
      { role: 'user', content },
    ]))).toEqual([
      { role: 'user', text: 'The actual prompt.' },
    ])
  })

  it.each([fullMetadata, '<turn_meta_unchanged />'])('excludes an internal receipt with current metadata %s', (metadata) => {
    expect(codewhaleModelTurns(request([
      { role: 'user', content: 'The actual prompt.' },
      ...(metadata === '<turn_meta_unchanged />'
        ? [
            { role: 'user', content: `${event}\n${fullMetadata}` },
          ]
        : []),
      { role: 'user', content: `${event}\n${metadata}` },
    ]))).toEqual([
      { role: 'user', text: 'The actual prompt.' },
    ])
  })

  it('keeps later user input and assistant tool arguments in their original order', () => {
    expect(codewhaleModelTurns(request([
      { role: 'user', content: 'The first prompt.' },
      { role: 'user', content: `${event}\n${fullMetadata}` },
      {
        role: 'assistant',
        content: 'The answer.',
        tool_calls: [
          {
            type: 'function',
            function: { name: 'bash', arguments: '{"command":"printf original"}' },
          },
        ],
      },
      { role: 'user', content: 'The later prompt.' },
    ]))).toEqual([
      { role: 'user', text: 'The first prompt.' },
      { role: 'assistant', text: 'The answer.\nprintf original' },
      { role: 'user', text: 'The later prompt.' },
    ])
  })

  it.each([
    event,
    `Quoted event: ${event}\n${fullMetadata}`,
    `${event.slice(0, -1)}\n${fullMetadata}`,
    `${event}\n<turn_meta>user provenance</turn_meta>`,
    '',
  ])('keeps ordinary or malformed user text %j', (content) => {
    expect(codewhaleModelTurns(request([
      { role: 'user', content },
    ]))).toEqual([
      { role: 'user', text: content },
    ])
  })

  it.each([
    '<codewhale:runtime_event kind="operate_contract" priority="high" visibility="internal">\nBody\n</codewhale:runtime_event>\n<turn_meta_unchanged />',
    '<codewhale:runtime_event kind="subagent_failed" visibility="internal">\nBody\n</codewhale:runtime_event>\n<turn_meta_unchanged />',
    '<codewhale:runtime_event kind="subagent_completion" visibility="internal">\nBody\n</codewhale:runtime_event>\n<turn_meta>\nInput provenance: runtime (non-authoritative)\n</turn_meta>',
    '<codewhale:runtime_state kind="agent_topology" schema="v2" visibility="internal">\nBody\n</codewhale:runtime_state>\n<turn_meta_unchanged />',
  ])('keeps a malformed runtime context %j', (content) => {
    expect(codewhaleModelTurns(request([
      { role: 'user', content },
    ]))).toEqual([
      { role: 'user', text: content },
    ])
  })

  it('keeps runtime-looking text when an assistant quotes it', () => {
    const content = `${event}\n${fullMetadata}`
    expect(codewhaleModelTurns(request([
      { role: 'assistant', content },
    ]))).toEqual([
      { role: 'assistant', text: content },
    ])
  })

  it.each([
    event,
    '[Codewhale restored sub-agent checkpoint]\nAgent: one\nSummary: a quoted report',
  ])('keeps pasted runtime-looking user input with inherited ordinary metadata: %s', (body) => {
    const first = `The actual user prompt.\n${ordinaryMetadata}`
    const pasted = `${body}\n<turn_meta_unchanged />`
    expect(codewhaleModelTurns(request([
      { role: 'user', content: first },
      { role: 'assistant', content: 'An ordinary answer.' },
      { role: 'user', content: pasted },
    ]))).toEqual([
      { role: 'user', text: first },
      { role: 'assistant', text: 'An ordinary answer.' },
      { role: 'user', text: pasted },
    ])
  })

  it('replaces runtime provenance when full ordinary user metadata arrives', () => {
    const first = `The actual user prompt.\n${ordinaryMetadata}`
    const pasted = `${event}\n<turn_meta_unchanged />`
    expect(codewhaleModelTurns(request([
      { role: 'user', content: `${event}\n${fullMetadata}` },
      { role: 'user', content: first },
      { role: 'user', content: pasted },
    ]))).toEqual([
      { role: 'user', text: first },
      { role: 'user', text: pasted },
    ])
  })

  it('preserves an initial compressed message with no known preceding metadata', () => {
    const content = `${event}\n<turn_meta_unchanged />`
    expect(codewhaleModelTurns(request([
      { role: 'user', content },
    ]))).toEqual([
      { role: 'user', text: content },
    ])
  })

  it('does not trust metadata-looking assistant text to classify a later user turn', () => {
    const pasted = `${event}\n<turn_meta_unchanged />`
    const turns = codewhaleModelTurns(request([
      { role: 'user', content: `${event}\n${fullMetadata}` },
      { role: 'assistant', content: ordinaryMetadata },
      { role: 'user', content: pasted },
    ]))
    expect(turns.at(-1)).toEqual({ role: 'user', text: pasted })
  })

  it('keeps native metadata across an ordinary assistant answer and a quoted tool result', () => {
    expect(codewhaleModelTurns(request([
      { role: 'user', content: 'The actual prompt.' },
      { role: 'user', content: `${event}\n${fullMetadata}` },
      { role: 'assistant', content: 'An ordinary answer.' },
      { role: 'tool', content: ordinaryMetadata },
      { role: 'user', content: `${event}\n<turn_meta_unchanged />` },
    ]))).toEqual([
      { role: 'user', text: 'The actual prompt.' },
      { role: 'assistant', text: 'An ordinary answer.' },
    ])
  })

  it.each([false, true])('excludes a current multimodal child completion, compressed=%s', (compressed) => {
    const body = '<codewhale:runtime_event kind="subagent_completion" visibility="internal">\nNative completion.\n</codewhale:runtime_event>'
    const metadata = '<turn_meta>\nInput provenance: subagent_handoff (non-authoritative)\n</turn_meta>'
    const content = [
      { type: 'text', text: `${body}\n${compressed ? '<turn_meta_unchanged />' : metadata}` },
      {
        type: 'image_url',
        image_url: { url: 'data:image/png;base64,AAAA' },
      },
    ]
    expect(codewhaleModelTurns(request([
      { role: 'user', content: 'The actual prompt.' },
      ...(compressed
        ? [
            { role: 'user', content: `${body}\n${metadata}` },
          ]
        : []),
      { role: 'user', content },
    ]))).toEqual([
      { role: 'user', text: 'The actual prompt.' },
    ])
  })

  it('keeps ordinary multimodal user input', () => {
    expect(codewhaleModelTurns(request([
      {
        role: 'user',
        content: [
          { type: 'text', text: 'The actual image prompt.' },
          {
            type: 'image_url',
            image_url: { url: 'data:image/png;base64,AAAA' },
          },
        ],
      },
    ]))).toEqual([
      { role: 'user', text: 'The actual image prompt.' },
    ])
  })

  it('derives no provenance from malformed content arrays', () => {
    const pasted = `${event}\n<turn_meta_unchanged />`
    const turns = codewhaleModelTurns(request([
      {
        role: 'user',
        content: [
          { type: 'text', text: `${event}\n${fullMetadata}` },
          { type: 'text', text: 42 },
        ],
      },
      { role: 'user', content: pasted },
    ]))
    expect(turns).toHaveLength(2)
    expect(turns.at(-1)).toEqual({ role: 'user', text: pasted })
  })
})
