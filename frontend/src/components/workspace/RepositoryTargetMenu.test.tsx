import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
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

  // The fold is lossy, so two labels can reduce to one slug. Deriving the id
  // from user-controlled text alone cannot supply the uniqueness the prop
  // exists for, and a repository label carries a worker name, a middle dot and
  // a path -- all of which fold to the same hyphen.
  it('keeps two submenus apart when their labels fold to one slug', () => {
    renderMenu([{ label: 'worker · a/b' }, { label: 'worker-a-b' }])

    const ids = screen.getAllByRole('menuitem', { hidden: true })
      .map(el => el.closest('[data-testid]')?.getAttribute('data-testid'))
      .filter((id): id is string => Boolean(id) && id!.startsWith('target-'))
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('falls back to the position when a label has nothing addressable in it', () => {
    renderMenu([{ label: '日本語' }, { label: 'beta' }])

    // An empty slug would have made the id `target-`, which the sibling's
    // popover id `target-beta-popover` does not collide with only by luck.
    expect(screen.getByTestId('target-0')).toBeInTheDocument()
    expect(screen.getByTestId('target-beta')).toBeInTheDocument()
  })

  it('renders nothing at all for no targets', () => {
    renderMenu([])

    expect(items('host')).toEqual([])
    expect(screen.queryByText('Repositories')).not.toBeInTheDocument()
  })

  // Every caller drives `targets` from a memo that empties when its menu
  // closes, so the 1 -> 0 transition happens on every close. Indexing the list
  // for the single-target branch handed `undefined` to the caller's render
  // prop whenever that transition beat the enclosing guard.
  it('renders nothing when the only target disappears', () => {
    const [targets, setTargets] = createSignal<Repo[]>([{ label: 'alpha' }])
    render(() => (
      <menu data-testid="host">
        <RepositoryTargetMenu
          targets={targets}
          labelOf={t => t.label}
          header="Repositories"
          testIdPrefix="target"
        >
          {t => <button type="button" role="menuitem">{`act on ${t.label}`}</button>}
        </RepositoryTargetMenu>
      </menu>
    ))
    expect(items('host')).toEqual(['act on alpha'])

    setTargets([])

    expect(items('host')).toEqual([])
  })
})
