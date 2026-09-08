import type { DropdownTriggerProps } from './DropdownMenu'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import * as iconButtonStyles from './IconButton.css'
import { moreHorizontalTrigger } from './moreHorizontalTrigger'

const triggerProps: DropdownTriggerProps = {
  'aria-expanded': false,
  'ref': () => {},
  'onPointerDown': vi.fn(),
  'onClick': vi.fn(),
}

describe('moreHorizontalTrigger', () => {
  it('uses a 24px button with a 14px icon', () => {
    render(() => moreHorizontalTrigger({ title: 'More actions' })(triggerProps))

    const button = screen.getByRole('button', { name: 'More actions' })
    expect(button.classList).toContain(iconButtonStyles.sizeMd)
    expect(button.classList).not.toContain(iconButtonStyles.sizeSm)

    const icon = button.querySelector('svg')
    expect(icon).toHaveAttribute('width', '14')
    expect(icon).toHaveAttribute('height', '14')
  })
})
