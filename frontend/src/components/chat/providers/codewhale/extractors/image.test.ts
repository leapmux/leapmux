import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { MEDIA_DATA_URI, MEDIA_DESCRIPTOR, MEDIA_OUTPUT_FILE_ID, toolCompleted } from '../toolResults.fixtures'
import { codewhaleToolContent, codewhaleToolMediaImages } from './image'
import { codewhaleToolFrame } from './toolCommon'

function frame(descriptor: unknown = MEDIA_DESCRIPTOR) {
  return codewhaleToolFrame(toolCompleted(CODEWHALE_TOOL.ReadMedia, { path: 'shot.png' }, 'The image is ready.', { tool_media: [descriptor] }))
}

const supplement = { outputFiles: { [MEDIA_OUTPUT_FILE_ID]: MEDIA_DATA_URI } }
const noData = [{ mimeType: 'image/png', dimensions: { width: 64, height: 64 } }]

describe('codewhaleToolMediaImages', () => {
  it('reads the matching recovered full tool output and preserves its dimensions', () => {
    expect(codewhaleToolMediaImages(frame(), supplement)).toEqual([{ ...noData[0], url: MEDIA_DATA_URI }])
  })

  it.each([undefined, null, {}, { outputFiles: {} }, { outputFiles: { foreign: MEDIA_DATA_URI } }, { outputFiles: { [MEDIA_OUTPUT_FILE_ID]: 42 } }, { outputFiles: { [MEDIA_OUTPUT_FILE_ID]: 'https://example.com/image.png' } }, { outputFiles: { [MEDIA_OUTPUT_FILE_ID]: 'data:image/jpeg;base64,AAAA' } }, { outputFiles: { [MEDIA_OUTPUT_FILE_ID]: 'data:image/png;base64,!' } }, { outputFiles: { [MEDIA_OUTPUT_FILE_ID]: 'data:image/png;base64,AAAA' } }])('keeps an unavailable slot for missing or invalid recovered bytes: %j', (value) => {
    expect(codewhaleToolMediaImages(frame(), value)).toEqual(noData)
  })

  it.each([
    ['version', 0],
    ['version', 2],
    ['version', '1'],
    ['session_id', ''],
    ['session_id', '../session'],
    ['session_id', 'session-é'],
    ['artifact_id', 'art_image_bad'],
    ['artifact_id', `art_image_${'z'.repeat(64)}`],
    ['tool_call_id', 'foreign'],
    ['media_type', 'image/svg+xml'],
    ['byte_size', 0],
    ['byte_size', -1],
    ['byte_size', 1.5],
    ['byte_size', 5242881],
    ['width', 0],
    ['width', 8193],
    ['height', -1],
    ['height', '64'],
    ['sha256', ''],
    ['sha256', 'A'.repeat(64)],
  ])('refuses an invalid native descriptor field %s=%j', (key, value) => {
    expect(codewhaleToolMediaImages(frame({ ...MEDIA_DESCRIPTOR, [key]: value }), supplement)).toEqual([])
  })

  it('refuses an excessive pixel count and a duplicate descriptor', () => {
    expect(codewhaleToolMediaImages(frame({ ...MEDIA_DESCRIPTOR, width: 8192, height: 8192 }), supplement)).toEqual([])
    const duplicate = codewhaleToolFrame(toolCompleted(CODEWHALE_TOOL.ReadMedia, {}, '', { tool_media: [MEDIA_DESCRIPTOR, MEDIA_DESCRIPTOR] }))
    expect(codewhaleToolMediaImages(duplicate, supplement)).toEqual([])
  })

  it('refuses a missing field, a malformed descriptor, and a non-completed frame', () => {
    for (const key of Object.keys(MEDIA_DESCRIPTOR)) {
      const descriptor: Record<string, unknown> = { ...MEDIA_DESCRIPTOR }
      delete descriptor[key]
      expect(codewhaleToolMediaImages(frame(descriptor), supplement)).toEqual([])
    }
    expect(codewhaleToolMediaImages(frame(null), supplement)).toEqual([])
    expect(codewhaleToolMediaImages(null, supplement)).toEqual([])
    const completed = frame()
    if (!completed)
      throw new Error('The valid native image fixture produced no frame.')
    expect(codewhaleToolMediaImages({ ...completed, outcome: 'failed' }, supplement)).toEqual([])
  })
})

describe('codewhaleToolContent', () => {
  it('keeps unknown native blocks and image order without duplicate images', () => {
    const images = codewhaleToolMediaImages(frame(), supplement)
    const result = codewhaleToolContent(JSON.stringify({ content: [{ type: 'text', text: 'Before.' }, { type: 'image', mimeType: 'image/png', data: '[removed]' }, { type: 'unknown-native', value: 7 }, { type: 'text', text: 'After.' }] }), images)
    expect(result.map(item => item.type)).toEqual(['text', 'image', 'unknown', 'text'])
    expect(result.filter(item => item.type === 'image')).toHaveLength(1)
  })

  it('preserves plain text and appends recovered images when the native detail contains no blocks', () => {
    expect(codewhaleToolContent('The image is ready.', noData)).toEqual([{ type: 'text', text: 'The image is ready.' }, { type: 'image', source: noData[0] }])
    expect(codewhaleToolContent('', [])).toEqual([])
  })
})

describe('codewhale native MCP content preservation', () => {
  it('keeps actual inline native bytes when no full tool output descriptor exists', () => {
    const data = MEDIA_DATA_URI.split(',')[1]
    expect(codewhaleToolContent(JSON.stringify({ content: [{ type: 'image', mimeType: 'image/png', data }] }), [])).toEqual([{ type: 'image', source: { mimeType: 'image/png', data } }])
  })

  it('keeps an unavailable slot for a removed native image payload', () => {
    expect(codewhaleToolContent(JSON.stringify({ content: [{ type: 'image', mimeType: 'image/png', data: '[MCP image payload removed from text output]' }] }), [])).toEqual([{ type: 'image', source: { mimeType: 'image/png' } }])
  })

  it('preserves structured content and unknown native result fields', () => {
    expect(codewhaleToolContent(JSON.stringify({ content: [{ type: 'text', text: 'The result.' }], structuredContent: { count: 7 }, nativeExtra: true }), [])).toEqual([{ type: 'text', text: 'The result.' }, { type: 'unknown', raw: { structuredContent: { count: 7 }, nativeExtra: true } }])
  })
})

describe('codewhale recovered base64 padding', () => {
  it('refuses nonzero padding bits before constructing an inline image URL', () => {
    const descriptor = { ...MEDIA_DESCRIPTOR, byte_size: 1 }
    expect(codewhaleToolMediaImages(frame(descriptor), { outputFiles: { [MEDIA_OUTPUT_FILE_ID]: 'data:image/png;base64,AR==' } })).toEqual(noData)
  })
})
