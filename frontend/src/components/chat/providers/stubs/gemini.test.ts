import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { getProviderPlugin } from '../registry'
import { input, model, option, optionGroup } from '../testUtils'

import './gemini'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

describe('gemini provider', () => {
  const plugin = getProviderPlugin(AgentProvider.GEMINI_CLI)!

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

  it('classifies tool_call_update cancelled as tool_use', () => {
    const parent = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      status: 'cancelled',
      kind: 'execute',
    }
    expect(plugin.classify(input(parent))).toEqual({
      kind: 'tool_use',
      toolName: 'execute',
      toolUse: parent,
      content: [],
    })
  })

  it('maps plan mode to permission mode', () => {
    expect(plugin.planMode?.currentMode({ permissionMode: 'plan' })).toBe('plan')
    expect(plugin.planMode?.currentMode({ permissionMode: '' })).toBe('default')
  })

  it('builds a Gemini cancel request for interrupt', () => {
    expect(plugin.buildInterruptContent?.('session-1')).toBe(JSON.stringify({
      jsonrpc: '2.0',
      method: 'session/cancel',
      params: { sessionId: 'session-1' },
    }))
  })

  it('uses yolo as bypass permission mode', () => {
    expect(plugin.bypassPermissionMode).toBe('yolo')
  })

  it('changes permission mode through UpdateAgentSettings', async () => {
    await plugin.changePermissionMode?.('worker-1', 'agent-1', 'plan')
    expect(workerRpc.updateAgentSettings).toHaveBeenCalledWith('worker-1', {
      agentId: 'agent-1',
      settings: { permissionMode: 'plan' },
    })
  })
})

describe('gemini settings panel', () => {
  const plugin = getProviderPlugin(AgentProvider.GEMINI_CLI)!

  it('renders permission modes and updates via permission-mode callback', async () => {
    const onPermissionModeChange = vi.fn()
    render(() => plugin.SettingsPanel!({
      model: 'gemini-2.5-pro',
      permissionMode: 'default',
      availableModels: [
        model('auto', 'Auto', { isDefault: true }),
        model('gemini-2.5-pro', 'Gemini 2.5 Pro'),
      ],
      availableOptionGroups: [optionGroup('permissionMode', 'Permission Mode', [
        option('default', 'Default', { isDefault: true }),
        option('plan', 'Plan'),
        option('yolo', 'YOLO'),
      ])],
      onPermissionModeChange,
    }))

    expect(screen.getByText('Permission Mode')).toBeInTheDocument()
    expect(screen.getByTestId('permission-mode-default')).toBeInTheDocument()
    expect(screen.getByTestId('permission-mode-plan')).toBeInTheDocument()

    await fireEvent.click(screen.getByDisplayValue('plan'))
    expect(onPermissionModeChange).toHaveBeenCalledWith('plan')
  })

  it('includes the selected permission mode in the trigger label', () => {
    render(() => plugin.settingsTriggerLabel!({
      model: 'gemini-2.5-pro',
      permissionMode: 'plan',
      availableModels: [model('gemini-2.5-pro', 'Gemini 2.5 Pro', { isDefault: true })],
      availableOptionGroups: [optionGroup('permissionMode', 'Permission Mode', [
        option('default', 'Default', { isDefault: true }),
        option('plan', 'Plan'),
      ])],
    }))

    expect(screen.getByText('Gemini 2.5 Pro \u00B7 Plan')).toBeInTheDocument()
  })
})
