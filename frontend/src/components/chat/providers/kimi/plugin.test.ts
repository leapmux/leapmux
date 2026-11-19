import { describe, expect, it } from 'vitest'
import { KIMI_DEFAULT_MODE, KIMI_EVENT, KIMI_MODE, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { WORKSPACE_TRUST_CONTROL_TYPE, WORKSPACE_TRUST_DECISION } from '~/generated/contracts/workspace-trust'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiApprovalRequest, kimiConversationItemRecord, kimiConversationToolRecord, kimiConversationTurnRecord, kimiFrame } from '~/test-support/kimiFixtures'
import { providerFor, resolveMessageForRendering } from '../registry'
import { input } from '../testUtils'
import './plugin'

const plugin = providerFor(AgentProvider.KIMI_CODE)!

describe('kimi plugin', () => {
  // Each hook answers a Kimi row with the Kimi reading, so a hook that another reader
  // replaced fails here, and not only when a row renders.
  it('registers the Kimi reader behind every transcript and session hook', () => {
    const parsed = (row: Record<string, unknown>) => resolveMessageForRendering(input(row, undefined, AgentProvider.KIMI_CODE), AgentProvider.KIMI_CODE)
    const warning = kimiFrame(KIMI_EVENT.Warning, { message: 'Low disk' })
    const start = kimiConversationToolRecord('request', {
      item_id: 'child',
      name: KIMI_TOOL.Agent,
      input: { prompt: 'p' },
    })
    const result = kimiConversationToolRecord('result', {
      item_id: 'shell',
      name: KIMI_TOOL.Bash,
      input: { command: 'printf x' },
      status: 'ended',
      outcome: 'completed',
      output: 'x',
    })

    expect(plugin.transcript.classify(input(warning, undefined, AgentProvider.KIMI_CODE))).toStrictEqual({
      kind: 'notification',
      entries: [
        { kind: 'text', text: 'Warning: Low disk' },
      ],
    })
    expect(plugin.transcript.notificationEntry?.(warning)).toStrictEqual([
      { kind: 'text', text: 'Warning: Low disk' },
    ])
    expect(plugin.transcript.spanRole(parsed(result))).toBe('result')
    expect(plugin.transcript.needsRequest?.(parsed(start))).toBe(false)
    expect(plugin.transcript.needsRequest?.(parsed(result))).toBe(true)
    expect(plugin.transcript.extractDivider?.(kimiConversationTurnRecord({ status: 'ended', outcome: 'interrupted' }))).toStrictEqual({ label: 'Turn interrupted' })
    expect(plugin.session?.compactionBoundaryFromMessage?.(parsed(kimiConversationItemRecord({
      type: 'system',
      kind: 'compaction',
      trigger: 'auto',
      status: 'ended',
      outcome: 'completed',
      tokens_before: 9,
      tokens_after: 1,
    }))))
      .toStrictEqual({ trigger: 'auto', pre: 9, post: 1 })
  })

  it('words a saved answer through the Kimi summary', () => {
    const summary = plugin.controls?.controlResponseDisplay?.({
      requestId: 'r',
      claimToken: 't',
      request: kimiApprovalRequest(KIMI_TOOL.Bash, { kind: 'command', command: 'ls' }),
      response: {
        type: 'control_response',
        response: {
          subtype: 'success',
          request_id: 'r',
          response: { kind: 'approval', decision: 'approved', scope: 'session' },
        },
      },
    })
    expect(summary).toStrictEqual({ kind: 'label', text: 'Allow for this session' })
  })

  // The worker's `ValidateAttachment` states the same rule: text and images, no PDF
  // and no binary file.
  it('advertises text and image attachments only', () => {
    expect(plugin.configuration?.attachments).toStrictEqual({
      text: true,
      image: true,
      pdf: false,
      audio: false,
      binary: false,
    })
  })

  it('carries plan mode on the permission-mode axis', () => {
    const planMode = plugin.configuration?.planMode
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(planMode?.groupKey).toBe('permissionMode')
    expect(planMode?.planValue).toBe(KIMI_MODE.Plan)
    expect(planMode?.defaultValue).toBe(KIMI_DEFAULT_MODE)
    expect(planMode?.currentMode({
      optionValues: { permissionMode: KIMI_MODE.Yolo },
    })).toBe(KIMI_MODE.Yolo)
    expect(planMode?.currentMode({})).toBe(KIMI_DEFAULT_MODE)
  })

  it('maps the presets onto the permission modes that match them', () => {
    expect(plugin.controls?.permissionPresets).toStrictEqual({
      smart: {
        sets: { permissionMode: KIMI_MODE.Yolo },
      },
      bypass: {
        sets: { permissionMode: KIMI_MODE.Auto },
      },
    })
  })

  it('lets a subagent tab send', () => {
    expect(plugin.configuration?.supportsSubagentSend).toBe(true)
  })
})

// The Worker asks Kimi Code's workspace-trust question itself, in LeapMux's neutral shape.
describe('kimiControls workspace trust', () => {
  const question = {
    type: WORKSPACE_TRUST_CONTROL_TYPE.Question,
    path: '/work/project',
    configuration: ['MCP servers', 'skills', 'instructions'],
  }

  it('reads the saved answer', () => {
    expect(plugin.controls?.controlResponseDisplay?.({
      requestId: 'workspace-trust:session-1',
      claimToken: '',
      request: question,
      response: {
        type: 'control_response',
        response: {
          request_id: 'workspace-trust:session-1',
          response: { decision: WORKSPACE_TRUST_DECISION.Trust },
        },
      },
    })).toEqual({ kind: 'label', text: 'Trusted this folder' })
  })
})
