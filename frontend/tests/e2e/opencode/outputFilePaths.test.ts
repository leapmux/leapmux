import { describe, expect, it } from 'vitest'
import { READER_CALL_ID, READER_PREVIEW, runNativeOutputReaderCases } from '../helpers/nativeOutputReaderCases'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { checkNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { openCodeTailWindowMarkers, readOpenCodeNativeOutput } from './outputFilePaths'

const path = '/native/tool-output/tool_abc123'
const frame = {
  sessionUpdate: 'tool_call_update',
  toolCallId: READER_CALL_ID,
  status: 'completed',
  rawOutput: {
    output: READER_PREVIEW,
    metadata: {
      truncated: true,
      outputPath: path,
    },
  },
}

describe('readOpenCodeNativeOutput', () => {
  runNativeOutputReaderCases({ read: readOpenCodeNativeOutput, frame, path, pointerError: 'The native OpenCode result has no exact filesystem output pointer.' })
})

describe('openCodeTailWindowMarkers', () => {
  const output = computedNativeToolOutput({ prefix: 'TAIL', lineCount: 10, padding: 0 })
  const lines = output.text.split('\n')
  /** The preview that OpenCode writes for a cut output: its notice, then the last lines that it captured. */
  const preview = (from: number, to: number) => `...output truncated...\nFull output saved to: ${path}\n${lines.slice(from, to).join('\n')}`

  it('accepts every tail window that a cut output can leave, the middle line and a lost end included', () => {
    for (const [from, to] of [[3, 7], [5, 9], [8, 11], [1, 2]]) {
      const receipt = { paths: [path], previewText: preview(from!, to!) }
      expect(checkNativeOutputReceipt(receipt, output, openCodeTailWindowMarkers(output))).toEqual({ path, previewMarkers: ['TAIL-line-'], absentMarker: 'TAIL-line-0:', rowAbsentMarkers: ['TAIL-line-0:'] })
    }
  })

  it('refuses a preview that holds the first line, which no cut output keeps', () => {
    expect(() => checkNativeOutputReceipt({ paths: [path], previewText: preview(0, 4) }, output, openCodeTailWindowMarkers(output))).toThrow('holds the absent line')
  })
})
