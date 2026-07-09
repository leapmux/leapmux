import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { describeACPStubBasics } from './stubBasics'

import './cursor'

describe('cursor provider', () => {
  const plugin = providerFor(AgentProvider.CURSOR)!

  // Cursor's attachment caps, agent_message_chunk classification, config_option_update hiding,
  // and ACP interrupt request are the standard stub behaviours (interrupt is wired unconditionally
  // by registerACPProvider, so routing through the helper also covers it).
  describeACPStubBasics(plugin, { text: true, image: true, pdf: true, binary: true })

  it('maps plan mode to agent/plan values', () => {
    expect(plugin.planMode?.currentMode({ optionValues: { permissionMode: 'plan' } })).toBe('plan')
    expect(plugin.planMode?.currentMode({ optionValues: { permissionMode: '' } })).toBe('agent')
  })

  it('recognizes cursor ask-question control payloads', () => {
    expect(plugin.isAskUserQuestion?.({ method: 'cursor/ask_question' })).toBe(true)
    expect(plugin.isAskUserQuestion?.({ method: 'cursor/create_plan' })).toBe(false)
  })

  it('declares plan mode on the permissionMode group and defaults to agent', () => {
    // The generic settings panel renders the permissionMode group Cursor reports;
    // the provider only declares the plan-mode mapping and its default mode.
    expect(plugin.planMode).toMatchObject({
      groupKey: 'permissionMode',
      planValue: 'plan',
      defaultValue: 'agent',
    })
  })

  it('renders the permissionMode group as the trigger mode segment', () => {
    expect(plugin.triggerModeGroupKey).toBe('permissionMode')
  })

  // The neutral {isSynthetic, controlResponse} row -> control_response classification is provider-
  // agnostic and lives in classifyMessage (see messageClassification.test.ts); this covers only
  // Cursor's own controlResponseDisplay derivation.
  it('derives Cursor-specific control-response labels', () => {
    expect(plugin.controlResponseDisplay!({
      provider: 'CURSOR',
      requestId: '7',
      request: { method: 'cursor/create_plan' },
      response: { result: { outcome: { outcome: 'accepted' } } },
    })).toEqual({ kind: 'label', text: 'Accept' })
  })
})
