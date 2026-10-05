import type { CommandResult } from '../model/commandResult'
import type { FinishedToolCallStatus, UnfinishedToolCallStatus } from '../model/toolCallStatus'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { withCommandExit } from '../model/commandResult'
import { FINISHED_TOOL_STATUSES, UNFINISHED_TOOL_STATUSES } from '../model/toolCallStatus'
import { CommandResultBody } from './commandResult'

function source(over: Partial<CommandResult> = {}): CommandResult {
  const base: CommandResult = {
    output: over.output ?? 'ok\n',
    ...(over.outputUnavailable !== undefined ? { outputUnavailable: over.outputUnavailable } : {}),
    ...(over.durationMs !== undefined ? { durationMs: over.durationMs } : {}),
    ...(over.truncated !== undefined ? { truncated: over.truncated } : {}),
    ...(over.label !== undefined ? { label: over.label } : {}),
  }
  if (over.exitCode !== undefined && over.exitCode !== null)
    return withCommandExit(base, { exitCode: over.exitCode })
  if (over.signal !== undefined)
    return withCommandExit(base, { signal: over.signal })
  if (over.failed === true)
    return withCommandExit(base, { failed: true })
  return over.exitCode === null ? withCommandExit(base, { exitCode: null }) : base
}

/** The alert glyph, by the class lucide stamps on it. */
function glyph(container: HTMLElement): string {
  return container.querySelector('svg.lucide')?.getAttribute('class') ?? ''
}

describe('CommandResultBody', () => {
  // Successful commands omit the status header and display their output directly.
  it('draws no status header for a plain command that succeeded', () => {
    const { container } = render(() => <CommandResultBody source={source()} status="completed" />)
    expect(container.textContent).toContain('ok')
    expect(container.textContent).not.toContain('Success')
    expect(glyph(container)).toBe('')
  })

  // The call status and process exit code are separate results.
  // Either can require a failure header.
  // commandIsError reads only the process exit.
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

  // A command can fail before startup or after its time limit without a code or signal.
  // The command failure flag must still identify that failure.
  // One call can contain several command outcomes.
  it('states a failure that the command reported without a code or a signal', () => {
    const { container } = render(() => <CommandResultBody source={source({ output: 'Command failed: spawn nope ENOENT', failed: true })} status="completed" />)
    expect(glyph(container)).toContain('lucide-circle-alert')
    expect(container.textContent).toContain('Error')
    expect(container.textContent).toContain('Command failed: spawn nope ENOENT')
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

describe('CommandResultBody output ownership', () => {
  it.each([false, true])('marks returned output and excludes the status header when expanded is %s', (expanded) => {
    const output = ['native first', ...Array.from({ length: 8 }, (_, index) => `line ${index}`), 'native last'].join('\n')
    const { container } = render(() => <CommandResultBody source={source({ output, exitCode: 7 })} status="failed" context={{ getMessageUiState: () => expanded }} />)
    const marked = container.querySelectorAll('[data-tool-output-preview]')
    expect(marked).toHaveLength(1)
    expect(marked[0]?.textContent).toContain('native first')
    expect(marked[0]?.textContent).not.toContain('Error (exit 7)')
    expect(container.textContent).toContain('Error (exit 7)')
    expect(marked[0]?.textContent?.includes('native last')).toBe(expanded)
  })

  it('keeps the empty stream hint outside output ownership', () => {
    const { container } = render(() => <CommandResultBody source={source({ output: '', exitCode: 0 })} status="completed" />)
    expect(container.textContent).toContain('[no output]')
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
  })
})
