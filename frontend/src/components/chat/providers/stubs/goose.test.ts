import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { getProviderPlugin } from '../registry'
import { input, model, option, optionGroup } from '../testUtils'

import './goose'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

const MODE_AUTO = 'auto'
const MODE_APPROVE = 'approve'
const MODE_SMART_APPROVE = 'smart_approve'
const MODE_CHAT = 'chat'

describe('goose provider', () => {
  const plugin = getProviderPlugin(AgentProvider.GOOSE)!

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

  it('builds an ACP cancel request for interrupt', () => {
    expect(plugin.buildInterruptContent?.('session-1')).toBe(JSON.stringify({
      jsonrpc: '2.0',
      method: 'session/cancel',
      params: { sessionId: 'session-1' },
    }))
  })

  it('uses auto as bypass permission mode', () => {
    expect(plugin.bypassPermissionMode).toBe(MODE_AUTO)
  })

  it('changes permission mode through UpdateAgentSettings', async () => {
    await plugin.changePermissionMode?.('worker-1', 'agent-1', MODE_APPROVE)
    expect(workerRpc.updateAgentSettings).toHaveBeenCalledWith('worker-1', {
      agentId: 'agent-1',
      settings: { permissionMode: MODE_APPROVE },
    })
  })
})

describe('goose settings panel', () => {
  const plugin = getProviderPlugin(AgentProvider.GOOSE)!

  it('renders runtime modes and updates through permission-mode callback', async () => {
    const onPermissionModeChange = vi.fn()
    render(() => plugin.SettingsPanel!({
      model: 'fast-model',
      permissionMode: MODE_AUTO,
      availableModels: [
        model('default-model', 'Default Model'),
        model('fast-model', 'Fast Model', { isDefault: true }),
      ],
      availableOptionGroups: [optionGroup('permissionMode', 'Mode', [
        option(MODE_AUTO, 'Auto', { isDefault: true }),
        option(MODE_APPROVE, 'Approve'),
        option(MODE_SMART_APPROVE, 'Smart Approve'),
        option(MODE_CHAT, 'Chat'),
      ])],
      onPermissionModeChange,
    }))

    expect(screen.getByText('Mode')).toBeInTheDocument()
    expect(screen.getByTestId(`permission-mode-${MODE_APPROVE}`)).toBeInTheDocument()

    await fireEvent.click(screen.getByDisplayValue(MODE_APPROVE))
    expect(onPermissionModeChange).toHaveBeenCalledWith(MODE_APPROVE)
  })

  it('includes the selected mode in the trigger label', () => {
    render(() => plugin.settingsTriggerLabel!({
      model: 'fast-model',
      permissionMode: MODE_APPROVE,
      availableModels: [model('fast-model', 'Fast Model', { isDefault: true })],
      availableOptionGroups: [optionGroup('permissionMode', 'Mode', [
        option(MODE_AUTO, 'Auto', { isDefault: true }),
        option(MODE_APPROVE, 'Approve'),
      ])],
    }))

    expect(screen.getByText('Fast Model \u00B7 Approve')).toBeInTheDocument()
  })
})
