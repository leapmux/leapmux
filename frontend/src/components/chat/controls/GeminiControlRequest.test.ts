import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { GeminiControlActions, sendGeminiPermissionResponse } from './GeminiControlRequest'

describe('sendGeminiPermissionResponse', () => {
  it('sends an ACP permission response with the selected option', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)

    await sendGeminiPermissionResponse('agent1', onRespond, '7', 'proceed_once')

    expect(onRespond).toHaveBeenCalledTimes(1)
    const [, content] = onRespond.mock.calls[0]
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
    const onPermissionModeChange = vi.fn()

    render(() => GeminiControlActions({
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
      bypassPermissionMode: 'yolo',
      onPermissionModeChange,
    }))

    await fireEvent.click(screen.getByTestId('control-bypass-btn'))

    expect(onRespond).toHaveBeenCalledTimes(1)
    const [, content] = onRespond.mock.calls[0]
    const parsed = JSON.parse(new TextDecoder().decode(content))
    expect(parsed.result.outcome.optionId).toBe('proceed_once')
    expect(onPermissionModeChange).toHaveBeenCalledWith('yolo')
  })
})
