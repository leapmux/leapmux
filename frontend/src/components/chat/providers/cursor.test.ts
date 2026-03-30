import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider, AvailableModelSchema, AvailableOptionGroupSchema, AvailableOptionSchema } from '~/generated/leapmux/v1/agent_pb'
import { getProviderPlugin } from './registry'
import { input } from './testUtils'

import './cursor'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

describe('cursor provider', () => {
  const plugin = getProviderPlugin(AgentProvider.CURSOR_CLI)!

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
      content: { type: 'text', text: 'Hello Cursor' },
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

  it('maps plan mode to agent/plan values', () => {
    expect(plugin.planMode?.currentMode({ permissionMode: 'plan' })).toBe('plan')
    expect(plugin.planMode?.currentMode({ permissionMode: '' })).toBe('agent')
  })

  it('recognizes cursor ask-question control payloads', () => {
    expect(plugin.isAskUserQuestion?.({ method: 'cursor/ask_question' })).toBe(true)
    expect(plugin.isAskUserQuestion?.({ method: 'cursor/create_plan' })).toBe(false)
  })

  it('changes permission mode through UpdateAgentSettings', async () => {
    await plugin.changePermissionMode?.('worker-1', 'agent-1', 'plan')
    expect(workerRpc.updateAgentSettings).toHaveBeenCalledWith('worker-1', {
      agentId: 'agent-1',
      settings: { permissionMode: 'plan' },
    })
  })
})

describe('cursor settings panel', () => {
  const plugin = getProviderPlugin(AgentProvider.CURSOR_CLI)!

  it('renders runtime modes and updates via permission-mode callback', async () => {
    const onPermissionModeChange = vi.fn()
    render(() => plugin.SettingsPanel!({
      model: 'auto',
      permissionMode: 'agent',
      availableModels: [
        create(AvailableModelSchema, { id: 'auto', displayName: 'Auto', isDefault: true }),
        create(AvailableModelSchema, { id: 'gpt-5.4', displayName: 'GPT-5.4' }),
      ],
      availableOptionGroups: [create(AvailableOptionGroupSchema, {
        key: 'permissionMode',
        label: 'Mode',
        options: [
          create(AvailableOptionSchema, { id: 'agent', name: 'Agent', isDefault: true }),
          create(AvailableOptionSchema, { id: 'plan', name: 'Plan' }),
          create(AvailableOptionSchema, { id: 'ask', name: 'Ask' }),
        ],
      })],
      onPermissionModeChange,
    }))

    expect(screen.getByText('Mode')).toBeTruthy()
    await fireEvent.click(screen.getByDisplayValue('plan'))
    expect(onPermissionModeChange).toHaveBeenCalledWith('plan')
  })

  it('includes the selected mode in the trigger label', () => {
    render(() => plugin.settingsTriggerLabel!({
      model: 'auto',
      permissionMode: 'plan',
      availableModels: [create(AvailableModelSchema, { id: 'auto', displayName: 'Auto', isDefault: true })],
      availableOptionGroups: [create(AvailableOptionGroupSchema, {
        key: 'permissionMode',
        label: 'Mode',
        options: [
          create(AvailableOptionSchema, { id: 'agent', name: 'Agent', isDefault: true }),
          create(AvailableOptionSchema, { id: 'plan', name: 'Plan' }),
        ],
      })],
    }))

    expect(screen.getByText('Auto \u00B7 Plan')).toBeTruthy()
  })
})
