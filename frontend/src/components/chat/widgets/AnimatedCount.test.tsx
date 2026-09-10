/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AnimatedCount } from './AnimatedCount'

describe('animated count', () => {
  it('exposes the supplied display and unit to assistive technology', () => {
    const { getByText } = render(() => <AnimatedCount display="1.2" unit="KB" />)
    expect(getByText('1.2 KB')).toBeInTheDocument()
  })

  it('disables digit and fade animation while paused', () => {
    const { container } = render(() => <AnimatedCount display="123" unit="tokens" paused />)
    expect(container.querySelector('[data-animated-count]')).toHaveAttribute('data-paused', 'true')
    expect(container.querySelector<HTMLElement>('[data-testid="odo-digit"] > span:last-child')?.style.transition).toBe('none')
  })
})
