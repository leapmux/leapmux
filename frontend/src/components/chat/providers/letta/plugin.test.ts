import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { LETTA_MODEL_ERROR_MARKER, lettaLoopError, lettaModelErrorThread } from '~/test-support/lettaFixtures'
import { makeMessage, wrapContent } from '~/test-support/messageFactory'
import { elementText } from '~/test-support/messageRenderProbes'
import { flattenNotificationEntries } from '../../notificationEntries'
import { renderNotificationBlocks } from '../../notificationRenderers'
import { prepareChatRow } from '../../rowPreparation'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { pluginFor } from '../registry'
import './plugin'

describe('lettaPlugin', () => {
  it('declares the permission-mode axis that the status bar draws', () => {
    const plugin = pluginFor(AgentProvider.LETTA)
    // The status bar draws a mode chip ONLY for the axis the plugin names.
    // Omitting it hid the mode the session was running behind the group label.
    expect(plugin?.configuration?.triggerModeGroupKey).toBe(OPTION_ID_PERMISSION_MODE)
  })
})

describe('lettaPlugin a failed model request', () => {
  /** The text that the transcript draws for the stored thread of one failed turn. */
  function failedTurnText(): string {
    const message = makeMessage({ agentProvider: AgentProvider.LETTA, content: wrapContent(lettaModelErrorThread()) })
    const { extraction } = prepareChatRow(message)
    if (extraction.kind !== 'row' || extraction.row.kind !== 'notification')
      throw new Error('The stored thread is not a notification row.')
    return elementText(renderNotificationBlocks(flattenNotificationEntries(extraction.row.thread.entries)))
  }

  // The browser reads a row through the same preparation as the transcript. The marker
  // sits in the loop errors, behind the snapshot that opens the stored thread.
  it('draws the native error that a stored thread holds after its snapshot', () => {
    expect(failedTurnText()).toContain(LETTA_MODEL_ERROR_MARKER)
  })

  it('draws no raw snapshot beside the native error', () => {
    expect(failedTurnText()).not.toContain('update_subagent_state')
  })

  it('reads a notice through the notification reader that a direct plugin supplies', () => {
    const loopError = lettaLoopError(LETTA_MODEL_ERROR_MARKER, true)
    const read = pluginFor(AgentProvider.LETTA)?.transcript.notificationEntry

    expect(read).toBeTypeOf('function')
    expect(read?.(loopError)).toEqual([{ kind: 'text', text: loopError.message }])
  })
})
