import type { MessageCompletion } from './assembledMessage'
import type { ChatRow } from './model/row'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ERROR_MARKER, INTERRUPTION_MARKER } from './assembledMessage'
import { renderExtractedRow } from './rowRenderers'

/** The text of one extracted row, drawn with `completion`. */
function drawnText(row: ChatRow, completion: MessageCompletion | null): string {
  const { container } = render(() => renderExtractedRow({ kind: 'row', row, completion }, undefined))
  return container.textContent ?? ''
}

const INTERRUPTED_DIVIDER: ChatRow = { kind: 'divider', divider: { label: 'Turn interrupted (1.2s)' } }

describe('renderExtractedRow', () => {
  it('draws the interruption marker under the text that a stop cut', () => {
    const text = drawnText({ kind: 'assistant-text', text: 'Half a sen' }, 'interrupted')
    expect(text).toContain('Half a sen')
    expect(text).toContain(INTERRUPTION_MARKER)
  })

  it('draws the error marker under the text that a failure cut', () => {
    expect(drawnText({ kind: 'assistant-text', text: 'Half a sen' }, 'error')).toContain(ERROR_MARKER)
  })

  it('draws no marker under complete text', () => {
    const text = drawnText({ kind: 'assistant-text', text: 'Whole.' }, 'complete')
    expect(text).not.toContain(INTERRUPTION_MARKER)
    expect(text).not.toContain(ERROR_MARKER)
  })

  // A divider states the outcome of the turn in its own label. The marker states
  // truncated TEXT, so under a divider it claimed a cut that a turn with no text, or
  // with complete text, never had -- on every stopped turn of every provider whose
  // divider carries the turn's completion.
  it.each(['interrupted', 'error'] as const)('draws no text marker under a divider of a turn that ended %s', (completion) => {
    const text = drawnText(INTERRUPTED_DIVIDER, completion)
    expect(text).toContain('Turn interrupted (1.2s)')
    expect(text).not.toContain(INTERRUPTION_MARKER)
    expect(text).not.toContain(ERROR_MARKER)
  })
})
