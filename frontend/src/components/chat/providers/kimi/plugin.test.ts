import { describe, expect, it } from 'vitest'
import { KIMI_DEFAULT_MODE, KIMI_EVENT, KIMI_MODE, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiApprovalRequest, kimiFrame, kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
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
    const start = kimiToolStart('c', KIMI_TOOL.Agent, { prompt: 'p' })

    expect(plugin.transcript.classify(input(warning, undefined, AgentProvider.KIMI_CODE))).toStrictEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Warning: Low disk' }] })
    expect(plugin.transcript.notificationEntry?.(warning)).toStrictEqual([{ kind: 'text', text: 'Warning: Low disk' }])
    expect(plugin.transcript.spanRole(parsed(kimiToolResult('c', 'x')))).toBe('result')
    expect(plugin.transcript.relatedMessages?.(parsed(start))).toStrictEqual(['result'])
    expect(plugin.transcript.extractDivider?.(kimiFrame(KIMI_EVENT.TurnEnded, { reason: 'cancelled' }))).toStrictEqual({ label: 'Turn interrupted' })
    expect(plugin.session?.compactionBoundaryFromMessage?.(parsed(kimiFrame(KIMI_EVENT.CompactionCompleted, { result: { tokensBefore: 9, tokensAfter: 1 } }))))
      .toStrictEqual({ pre: 9, post: 1 })
  })

  it('words a saved answer through the Kimi summary', () => {
    const summary = plugin.controls?.controlResponseDisplay?.({
      requestId: 'r',
      claimToken: 't',
      request: kimiApprovalRequest(KIMI_TOOL.Bash, { kind: 'command', command: 'ls' }),
      response: { type: 'control_response', response: { subtype: 'success', request_id: 'r', response: { decision: 'approved', scope: 'session' } } },
    })
    expect(summary).toStrictEqual({ kind: 'label', text: 'Allow for this session' })
  })

  // The worker's `ValidateAttachment` states the same rule: text and images, no PDF
  // and no binary file.
  it('advertises text and image attachments only', () => {
    expect(plugin.configuration?.attachments).toStrictEqual({ text: true, image: true, pdf: false, binary: false })
  })

  it('carries plan mode on the permission-mode axis', () => {
    const planMode = plugin.configuration?.planMode
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(planMode?.groupKey).toBe('permissionMode')
    expect(planMode?.planValue).toBe(KIMI_MODE.Plan)
    expect(planMode?.defaultValue).toBe(KIMI_DEFAULT_MODE)
    expect(planMode?.currentMode({ optionValues: { permissionMode: KIMI_MODE.Yolo } })).toBe(KIMI_MODE.Yolo)
    expect(planMode?.currentMode({})).toBe(KIMI_DEFAULT_MODE)
  })

  it('maps the presets onto the permission modes that match them', () => {
    expect(plugin.controls?.permissionPresets).toStrictEqual({
      smart: { sets: { permissionMode: KIMI_MODE.Yolo } },
      bypass: { sets: { permissionMode: KIMI_MODE.Auto } },
    })
  })

  it('lets a subagent tab send', () => {
    expect(plugin.configuration?.supportsSubagentSend).toBe(true)
  })

  it('sends the composer text as the reason of a refusal', () => {
    expect(plugin.controls?.buildControlResponse?.({}, 'Not this way.', 'approval_1')).toStrictEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'approval_1', response: { behavior: 'deny', message: 'Not this way.' } },
    })
  })
})
