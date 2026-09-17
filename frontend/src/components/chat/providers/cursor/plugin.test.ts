import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createControlAnswerState } from '../../controls/types'
import { describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'

import './plugin'

describe('cursor provider', () => {
  const plugin = providerFor(AgentProvider.CURSOR)!

  // Cursor's attachment caps, assembled-text handling, config_option_update hiding,
  // and ACP interrupt request are the standard stub behaviours (interrupt is wired unconditionally
  // by registerACPProvider, so routing through the helper also covers it).
  describeACPProviderBasics(AgentProvider.CURSOR, { text: true, image: true, pdf: true, binary: true })

  it('maps plan mode to agent/plan values', () => {
    expect(plugin?.configuration?.planMode?.currentMode({ optionValues: { permissionMode: 'plan' } })).toBe('plan')
    expect(plugin?.configuration?.planMode?.currentMode({ optionValues: { permissionMode: '' } })).toBe('agent')
  })

  it('recognizes cursor ask-question control payloads', () => {
    expect(plugin?.controls?.askUserQuestion?.isRequest({ method: 'cursor/ask_question' })).toBe(true)
    expect(plugin?.controls?.askUserQuestion?.isRequest({ method: 'cursor/create_plan' })).toBe(false)
  })

  it('declares plan mode on the permissionMode group and defaults to agent', () => {
    // The generic settings panel renders the permissionMode group Cursor reports;
    // the provider only declares the plan-mode mapping and its default mode.
    expect(plugin?.configuration?.planMode).toMatchObject({
      groupKey: 'permissionMode',
      planValue: 'plan',
      defaultValue: 'agent',
    })
  })

  it('renders the permissionMode group as the trigger mode segment', () => {
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('permissionMode')
  })

  it('uses Oat small metrics for create-plan decisions', () => {
    const payload = { method: 'cursor/create_plan', params: {} }
    // Cursor claims its create-plan and nothing else. A permission beside it takes
    // the shared decision row, and its ask-question takes the shared question form --
    // a wider claim would answer a question with a plan verdict.
    expect(plugin?.controls?.controlActionsFor!({ method: 'session/request_permission', params: {} })).toBeUndefined()
    expect(plugin?.controls?.controlActionsFor!({ method: 'cursor/ask_question', params: {} })).toBeUndefined()
    render(() => plugin?.controls?.controlActionsFor!(payload)!({
      request: {
        requestId: 'cursor-plan-1',
        agentId: 'cursor-1',
        payload,
      },
      answerState: createControlAnswerState(),
      onRespond: async () => {},
      hasEditorContent: false,
      onTriggerSend: () => {},
    }))

    expect(screen.getByTestId('control-deny-btn')).toHaveClass('outline', compactControl)
    expect(screen.getByTestId('control-allow-btn')).toHaveClass(compactControl)
    expect(screen.getByTestId('control-allow-btn')).not.toHaveClass('outline')
  })

  // The neutral {isSynthetic, controlResponse} row -> control_response classification is provider-
  // agnostic and lives in classifyMessage (see messageClassification.test.ts); this covers only
  // Cursor's own controlResponseDisplay derivation.
  it('derives Cursor-specific control-response labels', () => {
    expect(plugin?.controls?.controlResponseDisplay!({
      claimToken: 'claim-1',
      requestId: '7',
      request: { method: 'cursor/create_plan' },
      response: { result: { outcome: { outcome: 'accepted' } } },
    })).toEqual({ kind: 'label', text: 'Accept' })
  })
})
