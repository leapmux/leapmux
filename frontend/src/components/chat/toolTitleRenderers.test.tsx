import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { renderEditTitle, renderReadTitle, renderWriteTitle } from './toolTitleRenderers'

describe('file tool titles', () => {
  it.each([-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('ignores an invalid read offset or limit: %s', (value) => {
    const { container } = render(() => renderReadTitle('/file.ts', value, value))
    expect(container.textContent).toBe('/file.ts')
  })

  it('does not display an end line that exceeds the safe integer range', () => {
    const { container } = render(() => renderReadTitle('/file.ts', Number.MAX_SAFE_INTEGER, 2))
    expect(container.textContent).toBe(`/file.ts (Line ${Number.MAX_SAFE_INTEGER}–)`)
  })

  it('counts inserted lines when the original text is empty', () => {
    const { container } = render(() => renderEditTitle('/file.ts', '', 'one\ntwo\n'))
    expect(container.textContent).toContain('+2')
  })

  it('counts removed lines when the new text is empty', () => {
    const { container } = render(() => renderEditTitle('/file.ts', 'one\ntwo\n', ''))
    expect(container.textContent).toMatch(/[-−]2/)
  })

  it('does not count a trailing newline as another written line', () => {
    const { container } = render(() => renderWriteTitle('/file.ts', 'one\n'))
    expect(container.textContent).toContain('(1 line)')
  })
})
