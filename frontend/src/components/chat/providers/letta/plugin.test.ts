import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { LETTA_MODEL_ERROR_MARKER, lettaFailedTurn, lettaLoopError, lettaMixedErrorThread } from '~/test-support/lettaFixtures'
import { makeMessage, makeTranscriptMessage, wrapContent } from '~/test-support/messageFactory'
import { elementText } from '~/test-support/messageRenderProbes'
import { flattenNotificationEntries } from '../../notificationEntries'
import { renderNotificationBlocks } from '../../notificationRenderers'
import { prepareChatRow } from '../../rowPreparation'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { pluginFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

describe('lettaPlugin', () => {
  it('reports native compaction sizes and identifies stored starts separately', () => {
    const plugin = pluginFor(AgentProvider.LETTA)
    const frame = { message_type: 'summary_message', compaction_stats: { context_tokens_before: 759, context_tokens_after: 396 } }
    expect(plugin?.session?.compactionBoundaryFromMessage?.(input(frame))).toEqual({ pre: 759, post: 396 })
    expect(plugin?.transcript.compactionStart?.({ message_type: 'event_message', event_type: 'compaction' })).toBe(true)
    expect(plugin?.transcript.compactionStart?.(frame)).toBe(false)
  })
  it('declares the permission-mode axis that the status bar draws', () => {
    const plugin = pluginFor(AgentProvider.LETTA)
    // The status bar draws a mode chip ONLY for the axis the plugin declares.
    // Omitting it hid the mode the session was running behind the group label.
    expect(plugin?.configuration?.triggerModeGroupKey).toBe(OPTION_ID_PERMISSION_MODE)
  })
})

describe('lettaPlugin a failed model request', () => {
  it('reads the failed turn reason once through transcript preparation', () => {
    const message = makeTranscriptMessage({ id: 'failed', provider: AgentProvider.LETTA, content: lettaFailedTurn(), completion: MessageCompletion.ERROR }, 1n)
    const { extraction } = prepareChatRow(message)
    if (extraction.kind !== 'row' || extraction.row.kind !== 'divider')
      throw new Error('The native turn end did not produce a divider.')
    expect(extraction.row.divider.label).toBe(`Turn failed — 400: {"type":"invalid_request_error","code":"invalid_request_error","message":"${LETTA_MODEL_ERROR_MARKER}"}`)
    expect(extraction.row.divider.isError).toBe(true)
  })

  /** The text that the transcript draws for a mixed notification row. */
  function noticeText(): string {
    const message = makeMessage({ agentProvider: AgentProvider.LETTA, content: wrapContent(lettaMixedErrorThread()) })
    const { extraction } = prepareChatRow(message)
    if (extraction.kind !== 'row' || extraction.row.kind !== 'notification')
      throw new Error('The stored thread is not a notification row.')
    return elementText(renderNotificationBlocks(flattenNotificationEntries(extraction.row.thread.entries)))
  }

  // The browser reads a row through the same preparation as the transcript. The marker
  // sits in the loop errors, behind the snapshot that opens the stored thread.
  it('draws the native error that a stored thread holds after its snapshot', () => {
    expect(noticeText()).toContain(LETTA_MODEL_ERROR_MARKER)
  })

  it('draws no raw snapshot beside the native error', () => {
    expect(noticeText()).not.toContain('update_subagent_state')
  })

  it('reads a notice through the notification reader that a direct plugin supplies', () => {
    const loopError = lettaLoopError(LETTA_MODEL_ERROR_MARKER, true)
    const read = pluginFor(AgentProvider.LETTA)?.transcript.notificationEntry

    expect(read).toBeTypeOf('function')
    expect(read?.(loopError)).toEqual([{ kind: 'text', text: `400: {"type":"invalid_request_error","code":"invalid_request_error","message":"${LETTA_MODEL_ERROR_MARKER}"}` }])
  })
})

describe('lettaPlugin native manual compaction', () => {
  it('identifies starts and successful context rewrites without inventing token sizes', () => {
    const plugin = pluginFor(AgentProvider.LETTA)
    const start = { message_type: 'slash_command_start', command_id: 'compact', input: '/compact' }
    const end = { ...start, message_type: 'slash_command_end', success: true, output: 'Compaction completed. Message buffer length reduced from 20 to 2.' }
    expect(plugin?.transcript.compactionStart?.(start)).toBe(true)
    expect(plugin?.transcript.compactionStart?.({ ...start, input: '/compact help' })).toBe(false)
    expect(plugin?.session?.compactionBoundaryFromMessage?.(input(end))).toEqual({ trigger: 'manual' })
    expect(plugin?.session?.compactionBoundaryFromMessage?.(input({ ...end, success: false }))).toBeNull()
    expect(plugin?.session?.compactionBoundaryFromMessage?.(input({ ...end, input: '/compact help' }))).toBeNull()
    expect(plugin?.session?.compactionBoundaryFromMessage?.(input({ ...end, success: 'true' }))).toBeNull()
  })
})
