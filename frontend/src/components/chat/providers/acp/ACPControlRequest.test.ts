import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import { ACPControlActions, sendACPPermissionResponse } from './ACPControlRequest'

describe('sendACPPermissionResponse', () => {
  it('sends an ACP permission response with the selected option', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)

    await sendACPPermissionResponse(onRespond, '7', 'proceed_once')

    expect(onRespond).toHaveBeenCalledTimes(1)
    const [content] = onRespond.mock.calls[0]
    const parsed = JSON.parse(new TextDecoder().decode(content))
    expect(parsed).toEqual({
      jsonrpc: '2.0',
      id: 7,
      result: {
        outcome: {
          outcome: 'selected',
          optionId: 'proceed_once',
        },
      },
    })
  })

  it('bypass button sends the allow option and flips permission mode', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const applyBypass = vi.fn()

    render(() => ACPControlActions({
      request: {
        agentId: 'agent1',
        requestId: '7',
        payload: {
          params: {
            options: [
              { optionId: 'proceed_once', name: 'Allow once', kind: 'allow_once' },
              { optionId: 'cancel', name: 'Deny', kind: 'reject_once' },
            ],
          },
        },
      },
      onRespond,
      answerState: createControlAnswerState(),
      hasEditorContent: false,
      onTriggerSend: vi.fn(),
      bypass: { settings: { sets: { permissionMode: 'yolo' } }, apply: applyBypass },
    }))

    await fireEvent.click(screen.getByTestId('control-bypass-btn'))

    expect(onRespond).toHaveBeenCalledTimes(1)
    const [content] = onRespond.mock.calls[0]
    const parsed = JSON.parse(new TextDecoder().decode(content))
    expect(parsed.result.outcome.optionId).toBe('proceed_once')
    expect(applyBypass).toHaveBeenCalledWith({ sets: { permissionMode: 'yolo' } })
  })
})
