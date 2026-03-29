import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider, AvailableModelSchema, AvailableOptionGroupSchema, AvailableOptionSchema } from '~/generated/leapmux/v1/agent_pb'
import { getProviderPlugin } from './registry'
import { input } from './testUtils'

import './copilot'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

const MODE_AGENT = 'https://agentclientprotocol.com/protocol/session-modes#agent'
const MODE_PLAN = 'https://agentclientprotocol.com/protocol/session-modes#plan'
const MODE_AUTOPILOT = 'https://agentclientprotocol.com/protocol/session-modes#autopilot'

describe('copilot provider', () => {
  const plugin = getProviderPlugin(AgentProvider.COPILOT_CLI)!

  it('exposes attachment capabilities', () => {
    expect(plugin.attachments).toEqual({
      text: true,
      image: true,
      pdf: true,
      binary: true,
    })
  })

  it('classifies agent_message_chunk as assistant_text', () => {
    const parent = {
      sessionUpdate: 'agent_message_chunk',
      content: { type: 'text', text: 'Hello' },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'assistant_text' })
  })

  it('hides config_option_update', () => {
    const parent = {
      sessionUpdate: 'config_option_update',
      configOptions: [],
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('maps plan mode to the ACP URI value', () => {
    expect(plugin.planMode?.currentMode({ permissionMode: MODE_PLAN })).toBe(MODE_PLAN)
    expect(plugin.planMode?.currentMode({ permissionMode: '' })).toBe(MODE_AGENT)
  })

  it('builds an ACP cancel request for interrupt', () => {
    expect(plugin.buildInterruptContent?.('session-1')).toBe(JSON.stringify({
      jsonrpc: '2.0',
      method: 'session/cancel',
      params: { sessionId: 'session-1' },
    }))
  })

  it('uses autopilot as bypass permission mode', () => {
    expect(plugin.bypassPermissionMode).toBe(MODE_AUTOPILOT)
  })

  it('changes permission mode through UpdateAgentSettings', async () => {
    await plugin.changePermissionMode?.('worker-1', 'agent-1', MODE_PLAN)
    expect(workerRpc.updateAgentSettings).toHaveBeenCalledWith('worker-1', {
      agentId: 'agent-1',
      settings: { permissionMode: MODE_PLAN },
    })
  })
})

describe('copilot settings panel', () => {
  const plugin = getProviderPlugin(AgentProvider.COPILOT_CLI)!

  it('renders runtime modes and updates through permission-mode callback', async () => {
    const onPermissionModeChange = vi.fn()
    render(() => plugin.SettingsPanel!({
      model: 'gpt-5.4-mini',
      permissionMode: MODE_AGENT,
      availableModels: [
        create(AvailableModelSchema, { id: 'gpt-5.4', displayName: 'GPT-5.4' }),
        create(AvailableModelSchema, { id: 'gpt-5.4-mini', displayName: 'GPT-5.4 mini', isDefault: true }),
      ],
      availableOptionGroups: [create(AvailableOptionGroupSchema, {
        key: 'permissionMode',
        label: 'Mode',
        options: [
          create(AvailableOptionSchema, { id: MODE_AGENT, name: 'Agent', isDefault: true }),
          create(AvailableOptionSchema, { id: MODE_PLAN, name: 'Plan' }),
          create(AvailableOptionSchema, { id: MODE_AUTOPILOT, name: 'Autopilot' }),
        ],
      })],
      onPermissionModeChange,
    }))

    expect(screen.getByText('Mode')).toBeTruthy()
    expect(screen.getByTestId(`permission-mode-${MODE_PLAN}`)).toBeTruthy()

    await fireEvent.click(screen.getByDisplayValue(MODE_PLAN))
    expect(onPermissionModeChange).toHaveBeenCalledWith(MODE_PLAN)
  })

  it('includes the selected mode in the trigger label', () => {
    render(() => plugin.settingsTriggerLabel!({
      model: 'gpt-5.4-mini',
      permissionMode: MODE_PLAN,
      availableModels: [create(AvailableModelSchema, { id: 'gpt-5.4-mini', displayName: 'GPT-5.4 mini', isDefault: true })],
      availableOptionGroups: [create(AvailableOptionGroupSchema, {
        key: 'permissionMode',
        label: 'Mode',
        options: [
          create(AvailableOptionSchema, { id: MODE_AGENT, name: 'Agent', isDefault: true }),
          create(AvailableOptionSchema, { id: MODE_PLAN, name: 'Plan' }),
        ],
      })],
    }))

    expect(screen.getByText('GPT-5.4 mini \u00B7 Plan')).toBeTruthy()
  })
})
