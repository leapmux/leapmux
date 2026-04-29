import { describe, expect, it } from 'vitest'
import {
  piCancelResponse,
  piConfirmResponse,
  piValueResponse,
  sendPiExtensionResponse,
} from './controlResponse'

describe('pi controlResponse helpers', () => {
  it('builds value responses for select / input / editor', () => {
    expect(piValueResponse('req-1', 'Allow')).toEqual({
      type: 'extension_ui_response',
      id: 'req-1',
      value: 'Allow',
    })
  })

  it('builds confirm responses with confirmed=true', () => {
    expect(piConfirmResponse('req-2', true)).toEqual({
      type: 'extension_ui_response',
      id: 'req-2',
      confirmed: true,
    })
  })

  it('builds confirm responses with confirmed=false', () => {
    expect(piConfirmResponse('req-2', false)).toEqual({
      type: 'extension_ui_response',
      id: 'req-2',
      confirmed: false,
    })
  })

  it('builds cancellation responses', () => {
    expect(piCancelResponse('req-3')).toEqual({
      type: 'extension_ui_response',
      id: 'req-3',
      cancelled: true,
    })
  })

  it('serializes responses through onRespond as UTF-8 JSON', async () => {
    let captured: Uint8Array | null = null
    await sendPiExtensionResponse('agent', async (_id, content) => {
      captured = content
    }, piValueResponse('req-1', 'Allow'))

    expect(captured).not.toBeNull()
    const text = new TextDecoder().decode(captured!)
    const parsed = JSON.parse(text)
    expect(parsed).toEqual({ type: 'extension_ui_response', id: 'req-1', value: 'Allow' })
  })
})
