import { render, screen } from '@solidjs/testing-library'
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

describe('controlActionRow', () => {
  it('puts the primary actions in the right-hand zone', () => {
    render(() => <ControlActionRow primary={<button data-testid="allow">Allow</button>} />)

    const row = screen.getByTestId('control-footer')
    expect(row).toBeInTheDocument()
    expect(screen.getByTestId('allow').parentElement?.parentElement).toBe(row)
  })

  it('renders no secondary zone when the row has only a primary action', () => {
    // Seven of the nine provider rows offer one action. An empty left zone
    // would still occupy its grid column and pull the primary action inward.
    const { container } = render(() => (
      <ControlActionRow primary={<button data-testid="allow">Allow</button>} />
    ))

    expect(container.querySelector('[data-testid="control-footer"]')?.children).toHaveLength(1)
  })

  it('keeps the secondary, centre, and primary zones in reading order', () => {
    render(() => (
      <ControlActionRow
        secondary={<button data-testid="reject">Reject</button>}
        centre={<span data-testid="pagination">1 2 3</span>}
        primary={<button data-testid="submit">Submit</button>}
      />
    ))

    const text = screen.getByTestId('control-footer').textContent ?? ''
    expect(text.indexOf('Reject')).toBeLessThan(text.indexOf('1 2 3'))
    expect(text.indexOf('1 2 3')).toBeLessThan(text.indexOf('Submit'))
  })

  it('puts leading controls before decisions in the right-hand zone', () => {
    render(() => (
      <ControlActionRow
        leading={<span data-testid="choice">Choice</span>}
        primary={<button data-testid="allow">Allow</button>}
      />
    ))

    const choice = screen.getByTestId('choice')
    const allow = screen.getByTestId('allow')
    expect(choice.parentElement).toBe(allow.parentElement)
    expect(choice.compareDocumentPosition(allow) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('marks every row with one test id, whatever zones it fills', () => {
    // Only two of the nine rows carried this marker before the extraction, so
    // nothing noticed when one of them differed from the rest.
    const { container: onlyPrimary } = render(() => <ControlActionRow primary={<span>a</span>} />)
    const { container: allZones } = render(() => (
      <ControlActionRow secondary={<span>a</span>} centre={<span>b</span>} primary={<span>c</span>} />
    ))

    expect(onlyPrimary.querySelectorAll('[data-testid="control-footer"]')).toHaveLength(1)
    expect(allZones.querySelectorAll('[data-testid="control-footer"]')).toHaveLength(1)
  })
})
