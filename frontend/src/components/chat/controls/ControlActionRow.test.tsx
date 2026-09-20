import { render, screen } from '@solidjs/testing-library'
import { createSignal, Show } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { actionButtonClass, ControlActionRow } from './ControlActionRow'

describe('actionButtonClass', () => {
  it('sizes a filled action', () => {
    expect(actionButtonClass()).toBe(compactControl)
    expect(actionButtonClass(false)).toBe(compactControl)
  })

  it('keeps the outline variant beside the size', () => {
    expect(actionButtonClass(true)).toBe(`${compactControl} outline`)
  })
})

describe('ControlActionRow', () => {
  it('renders navigation only while its conditional content exists', () => {
    const [visible, setVisible] = createSignal(false)
    render(() => <ControlActionRow navigation={<Show when={visible()}><span>Page 1</span></Show>} primary={<button>Submit</button>} />)
    const row = screen.getByTestId('control-footer')
    expect(row.children).toHaveLength(1)
    setVisible(true)
    expect(row.children).toHaveLength(2)
    setVisible(false)
    expect(row.children).toHaveLength(1)
  })

  it('puts the primary actions in the decision group', () => {
    render(() => <ControlActionRow primary={<button data-testid="allow">Allow</button>} />)

    const row = screen.getByTestId('control-footer')
    expect(row).toBeInTheDocument()
    // The decisions are one group that never splits across a wrap: the button,
    // the group, the decision row, the footer.
    const button = screen.getByTestId('allow')
    const group = button.parentElement
    expect(group?.className).toContain('controlFooterPrimaryDecisions')
    expect(group?.parentElement).toBe(row.firstElementChild)
  })

  it('omits an absent secondary group', () => {
    // An absent group must not reserve a layout gap.
    const { container } = render(() => (
      <ControlActionRow primary={<button data-testid="allow">Allow</button>} />
    ))

    expect(container.querySelector('[data-testid="control-footer"]')?.children).toHaveLength(1)
  })

  it('keeps secondary actions, navigation, and decisions in reading order', () => {
    render(() => (
      <ControlActionRow
        secondary={<button data-testid="reject">Reject</button>}
        navigation={<span data-testid="pagination">1 2 3</span>}
        primary={<button data-testid="submit">Submit</button>}
      />
    ))

    const text = screen.getByTestId('control-footer').textContent ?? ''
    expect(text.indexOf('Reject')).toBeLessThan(text.indexOf('1 2 3'))
    expect(text.indexOf('1 2 3')).toBeLessThan(text.indexOf('Submit'))
  })

  it('groups leading controls before the decisions', () => {
    render(() => (
      <ControlActionRow
        leading={<span data-testid="choice">Choice</span>}
        primary={<button data-testid="allow">Allow</button>}
      />
    ))

    const choice = screen.getByTestId('choice')
    const allow = screen.getByTestId('allow')
    // Both groups sit inside the decision row, the choices before the decisions.
    const decisions = allow.closest('[class*="controlFooterDecisions"]')
    expect(choice.closest('[class*="controlFooterDecisions"]')).toBe(decisions)
    expect(choice.compareDocumentPosition(allow) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('identifies the row once for every group combination', () => {
    // One marker identifies the row for each combination of groups.
    const { container: onlyPrimary } = render(() => <ControlActionRow primary={<span>a</span>} />)
    const { container: allZones } = render(() => (
      <ControlActionRow secondary={<span>a</span>} navigation={<span>b</span>} primary={<span>c</span>} />
    ))

    expect(onlyPrimary.querySelectorAll('[data-testid="control-footer"]')).toHaveLength(1)
    expect(allZones.querySelectorAll('[data-testid="control-footer"]')).toHaveLength(1)
  })
})
