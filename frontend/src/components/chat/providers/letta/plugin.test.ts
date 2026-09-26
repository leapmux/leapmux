import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
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
