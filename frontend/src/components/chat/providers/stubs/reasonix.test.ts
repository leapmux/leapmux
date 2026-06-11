import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input, model } from '../testUtils'

import './reasonix'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

describe('reasonix provider', () => {
  const plugin = providerFor(AgentProvider.REASONIX)!

  it('is text-only (no image/pdf/binary attachments)', () => {
    expect(plugin.attachments).toEqual({
      text: true,
      image: false,
      pdf: false,
      binary: false,
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

  it('has no runtime mode: no plan mode, permission mode, or bypass', () => {
    expect(plugin.planMode).toBeUndefined()
    expect(plugin.changePermissionMode).toBeUndefined()
    expect(plugin.defaultPermissionMode).toBeUndefined()
    expect(plugin.bypassPermissionMode).toBeUndefined()
  })
})

describe('reasonix settings panel (model-only)', () => {
  const plugin = providerFor(AgentProvider.REASONIX)!
  const models = [
    model('deepseek-flash', 'DeepSeek Flash', { isDefault: true }),
    model('deepseek-pro', 'DeepSeek Pro'),
  ]

  it('renders the model selector and no mode group', () => {
    const { container } = render(() => plugin.SettingsPanel!({
      model: 'deepseek-flash',
      permissionMode: '',
      availableModels: models,
      // Reasonix reports no option groups over ACP.
      availableOptionGroups: [],
      onChange: vi.fn(),
    }))

    expect(screen.getByTestId('model-deepseek-flash')).toBeInTheDocument()
    // No permission-mode radio group and no read-only extra groups.
    expect(container.querySelector('[data-testid^="permission-mode-"]')).toBeNull()
    expect(container.querySelector('[data-testid^="extra-"]')).toBeNull()
  })

  it('shows only the model in the trigger label (no trailing separator)', () => {
    render(() => plugin.settingsTriggerLabel!({
      model: 'deepseek-flash',
      permissionMode: '',
      availableModels: models,
      availableOptionGroups: [],
    }))

    const label = screen.getByText('DeepSeek Flash')
    expect(label).toBeInTheDocument()
    expect(label.textContent).not.toContain('·')
  })
})
