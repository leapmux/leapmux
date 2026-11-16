import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../../testUtils'
import { museOutputFilePaths } from './outputFilePaths'

function frame(path: unknown) {
  return {
    method: 'item/completed',
    params: {
      sessionId: 'session',
      item: {
        itemId: 'item',
        kind: 'toolCall',
        turnId: 'turn',
        callId: 'call',
        tool: 'bash',
        status: 'completed',
        visibleOutput: 'Native preview',
        outputRef: {
          id: 'opaque-output',
          uri: 'native://opaque-output',
          kind: 'attachment',
          mediaType: 'future/media',
          path,
        },
      },
    },
  }
}

function extraction(own: Record<string, unknown>): RowExtractionInput {
  return {
    resolved: input(own, undefined, AgentProvider.MUSE_CODE),
    category: { kind: 'tool_result' },
    span: {
      request: undefined,
      role: 'result',
      visibleRows: { request: false },
    },
  }
}

describe('museOutputFilePaths', () => {
  it('keeps a reported path and the native preview without reading the file', () => {
    const own = frame('/native/output.txt')
    const original = structuredClone(own)
    expect(museOutputFilePaths(extraction(own))).toEqual(['/native/output.txt'])
    expect(own).toEqual(original)
  })

  it.each([undefined, null, '', 0, false, {}, []].map(path => ({ path })))('keeps an absent or malformed path empty $path', ({ path }) => {
    expect(museOutputFilePaths(extraction(frame(path)))).toEqual([])
  })

  it('does not derive a path from an opaque output ID or URI', () => {
    const own = frame(undefined)
    own.params.item.outputRef = { ...own.params.item.outputRef, id: 'opaque-id', uri: 'native://opaque-id' }
    expect(museOutputFilePaths(extraction(own))).toEqual([])
  })
})
