import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow, providerToolCall } from '~/test-support/toolCallFixture'
import { acpTextContent, describeACPCallRows, describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

const MODE_AUTO = 'auto'
const MODE_SMART_APPROVE = 'smart_approve'

describe('goose provider', () => {
  const plugin = providerFor(AgentProvider.GOOSE)!

  describeACPProviderBasics(AgentProvider.GOOSE, {
    text: true,
    image: true,
    pdf: false,
    audio: false,
    binary: false,
  })

  it('maps smart and bypass permissions to Goose modes', () => {
    expect(plugin?.controls?.permissionPresets).toEqual({
      smart: {
        sets: { permissionMode: MODE_SMART_APPROVE },
      },
      bypass: {
        sets: { permissionMode: MODE_AUTO },
      },
    })
  })

  it('has no plan mode', () => {
    // Goose's writable axis is the top-level permission mode (no plan toggle);
    // the generic settings panel renders the permissionMode group it reports.
    expect(plugin?.configuration?.planMode).toBeUndefined()
  })

  it('still renders the permissionMode group as the trigger mode segment (no plan mode needed)', () => {
    // The trigger mode segment is decoupled from plan mode: Goose has a mode axis
    // (permissionMode) without a plan toggle, so it still declares triggerModeGroupKey.
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('permissionMode')
  })

  it('states its own reasoning axis for the effort chip', () => {
    expect(plugin?.configuration?.effortGroupKey).toBe('thinking_effort')
  })

  it('derives a control-response label via the default ACP permission path (no question hook)', () => {
    // Goose has no question protocol, so it gets the shared acpControlResponseSummary default.
    expect(plugin?.controls?.controlResponseDisplay!({
      claimToken: 'claim-1',
      requestId: '7',
      request: {
        method: 'session/request_permission',
        params: {
          options: [
            { optionId: 'proceed_once', name: 'Allow once' },
          ],
        },
      },
      response: {
        result: {
          outcome: { optionId: 'proceed_once' },
        },
      },
    })).toEqual({ kind: 'label', text: 'Allow once' })
  })
})

describe('native output without a filesystem pointer', () => {
  it('keeps a valid native result without deriving an output path', () => {
    const frame = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'native-call',
      status: 'completed',
      kind: 'execute',
      title: 'native command',
      rawInput: {
        command: 'printf preview',
      },
      content: [
        {
          type: 'content',
          content: {
            type: 'text',
            text: 'native inline preview',
          },
        },
      ],
      rawOutput: {
        stdout: 'native inline preview',
        stderr: '',
        exit_code: 0,
      },
      _meta: {
        goose: {
          toolCall: {
            extensionName: 'developer',
            toolName: 'shell',
          },
        },
      },
    }
    const call = providerToolCall(AgentProvider.GOOSE, frame, {
      spanId: 'native-call',
      spanType: 'shell',
      agentSessionId: 'native-session',
      role: 'result',
    })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.GOOSE)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeTypeOf('function')
  })
})

// A delegation reports its run in the answer alone. The request row draws the launch,
// and the result row carries the report.
describe('goose call rows', () => {
  describeACPCallRows(AgentProvider.GOOSE, [
    {
      name: 'a delegation that reports its run',
      request: {
        sessionUpdate: 'tool_call',
        toolCallId: 'goose-delegate',
        kind: 'other',
        title: 'delegate',
        status: 'pending',
        rawInput: { instructions: 'Inspect the parser' },
        _meta: {
          goose: {
            toolCall: { toolName: 'summon__delegate' },
          },
        },
      },
      result: {
        sessionUpdate: 'tool_call_update',
        toolCallId: 'goose-delegate',
        status: 'completed',
        content: acpTextContent('The parser is fine.'),
      },
      requestCall: {
        kind: 'agent',
        status: 'pending',
        request: { description: 'Inspect the parser', prompt: 'Inspect the parser' },
      },
      resultCall: {
        kind: 'agent',
        status: 'completed',
        request: { description: 'Inspect the parser' },
        result: {
          agents: [
            { outcome: 'completed', body: 'The parser is fine.' },
          ],
        },
      },
    },
  ])
})

// A subagent's tool request rides an unfinished update of the delegation, and its row
// draws as an update row. It states its whole call in its own frame, so it reads no
// other message of its span: neither the delegation's request nor its result.
describe('goose subagent tool request rows', () => {
  const delegation = {
    sessionUpdate: 'tool_call',
    toolCallId: 'goose-delegate',
    kind: 'other',
    title: 'delegate',
    status: 'pending',
    rawInput: { instructions: 'Inspect the parser' },
    _meta: {
      goose: {
        toolCall: { toolName: 'summon__delegate' },
      },
    },
  }
  const update = {
    sessionUpdate: 'tool_call_update',
    toolCallId: 'goose-delegate',
    status: 'in_progress',
    _meta: {
      toolNotification: {
        type: 'message',
        params: {
          data: {
            type: 'subagent_tool_request',
            subagent_id: 'child',
            tool_call: {
              name: 'developer__shell',
              arguments: { command: 'ls' },
            },
          },
        },
      },
    },
  }

  it('builds the update row from its own frame, with no result', () => {
    const row = providerRow(AgentProvider.GOOSE, update, {
      role: 'other',
      request: input(delegation, null, AgentProvider.GOOSE),
    })
    expect(row).toMatchObject({
      kind: 'tool',
      role: 'update',
      hasRequestRow: true,
      call: {
        kind: 'execute',
        label: 'Tool request',
        status: 'in_progress',
        request: { command: 'ls' },
      },
    })
    expect(row?.kind === 'tool' && row.call.result).toBeUndefined()
    expect(JSON.stringify(row)).not.toContain('Inspect the parser')
  })

  it('needs no request for the update row', () => {
    expect(providerFor(AgentProvider.GOOSE)!.transcript.needsRequest?.(input(update, null, AgentProvider.GOOSE))).toBe(false)
  })
})
