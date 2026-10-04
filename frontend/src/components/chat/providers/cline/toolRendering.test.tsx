import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { CLINE_TOOL } from '~/generated/contracts/cline-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from '../../messageContentRenderer'
import { toolOutcomeLabel } from '../../results/toolOutcomeLabel'
import { toolResultCollapsed } from '../../toolStyles.css'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { clineToolFinishRow, clineToolStartRow } from './toolResults.fixtures'
import './plugin'
import '../testMocks'

const plugin = () => providerFor(AgentProvider.CLINE)!

function parsed(row: Record<string, unknown>): ResolvedMessageContent {
  return { ...input(row, undefined, AgentProvider.CLINE), supplementalContent: undefined }
}

/**
 * Render the result row of one call as the chat draws it by default: collapsed, with
 * its start row beside it as the request.
 */
function renderResult(name: string, args: Record<string, unknown>, output: unknown) {
  const request = clineToolStartRow(name, args)
  const row = clineToolFinishRow(name, output)
  const category = plugin().transcript.classify(input(row, undefined, AgentProvider.CLINE))
  const sources = testMessageSources({
    current: () => parsed(row),
    request: () => parsed(request),
    role: () => 'result',
    visibleRows: () => ({ request: true, result: true }),
  })
  return render(() => renderMessageContent(row, { premeasureMode: true, spanType: name, sources }, category, AgentProvider.CLINE))
}

describe('cline tool rendering', () => {
  // The record of Cline 3.0.64 for a command that wrote to stderr and exited with 3, as
  // the model received it in an E2E run. Cline states the code on the first line of the
  // result and again as the error, and the stderr text comes after a blank line and
  // `[stderr]`. The body must not hold the two statements of the code: they push the
  // stderr text below the rows that a collapsed result shows. The header states the
  // code instead, and it says that the command failed.
  it('draws why a command failed without an expansion, and heads it with the exit code', () => {
    const query = 'echo "cline-fail-$((70 + 7))" >&2; exit 3'
    const { container } = renderResult(CLINE_TOOL.RunCommands, { commands: [query] }, [
      { query, result: '[Command exited with code 3]\n\n[stderr]\ncline-fail-77\n', error: 'Command exited with code 3', success: false },
    ])
    expect(container.textContent).toContain('cline-fail-77')
    expect(container.textContent).toContain(toolOutcomeLabel('failed', 'exit 3'))
    // The header states the code. The body does not state it again.
    expect(container.textContent).not.toContain('Command exited with code')
    expect(container.querySelector(`.${toolResultCollapsed}`)).toBeNull()
  })

  it('heads each command of a call with its own outcome', () => {
    const { container } = renderResult(CLINE_TOOL.RunCommands, { commands: ['echo ok', 'make'] }, [
      { query: 'echo ok', result: 'ok\n', success: true },
      { query: 'make', result: '[Command exited with code 2]\nmake: *** No targets.', error: 'Command exited with code 2', success: false },
    ])
    expect(container.textContent).toContain('ok')
    expect(container.textContent).toContain('make: *** No targets.')
    expect(container.textContent).toContain(toolOutcomeLabel('failed', 'exit 2'))
    expect(container.textContent).not.toContain('Command exited with code')
  })

  // A command that did not start states no code. Its error is the only statement of
  // why, so the body keeps it.
  it('keeps the error of a command that failed with no exit code, under a failure header', () => {
    const { container } = renderResult(CLINE_TOOL.RunCommands, { commands: ['nope'] }, [
      { query: 'nope', result: '', error: 'Command failed: spawn nope ENOENT', success: false },
    ])
    expect(container.textContent).toContain('Command failed: spawn nope ENOENT')
    expect(container.textContent).toContain(toolOutcomeLabel('failed'))
  })

  // One call, two commands: the one that ran states no outcome, and the one that ran
  // out of time states its failure, although the call itself completed.
  it('heads only the command that failed with no exit code', () => {
    const { container } = renderResult(CLINE_TOOL.RunCommands, { commands: ['echo ok', 'sleep 99'] }, [
      { query: 'echo ok', result: 'ok\n', success: true },
      { query: 'sleep 99', result: '', error: 'Command failed: Command timed out after 30000ms', success: false },
    ])
    const headers = container.textContent?.split(toolOutcomeLabel('failed')).length ?? 0
    expect(headers - 1).toBe(1)
    expect(container.textContent).not.toContain(toolOutcomeLabel('succeeded'))
  })
})
