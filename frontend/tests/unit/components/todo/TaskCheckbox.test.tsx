import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { TaskCheckbox } from '~/components/todo/TaskCheckbox'

describe('taskCheckbox', () => {
  it('renders an empty checkbox for pending', () => {
    const { container } = render(() => <TaskCheckbox status="pending" />)
    const box = container.querySelector('[data-task-checkbox="pending"]')
    expect(box).toBeTruthy()
    expect(box?.querySelector('polyline')).toBeNull()
    expect(box?.querySelector('path')).toBeNull()
  })

  it('renders the checkmark polyline for completed', () => {
    const { container } = render(() => <TaskCheckbox status="completed" />)
    const box = container.querySelector('[data-task-checkbox="completed"]')
    expect(box).toBeTruthy()
    const poly = box?.querySelector('polyline')
    expect(poly?.getAttribute('points')).toBe('20 6 9 17 4 12')
  })

  it('renders the X glyph for deleted', () => {
    const { container } = render(() => <TaskCheckbox status="deleted" />)
    const box = container.querySelector('[data-task-checkbox="deleted"]')
    expect(box).toBeTruthy()
    const path = box?.querySelector('path')
    expect(path?.getAttribute('d')).toBe('M6 6 L18 18 M18 6 L6 18')
  })

  it('renders the marching-ants rect for in_progress that fills the box', () => {
    const { container } = render(() => <TaskCheckbox status="in_progress" />)
    const box = container.querySelector('[data-task-checkbox="in_progress"]')
    expect(box).toBeTruthy()
    const rect = box?.querySelector('rect')
    expect(rect).toBeTruthy()
    // The rect occupies the full 24-unit viewBox (inset by half the
    // stroke width so the outer edge of the stroke is flush with the
    // SVG boundary).
    expect(rect?.getAttribute('width')).toBe('22.5')
  })
})
