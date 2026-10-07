import { describe, expect, it } from 'vitest'
import { nativeOutputSnapshot, READER_CALL_ID, READER_PREVIEW, runNativeOutputReaderCases } from '../helpers/nativeOutputReaderCases'
import { readDroidNativeOutput } from './outputFilePaths'

const path = '/native/droid-terminal-current/12345678-1234-1234-1234-123456789abc.log'
const frame = {
  type: 'tool_result',
  toolUseId: READER_CALL_ID,
  messageId: 'message-current',
  content: `${READER_PREVIEW}\nFull command output saved to: ${path} (64 KB)`,
  isError: false,
}

describe('readDroidNativeOutput', () => {
  // Droid stores a tool result in a span of its own, whose ID holds the call ID.
  runNativeOutputReaderCases({ read: readDroidNativeOutput, frame, path, spanId: `droid-tool-${READER_CALL_ID}`, pointerError: 'The native Droid result has no unique terminal output path.' })

  it('keeps the original packet while the copied preview omits the exit trailer', () => {
    const captured = { ...frame, content: `${frame.content}\n\n[Process exited with code 0]` }
    const receipt = readDroidNativeOutput(nativeOutputSnapshot([{ frame: captured, spanId: `droid-tool-${READER_CALL_ID}` }]), READER_CALL_ID)
    expect(receipt.frame).toEqual(captured)
    expect(receipt.previewText).toBe(frame.content)
  })

  it('omits a matching native failure preamble without removing the output pointer', () => {
    const captured = { ...frame, isError: true, content: `Error: Command failed (exit code: 7)\n${frame.content}\n\n[Process exited with code 7]` }
    const receipt = readDroidNativeOutput(nativeOutputSnapshot([{ frame: captured, spanId: `droid-tool-${READER_CALL_ID}` }]), READER_CALL_ID)
    expect(receipt.previewText).toBe(frame.content)
    expect(receipt.paths).toEqual([path])
  })
})
