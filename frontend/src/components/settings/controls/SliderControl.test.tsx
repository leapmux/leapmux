import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SliderControl } from './SliderControl'

afterEach(() => {
  cleanup()
})

describe('sliderControl', () => {
  it('updates the readout while dragging and commits only on release', () => {
    const onChange = vi.fn()
    render(() => (
      <SliderControl
        value={40}
        min={0}
        max={100}
        step={1}
        unit="%"
        ariaLabel="Volume"
        onChange={onChange}
      />
    ))
    const slider = screen.getByRole('slider', { name: 'Volume' }) as HTMLInputElement
    expect(screen.getByText('40%')).toBeTruthy()

    fireEvent.input(slider, { target: { value: '60' } })
    expect(screen.getByText('60%')).toBeTruthy()
    expect(onChange).not.toHaveBeenCalled()

    fireEvent.change(slider, { target: { value: '60' } })
    expect(onChange).toHaveBeenCalledWith(60)
  })
})
