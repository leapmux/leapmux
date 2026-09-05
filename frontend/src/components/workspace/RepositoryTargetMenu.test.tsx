import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { RepositoryTargetMenu } from './RepositoryTargetMenu'

interface Repo { label: string }

function renderMenu(targets: Repo[]) {
  return render(() => (
    <menu data-testid="host">
      <RepositoryTargetMenu
        targets={() => targets}
        labelOf={t => t.label}
        header="Repositories"
        testIdPrefix="target"
      >
        {t => <button type="button" role="menuitem">{`act on ${t.label}`}</button>}
      </RepositoryTargetMenu>
    </menu>
  ))
}

function items(testId: string): string[] {
  return within(screen.getByTestId(testId))
    .queryAllByRole('menuitem', { hidden: true })
    .map(el => el.textContent?.trim() ?? '')
}

describe('repositoryTargetMenu', () => {
  // A submenu holding the only choice is a click the user should not have to
  // make, which is the rule the tab-creation items already followed.
  it('renders one target FLAT, with no submenu and no header', () => {
    renderMenu([{ label: 'leapmux' }])

    expect(items('host')).toEqual(['act on leapmux'])
    expect(screen.queryByText('Repositories')).not.toBeInTheDocument()
  })

  it('gives each of several targets its own submenu, under the header', () => {
    renderMenu([{ label: 'alpha' }, { label: 'beta' }])

    expect(screen.getByText('Repositories')).toBeInTheDocument()
    expect(items('host')).toEqual(['alpha', 'beta'])
  })

  it('renders one target\'s actions inside its own submenu', () => {
    renderMenu([{ label: 'alpha' }, { label: 'beta' }])

    fireEvent.click(screen.getByTestId('target-beta'))
    expect(items('target-beta-popover')).toEqual(['act on beta'])
  })

  // A shared id would address whichever copy the DOM holds first, which is
  // exactly the case that renders more than one submenu.
  it('gives each submenu an id derived from its own label', () => {
    renderMenu([{ label: 'worker-a · my.repo' }, { label: 'beta' }])

    expect(screen.getByTestId('target-worker-a-my-repo')).toBeInTheDocument()
    expect(screen.getByTestId('target-beta')).toBeInTheDocument()
  })

  it('renders nothing at all for no targets', () => {
    renderMenu([])

    expect(items('host')).toEqual([])
    expect(screen.queryByText('Repositories')).not.toBeInTheDocument()
  })
})
