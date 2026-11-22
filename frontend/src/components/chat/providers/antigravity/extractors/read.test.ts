import { describe, expect, it } from 'vitest'
import { antigravityReadImages, antigravityReadRequest, antigravityReadResult } from './read'
import { antigravityStep } from './toolCommon'

describe('antigravity read extraction', () => {
  const raw = {
    type: 'CORTEX_STEP_TYPE_VIEW_FILE',
    status: 'CORTEX_STEP_STATUS_DONE',
    metadata: {
      toolCall: { id: 'read', name: 'read_file', argumentsJson: '{"Path":"/p/a.ts"}' },
    },
    viewFile: {
      absolutePathUri: 'file:///p/a.ts',
      content: 'Showing lines 1-2 of 2\n1: alpha\n2: beta',
      binaryData: { base64Data: 'aGVsbG8=', mimeType: 'image/png', uri: 'file:///p/a.ts' },
    },
  }

  it('strips the range header and line prefixes', () => {
    const step = antigravityStep(raw)!
    expect(antigravityReadRequest(step)).toEqual({ path: '/p/a.ts' })
    expect(antigravityReadResult(step)).toEqual({
      lines: [
        { num: 1, text: 'alpha' },
        { num: 2, text: 'beta' },
      ],
      fallbackContent: '1: alpha\n2: beta',
    })
  })

  it.each(['\n', '\r\n'])('shows the file line below the native header with separator %j', (separator) => {
    const step = antigravityStep({
      ...raw,
      viewFile: {
        ...raw.viewFile,
        content: [
          'Created At: 2026-11-22T05:38:41+09:00',
          'Completed At: 2026-11-22T05:38:41+09:00',
          'Showing lines 1-1 of 1',
          '1: Created At: a line in the file',
        ].join(separator),
      },
    })!
    expect(antigravityReadResult(step)).toEqual({
      lines: [
        { num: 1, text: 'Created At: a line in the file' },
      ],
      fallbackContent: '1: Created At: a line in the file',
    })
  })

  it('keeps timestamp-like output when there is no native range header', () => {
    const content = 'Created At: report metadata\nThe file is unavailable.'
    const step = antigravityStep({
      ...raw,
      viewFile: { ...raw.viewFile, content },
    })!
    expect(antigravityReadResult(step)).toEqual({ lines: null, fallbackContent: content })
  })

  it('returns an empty result for an empty native view', () => {
    const step = antigravityStep({
      ...raw,
      viewFile: {
        ...raw.viewFile,
        content: 'Showing lines 1-0 of 0\n',
      },
    })!
    expect(antigravityReadResult(step)).toEqual({ lines: [], fallbackContent: '' })
  })

  it('retains native text when a result line has no number', () => {
    const step = antigravityStep({
      ...raw,
      viewFile: {
        ...raw.viewFile,
        content: 'Showing lines 1-1 of 1\nnative output',
      },
    })!
    expect(antigravityReadResult(step)).toEqual({ lines: null, fallbackContent: 'native output' })
  })

  it('reads native image bytes without throwing on an empty payload', () => {
    expect(antigravityReadImages(antigravityStep(raw)!).map(image => image.data)).toEqual(['aGVsbG8='])
    expect(antigravityReadImages(antigravityStep({ type: 'CORTEX_STEP_TYPE_VIEW_FILE' })!)).toEqual([])
  })

  it('reads image bytes from the native media field', () => {
    const step = antigravityStep({
      ...raw,
      metadata: {
        toolCall: { id: 'read', name: 'read_file', argumentsJson: '{"Path":"/p/image.png"}' },
      },
      viewFile: {
        absolutePathUri: 'file:///p/image.png',
        mediaData: { inlineData: 'aGVsbG8=', mimeType: 'image/png', uri: 'file:///p/image.png' },
      },
    })!
    expect(antigravityReadImages(step).map(image => image.data)).toEqual(['aGVsbG8='])
    expect(antigravityReadRequest(step)).toEqual({ path: '/p/image.png' })
  })
})

describe('antigravityReadRequest', () => {
  it('preserves the host of a native file URI', () => {
    expect(antigravityReadRequest(antigravityStep({
      type: 'CORTEX_STEP_TYPE_VIEW_FILE',
      viewFile: { absolutePathUri: 'file://server/share/My%20File.txt' },
    })!).path).toBe('//server/share/My File.txt')
  })
})
