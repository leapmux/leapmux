/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AnimatedCount } from './AnimatedCount'
import { SWAP_MS } from './AnimatedCount.css'

describe('animated count', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('exposes the supplied display and unit to assistive technology', () => {
    const { getByText } = render(() => <AnimatedCount display="1.2" unit="KB" />)
    expect(getByText('1.2 KB')).toBeInTheDocument()
  })

  it('disables digit and fade animation while paused', () => {
    const { container } = render(() => <AnimatedCount display="123" unit="tokens" paused />)
    expect(container.querySelector('[data-animated-count]')).toHaveAttribute('data-paused', 'true')
    expect(container.querySelector<HTMLElement>('[data-testid="odo-digit"] > span:last-child')?.style.transition).toBe('none')
  })

  it('removes the fading layer of the old unit family when the fade ends', () => {
    vi.useFakeTimers()
    const [display, setDisplay] = createSignal('999')
    const { queryAllByTestId } = render(() => <AnimatedCount display={display()} unit="tokens" />)
    expect(queryAllByTestId('odo-exiting')).toHaveLength(0)

    // 999 -> 1.0k changes the unit family, so the old number fades out over the new one.
    setDisplay('1.0k')
    expect(queryAllByTestId('odo-exiting')).toHaveLength(1)

    vi.advanceTimersByTime(SWAP_MS - 1)
    expect(queryAllByTestId('odo-exiting')).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(queryAllByTestId('odo-exiting')).toHaveLength(0)
  })

  it('clears the timer of a fading layer when it unmounts', () => {
    vi.useFakeTimers()
    const [display, setDisplay] = createSignal('999')
    const { unmount } = render(() => <AnimatedCount display={display()} unit="tokens" />)
    setDisplay('1.0k')
    expect(vi.getTimerCount()).toBe(1)

    unmount()

    expect(vi.getTimerCount()).toBe(0)
  })
})
