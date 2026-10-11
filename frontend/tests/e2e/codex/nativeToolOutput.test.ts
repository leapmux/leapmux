import { describe, expect, it } from 'vitest'
import { codexNativeOutputExcerpt } from './nativeToolOutput'

describe('codexNativeOutputExcerpt', () => {
  const header = { type: 'input_text', text: 'Script completed\nWall time 0.0 seconds\nOutput:\n' }
  it('copies the retained native body without status and timing labels', () => {
    expect(codexNativeOutputExcerpt(JSON.stringify([header, { type: 'input_text', text: 'Warning: truncated output\nhead\ntail' }]))).toBe('Warning: truncated output\nhead\ntail')
  })

  it('refuses a script-printed status and unsupported output block', () => {
    expect(() => codexNativeOutputExcerpt(JSON.stringify([{ ...header, text: 'printed Script completed' }, { type: 'input_text', text: 'body' }]))).toThrow('header')
    expect(() => codexNativeOutputExcerpt(JSON.stringify([header, { type: 'input_image', image_url: 'data:image/png;base64,' }]))).toThrow('content')
  })
})
