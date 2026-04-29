import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input, model, option, optionGroup } from '../testUtils'

import './cursor'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

describe('cursor provider', () => {
  const plugin = providerFor(AgentProvider.CURSOR)!

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
  const plugin = providerFor(AgentProvider.CURSOR)!

  it('renders runtime modes and updates through the unified onChange dispatcher', async () => {
    const onChange = vi.fn()
    render(() => plugin.SettingsPanel!({
      model: 'auto',
      permissionMode: 'agent',
      availableModels: [
        model('auto', 'Auto', { isDefault: true }),
        model('gpt-5.4', 'GPT-5.4'),
      ],
      availableOptionGroups: [optionGroup('permissionMode', 'Mode', [
        option('agent', 'Agent', { isDefault: true }),
        option('plan', 'Plan'),
        option('ask', 'Ask'),
      ])],
      onChange,
    }))

    expect(screen.getByText('Mode')).toBeInTheDocument()
    await fireEvent.click(screen.getByDisplayValue('plan'))
    expect(onChange).toHaveBeenCalledWith({ kind: 'permissionMode', value: 'plan' })
  })

  it('includes the selected mode in the trigger label', () => {
    render(() => plugin.settingsTriggerLabel!({
      model: 'auto',
      permissionMode: 'plan',
      availableModels: [model('auto', 'Auto', { isDefault: true })],
      availableOptionGroups: [optionGroup('permissionMode', 'Mode', [
        option('agent', 'Agent', { isDefault: true }),
        option('plan', 'Plan'),
      ])],
    }))

    expect(screen.getByText('Auto \u00B7 Plan')).toBeInTheDocument()
  })
})
