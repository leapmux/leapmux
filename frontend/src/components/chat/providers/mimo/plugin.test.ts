import type { ControlPrompt } from '../../model/controlPrompt'
import { describe, expect, it } from 'vitest'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { MIMO_DEFAULT_MODE, MIMO_MODE, MIMO_OPTION, MIMO_PERMISSION_POLICY, MIMO_STATUS_TYPE, MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { OPENCODE_EVENT } from '~/generated/contracts/opencode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { assembledMessageRow } from '~/test-support/assembledMessages'
import { buildControlResponseEnvelope } from '~/test-support/controlResponses'
import { mimoFrame, openingFrame, parsedFrame, statusFrame, TEST_SESSION, toolFrame } from '~/test-support/mimoFixtures'
import { providerQuotableText } from '~/test-support/toolCallFixture'
import { extractChatRow, extractedRow } from '../../rowExtraction'
import { providerFor } from '../registry'

import { input } from '../testUtils'
// Side-effect import to register the MiMo plugin.
import './plugin'

const plugin = providerFor(AgentProvider.MIMO_CODE)!

describe('mimo plugin metadata', () => {
  it('is registered for the MIMO_CODE provider', () => {
    expect(plugin).toBeDefined()
  })

  // The worker validates these native audio formats before its live model check.
  it('advertises native audio formats alongside text, image and PDF attachments', () => {
    expect(plugin.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: true,
      audio: [
        'audio/wav',
        'audio/x-wav',
        'audio/mp3',
        'audio/mpeg',
        'audio/flac',
        'audio/x-flac',
        'audio/mp4',
        'audio/m4a',
        'audio/x-m4a',
        'audio/ogg',
      ],
      binary: false,
    })
  })

  it('carries its primary agents on the permission-mode channel', () => {
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin.configuration?.planMode).toMatchObject({
      groupKey: 'permissionMode',
      planValue: MIMO_MODE.Plan,
      defaultValue: MIMO_DEFAULT_MODE,
    })
  })

  it('reads the current primary agent, and falls back to the default before the first report', () => {
    const currentMode = plugin.configuration!.planMode!.currentMode
    expect(currentMode({
      optionValues: { permissionMode: MIMO_MODE.Plan },
    })).toBe(MIMO_MODE.Plan)
    expect(currentMode({ optionValues: {} })).toBe(MIMO_DEFAULT_MODE)
    expect(currentMode({})).toBe(MIMO_DEFAULT_MODE)
  })

  // MiMo has no permission mode. Bypass turns on its own permission policy, which
  // approves every call, deletes included.
  it('maps the bypass preset onto the permission policy', () => {
    expect(plugin.controls?.permissionPresets).toEqual({
      bypass: {
        sets: { [MIMO_OPTION.PermissionPolicy]: MIMO_PERMISSION_POLICY.Bypass },
      },
    })
  })

  it('lets the reader send a message to a subagent', () => {
    expect(plugin.configuration?.supportsSubagentSend).toBe(true)
  })

  // A MiMo session id is an opaque token that the server mints, not a file.
  it('does not treat the session id as a file path', () => {
    expect(plugin.session?.sessionIdIsFilePath).toBeFalsy()
  })
})

describe('mimo plugin controls', () => {
  const elicitation = {
    type: OPENCODE_EVENT.QuestionAsked,
    properties: {
      id: 'que_3',
      questions: [
        {
          key: 'mcp_elicitation',
          header: 'docs',
          question: 'docs\n\nProceed?',
          options: [
            { label: 'Accept', description: '' },
            { label: 'Decline', description: '' },
            { label: 'Cancel', description: '' },
          ],
          multiple: false,
          custom: false,
        },
      ],
    },
    request: { tool_name: MIMO_TOOL.Question },
  }

  /** The row of one saved answer, as the transcript extracts it. */
  function savedRow(request: Record<string, unknown>, response: Record<string, unknown>, prompt: ControlPrompt) {
    return extractedRow(extractChatRow(AgentProvider.MIMO_CODE, input({}, null, AgentProvider.MIMO_CODE), {
      kind: 'control_response',
      response: {
        requestId: 'r',
        claimToken: 't',
        request,
        response,
        prompt,
      },
    }))
  }

  // MiMo asks an MCP server's confirmation as a question that takes no free text. The
  // Worker records it as an elicitation and answers it with the neutral elicitation
  // envelope, and the shared form display states the answer in the elicitation words
  // before MiMo's own display reads it.
  it('states the saved answer of an MCP confirmation in the elicitation words', () => {
    const prompt: ControlPrompt = {
      kind: 'elicitation',
      elicitation: {
        mode: 'form',
        server: 'docs',
        message: 'Proceed?',
        schema: { type: 'object', properties: {} },
      },
    }
    expect(savedRow(elicitation, buildControlResponseEnvelope('r', { action: MCP_ELICITATION_ACTION.Accept, content: {} }), prompt)).toEqual({
      kind: 'control-response',
      display: { kind: 'label', text: 'Approved' },
    })
    expect(savedRow(elicitation, buildControlResponseEnvelope('r', { action: MCP_ELICITATION_ACTION.Decline }), prompt)).toEqual({
      kind: 'control-response',
      display: { kind: 'label', text: 'Rejected' },
    })
    expect(savedRow(elicitation, buildControlResponseEnvelope('r', { action: MCP_ELICITATION_ACTION.Cancel }), prompt)).toEqual({
      kind: 'control-response',
      display: { kind: 'label', text: 'Cancelled' },
    })
  })

  it('leaves the saved answer to a question in the question words', () => {
    const question = {
      type: OPENCODE_EVENT.QuestionAsked,
      properties: {
        id: 'que_1',
        questions: [
          { question: 'Which?', header: 'Pick' },
        ],
      },
      request: { tool_name: MIMO_TOOL.Question },
    }
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        { question: 'Which?', header: 'Pick', options: [] },
      ],
    }
    expect(savedRow(question, {
      result: {
        answers: [
          ['A'],
        ],
      },
    }, prompt)).toEqual({
      kind: 'control-response',
      display: { kind: 'label', text: 'Pick: A' },
    })
  })
})

// The transcript hooks the plugin registers are MiMo's own readers: each answers a
// MiMo frame, which a reader of another provider would not.
describe('mimo plugin transcript', () => {
  it('reads the turn end and the retry of a MiMo frame', () => {
    expect(plugin.transcript.extractDivider(statusFrame(MIMO_STATUS_TYPE.Idle))).toEqual({ label: 'Turn ended' })
    expect(plugin.transcript.notificationEntry?.(statusFrame(MIMO_STATUS_TYPE.Retry, { attempt: 1 }))).toEqual([
      { kind: 'retry', scope: 'api', attempt: 1 },
    ])
  })

  it('pairs the two halves of a MiMo tool call', () => {
    const opening = parsedFrame(openingFrame(MIMO_TOOL.Bash, { command: 'ls' }))
    const final = parsedFrame(toolFrame(MIMO_TOOL.Bash, {
      input: { command: 'ls' },
    }))
    expect(plugin.transcript.spanRole(opening)).toBe('request')
    expect(plugin.transcript.spanRole(final)).toBe('result')
    expect(plugin.transcript.classify(final)).toEqual({ kind: 'tool_result' })
  })
})

describe('mimo quotable text', () => {
  it('reads the user\'s words and the assembled assistant text', () => {
    expect(providerQuotableText(AgentProvider.MIMO_CODE, { content: 'hi' }, {
      category: { kind: 'user_content' },
    })).toBe('hi')
    expect(providerQuotableText(AgentProvider.MIMO_CODE, assembledMessageRow('text', 'Hello'), {
      category: { kind: 'assistant_text' },
    })).toBe('Hello')
    expect(providerQuotableText(AgentProvider.MIMO_CODE, assembledMessageRow('reasoning', 'thinking'), {
      category: { kind: 'assistant_thinking' },
    })).toBe('thinking')
  })

  it('reads nothing from an unknown stored status frame', () => {
    expect(plugin.transcript.classify(parsedFrame(statusFrame(MIMO_STATUS_TYPE.Busy)))).toEqual({ kind: 'unknown' })
    expect(providerQuotableText(AgentProvider.MIMO_CODE, statusFrame(MIMO_STATUS_TYPE.Busy))).toBeNull()
  })

  // The worker persists only the events that the plugin reads. Any other event is a
  // shape that nobody expected, so it stays visible as an unknown row.
  it('classifies an event that no reader knows as unknown', () => {
    expect(plugin.transcript.classify(parsedFrame(mimoFrame('session.diff', { sessionID: TEST_SESSION })))).toEqual({ kind: 'unknown' })
  })
})
