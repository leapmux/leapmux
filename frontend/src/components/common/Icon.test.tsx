import { render } from '@solidjs/testing-library'
import Eye from 'lucide-solid/icons/eye'
import EyeOff from 'lucide-solid/icons/eye-off'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { Icon } from './Icon'

describe('icon', () => {
  it('reactively updates when the icon prop changes', () => {
    const [icon, setIcon] = createSignal(Eye)

    const { container } = render(() => (
      <Icon icon={icon()} size="sm" />
    ))

    const svg = () => container.querySelector('svg')!

    // Initial render should show the Eye icon.
    expect(svg()).toHaveClass('lucide-eye')
    expect(svg()).not.toHaveClass('lucide-eye-off')

    // Switch to EyeOff — the rendered SVG should update.
    setIcon(() => EyeOff)

    expect(svg()).toHaveClass('lucide-eye-off')
    expect(svg()).not.toHaveClass('lucide-eye')

    // Switch back to Eye.
    setIcon(() => Eye)

    expect(svg()).toHaveClass('lucide-eye')
    expect(svg()).not.toHaveClass('lucide-eye-off')
  })
})
