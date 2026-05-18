import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { StartupBody, StartupErrorBody, StartupSpinner } from '~/components/common/StartupPanel'

describe('startupSpinner', () => {
  it('renders the label text', () => {
    render(() => <StartupSpinner label="Starting agent…" />)
    expect(screen.getByText('Starting agent…')).toBeInTheDocument()
  })
})

describe('startupBody', () => {
  it('renders title only when neither body nor children are given', () => {
    render(() => <StartupBody title="Just a title" />)
    expect(screen.getByRole('heading', { level: 2 })).toHaveTextContent('Just a title')
  })

  it('renders the body slot below the title', () => {
    render(() => <StartupBody title="Heading" body={<p data-testid="body-slot">subline copy</p>} />)
    expect(screen.getByTestId('body-slot')).toHaveTextContent('subline copy')
  })

  it('renders an action row when children are provided', () => {
    render(() => (
      <StartupBody title="With actions" body="line">
        <button type="button">Download</button>
        <button type="button">Show anyway</button>
      </StartupBody>
    ))
    expect(screen.getByRole('button', { name: 'Download' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Show anyway' })).toBeInTheDocument()
  })

  it('omits the action row when no children are provided', () => {
    render(() => <StartupBody title="No actions" body="line" />)
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
})

describe('startupErrorBody (back-compat layered on StartupBody)', () => {
  it('renders the title and the danger-styled error details block', () => {
    render(() => <StartupErrorBody title="Terminal failed to start" error="exec: not found" />)
    expect(screen.getByRole('heading', { level: 2 })).toHaveTextContent('Terminal failed to start')
    const codeEl = document.querySelector('pre code')
    expect(codeEl).not.toBeNull()
    expect(codeEl!.textContent).toBe('exec: not found')
  })

  it('falls back to "Unknown error" when the error string is empty', () => {
    render(() => <StartupErrorBody title="Boom" error="" />)
    const codeEl = document.querySelector('pre code')
    expect(codeEl!.textContent).toBe('Unknown error')
  })
})
