import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../../testUtils'
import { antigravityOutputFilePaths } from './outputFilePaths'

function extraction(own: Record<string, unknown>): RowExtractionInput {
  return {
    resolved: input(own, undefined, AgentProvider.ANTIGRAVITY_CLI),
    category: { kind: 'tool_result' },
    span: {
      request: undefined,
      result: undefined,
      role: 'result',
      visibleRows: { request: false, result: true },
    },
  }
}

describe('antigravityOutputFilePaths', () => {
  it('reads the full-output URI from a run-command preview', () => {
    expect(antigravityOutputFilePaths(extraction({
      runCommand: {
        combinedOutput: {
          full: 'The full output is at file:///private/p/output.txt.\n\nCommand output:\npreview',
        },
      },
    }))).toEqual(['/private/p/output.txt'])
  })

  it('reads a generic full-output URI and returns no path without one', () => {
    expect(antigravityOutputFilePaths(extraction({
      generic: {
        result: { fullOutputUri: 'file:///p/other.txt' },
      },
    }))).toEqual(['/p/other.txt'])
    expect(antigravityOutputFilePaths(extraction({
      runCommand: {
        combinedOutput: { full: 'plain output' },
      },
    }))).toEqual([])
  })
})
