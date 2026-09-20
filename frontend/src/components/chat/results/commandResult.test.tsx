import type { CommandResult } from '../model/commandResult'
import type { FinishedToolCallStatus, UnfinishedToolCallStatus } from '../model/toolCallStatus'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { FINISHED_TOOL_STATUSES, UNFINISHED_TOOL_STATUSES } from '../model/toolCallStatus'
import { CommandResultBody } from './commandResult'

function source(over: Partial<CommandResult> = {}): CommandResult {
  return { output: 'ok\n', ...over } as CommandResult
}

/** The alert glyph, by the class lucide stamps on it. */
function glyph(container: HTMLElement): string {
  return container.querySelector('svg.lucide')?.getAttribute('class') ?? ''
}

describe('CommandResultBody', () => {
  // A row that succeeded states nothing about its outcome: the header is suppressed
  // whenever the label IS the success word, so the body is just the output.
  it('draws no status header for a plain command that succeeded', () => {
    const { container } = render(() => <CommandResultBody source={source()} status="completed" />)
    expect(container.textContent).toContain('ok')
    expect(container.textContent).not.toContain('Success')
    expect(glyph(container)).toBe('')
  })

  // The CALL's status and the PROCESS's exit code are separate verdicts, and the row
  // must state a failure from either. `commandIsError` answers only the process half,
  // so the caller restores the status half -- and nothing pinned that until now.
  it('states a failure the STATUS reported, beside a zero exit code', () => {
    const { container } = render(() => <CommandResultBody source={source({ exitCode: 0 })} status="failed" />)
    expect(glyph(container)).toContain('lucide-circle-alert')
    expect(container.textContent).toContain('Error')
  })

  it('states a failure the EXIT CODE reported, whatever the status says', () => {
    const { container } = render(() => <CommandResultBody source={source({ exitCode: 7 })} status="completed" />)
    expect(glyph(container)).toContain('lucide-circle-alert')
    expect(container.textContent).toContain('Error (exit 7)')
  })

  // A process the OS ended reports no code of its own, so the signal is the only
  // thing that explains the row.
  it('states the signal that ended a process no exit code describes', () => {
    const { container } = render(() => <CommandResultBody source={source({ signal: 'killed' })} status="completed" />)
    expect(glyph(container)).toContain('lucide-circle-alert')
    expect(container.textContent).toContain('Error (killed)')
  })

  it('words the reader own stop Interrupted, and a refusal Declined', () => {
    const stopped = render(() => <CommandResultBody source={source({ exitCode: 1 })} status="cancelled" />)
    expect(stopped.container.textContent).toContain('Interrupted')
    expect(glyph(stopped.container)).toContain('lucide-circle-alert')
    const declined = render(() => <CommandResultBody source={source()} status="declined" />)
    expect(declined.container.textContent).toContain('Declined')
    expect(glyph(declined.container)).toContain('lucide-ban')
  })

  // A command that wrote nothing still has to say so, with whatever the row knows.
  it('states an empty stream beside the code or the signal', () => {
    const coded = render(() => <CommandResultBody source={source({ output: '', exitCode: 3 })} status="failed" />)
    expect(coded.container.textContent).toContain('exit 3')
    const signalled = render(() => <CommandResultBody source={source({ output: '', signal: 'terminated' })} status="completed" />)
    expect(signalled.container.textContent).toContain('terminated')
  })

  it('says an output stream could not be recovered, which is not an empty one', () => {
    const { container } = render(() => <CommandResultBody source={source({ output: '', outputUnavailable: true })} status="completed" />)
    expect(container.textContent).toContain('output unavailable')
  })

  it.each(UNFINISHED_TOOL_STATUSES)('does not describe an unfinished %s stream as empty', (status: UnfinishedToolCallStatus) => {
    const { container } = render(() => <CommandResultBody source={source({ output: '' })} status={status} />)
    expect(container.textContent).not.toContain('[no output]')
  })

  it.each(FINISHED_TOOL_STATUSES)('states that a finished %s stream is empty', (status: FinishedToolCallStatus) => {
    const { container } = render(() => <CommandResultBody source={source({ output: '' })} status={status} />)
    expect(container.textContent).toContain('[no output]')
  })
})
