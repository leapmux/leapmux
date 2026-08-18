import { cleanup, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { stubMatchMedia } from '~/test-support/matchMediaStub'
import { useViewportBelow } from './useViewportBelow'

const QUERY = '(max-width: 639px)'

function Probe() {
  const below = useViewportBelow(640)
  return <span data-testid="below">{String(below())}</span>
}

afterEach(() => {
  cleanup()
})

describe('useViewportBelow', () => {
  it('removes its change handler on cleanup, every time', () => {
    const mm = stubMatchMedia()
    try {
      render(() => <Probe />)
      expect(mm.handlersFor(QUERY)).toHaveLength(1)
      cleanup()
      expect(mm.handlersFor(QUERY)).toHaveLength(0)

      // The second mount is the one that mattered: the Preferences dialog
      // unmounts on close, so an unpaired listener accumulated one handler
      // per open, each writing to a disposed signal.
      render(() => <Probe />)
      expect(mm.handlersFor(QUERY)).toHaveLength(1)
      cleanup()
      expect(mm.handlersFor(QUERY)).toHaveLength(0)
    }
    finally {
      mm.restore()
    }
  })

  it('follows the query after mount', () => {
    const mm = stubMatchMedia()
    try {
      render(() => <Probe />)
      expect(screen.getByTestId('below').textContent).toBe('false')
      for (const handler of mm.handlersFor(QUERY))
        handler({ matches: true })
      expect(screen.getByTestId('below').textContent).toBe('true')
    }
    finally {
      mm.restore()
    }
  })

  // jsdom implements innerWidth but not matchMedia, so the guard is what
  // keeps every test that mounts a responsive component from throwing.
  it('answers from innerWidth where matchMedia is absent', () => {
    render(() => <Probe />)
    expect(screen.getByTestId('below').textContent).toBe(String(window.innerWidth < 640))
  })
})
