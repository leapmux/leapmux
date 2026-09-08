import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { allowScopePillGroup, permissionPillGroup } from '~/test-support/controlRequests'
import { createControlAnswerState } from '../../controls/types'
import { OpenCodeControlActions } from './OpenCodeControlRequest'

function decodeOptionId(content: Uint8Array): string {
  return JSON.parse(new TextDecoder().decode(content)).result.outcome.optionId
}

describe('openCodeControlActions', () => {
  /** OpenCode's wire shape: once / always / reject, emitted allow-first. */
  function renderOptionsActions() {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const apply = vi.fn()
    render(() => OpenCodeControlActions({
      request: {
        agentId: 'agent1',
        requestId: '11',
        payload: {
          params: {
            options: [
              { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
              { optionId: 'always', kind: 'allow_always', name: 'Always allow' },
              { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
            ],
          },
        },
      },
      onRespond,
      answerState: createControlAnswerState(),
      hasEditorContent: false,
      onTriggerSend: vi.fn(),
      presets: { bypass: { sets: { permissionMode: 'yolo' } }, apply },
    }))
    return { onRespond, apply }
  }

  it('renders Deny first and moves Always allow into the Once / Always scope group', () => {
    renderOptionsActions()

    const deny = screen.getByTestId('control-deny-btn')
    const allow = screen.getByTestId('control-allow-btn')
    expect(deny.textContent).toBe('Deny')
    expect(allow.textContent).toBe('Allow')
    expect(deny.compareDocumentPosition(allow) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    const scope = allowScopePillGroup()
    expect(scope.getByRole('radio', { name: 'Once' })).toBeChecked()
    expect(scope.getByRole('radio', { name: 'Always' })).toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-always')).not.toBeInTheDocument()
  })

  it('sends once while Once is selected and always beyond it, applying the preset after each allow', async () => {
    const { onRespond, apply } = renderOptionsActions()

    // A preset applies only when the pill selects one; Unchanged applies nothing.
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[0][0])).toBe('once')
    expect(apply).not.toHaveBeenCalled()

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))

    fireEvent.click(allowScopePillGroup().getByRole('radio', { name: 'Always' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[1][0])).toBe('always')
    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'yolo' } })

    // No reject_always is offered, so Deny keeps its once option -- and never
    // applies a preset.
    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decodeOptionId(onRespond.mock.calls[2][0])).toBe('reject')
    expect(apply).toHaveBeenCalledTimes(1)
  })

  it('falls back to Deny / Allow for a payload without options', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => OpenCodeControlActions({
      request: {
        agentId: 'agent1',
        requestId: '12',
        payload: { params: {} },
      },
      onRespond,
      answerState: createControlAnswerState(),
      hasEditorContent: false,
      onTriggerSend: vi.fn(),
    }))

    expect(screen.getByTestId('control-deny-btn').textContent).toBe('Deny')
    expect(screen.getByTestId('control-allow-btn').textContent).toBe('Allow')
    expect(screen.queryByRole('radiogroup', { name: 'Allow scope' })).not.toBeInTheDocument()

    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[0][0])).toBe('once')
  })
})
