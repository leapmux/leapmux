import type { LocationChange } from '@solidjs/router'
import { readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
import { cleanup, render, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { DEFAULT_NAV_GROUP_ID } from '~/components/settings/navGroups'
import { collectFiles } from '~/test-support/sourceTree'
import { closePreferences, openPreferences, PreferencesAddress, showPreferencesDialog } from './UserMenuState'

/**
 * A memory history that RECORDS what the router writes to it.
 *
 * The push-or-replace choice is the whole behaviour here, and the address
 * alone cannot show it: an open that pushed and an open that replaced leave
 * the same string in `get()`. Only the write says which happened.
 */
function mountAddress(start = '/') {
  const base = createMemoryHistory()
  if (start !== '/')
    base.set({ value: start, replace: true })
  const writes: { value: string, replace: boolean }[] = []
  const history = {
    ...base,
    set(change: LocationChange) {
      writes.push({ value: change.value, replace: change.replace === true })
      base.set(change)
    },
  }
  const result = render(() => (
    <MemoryRouter history={history}>
      <Route path="/" component={PreferencesAddress} />
    </MemoryRouter>
  ))
  return { ...result, history, writes }
}

describe('preferences address', () => {
  afterEach(cleanup)

  // The deep link. Preferences is reachable by address, so a pasted or
  // bookmarked link opens the dialog on the section it specifies.
  it('opens on the category the address specifies', () => {
    mountAddress('/?prefs=admin-email')
    expect(showPreferencesDialog()).toBe('admin-email')
  })

  it('is closed while the address carries no category', () => {
    mountAddress()
    expect(showPreferencesDialog()).toBeNull()
  })

  // `?prefs=` carries no category, so it means the same as no parameter. The
  // router drops an empty value of its own, so this address can only be typed
  // by hand -- and an empty string is not a NAV_GROUPS id, so treating it as
  // one opens the dialog on a section that does not exist.
  it('is closed while the parameter carries an empty value', () => {
    mountAddress('/?prefs=')
    expect(showPreferencesDialog()).toBeNull()
  })

  // The three no-argument callers -- the user menu, the $mod+, shortcut and
  // the desktop menu item -- all reach this branch.
  it('opens the default group when a caller asks for no section', async () => {
    const { history, writes } = mountAddress()

    openPreferences()

    await waitFor(() => expect(showPreferencesDialog()).toBe(DEFAULT_NAV_GROUP_ID))
    expect(history.get()).toBe(`/?prefs=${DEFAULT_NAV_GROUP_ID}`)
    // PUSHED, which is what gives the Back button something to close.
    expect(writes).toEqual([{ value: `/?prefs=${DEFAULT_NAV_GROUP_ID}`, replace: false }])
  })

  it('closes when the browser goes back', async () => {
    const { history } = mountAddress()
    openPreferences('appearance')
    await waitFor(() => expect(showPreferencesDialog()).toBe('appearance'))

    history.back()

    await waitFor(() => expect(showPreferencesDialog()).toBeNull())
    expect(history.get()).toBe('/')
  })

  // Browsing the sections must not fill the history: the section is part of
  // one address rather than a page of its own.
  it('replaces the entry when it moves an open dialog to another section', async () => {
    const { history, writes } = mountAddress()
    openPreferences()
    await waitFor(() => expect(showPreferencesDialog()).toBe(DEFAULT_NAV_GROUP_ID))

    openPreferences('notifications')

    await waitFor(() => expect(showPreferencesDialog()).toBe('notifications'))
    expect(writes.map(w => w.replace)).toEqual([false, true])
    expect(history.get()).toBe('/?prefs=notifications')
  })

  // The close matches the open. Writing the plain address again would leave a
  // second copy of the page the user is already on in the history, and every
  // open-and-close would add one more Back press that does nothing.
  it('steps back over the entry it pushed when it closes', async () => {
    const { history, writes } = mountAddress()
    openPreferences('account')
    await waitFor(() => expect(showPreferencesDialog()).toBe('account'))

    closePreferences()

    await waitFor(() => expect(showPreferencesDialog()).toBeNull())
    expect(history.get()).toBe('/')
    expect(writes).toHaveLength(1)
  })

  // A deep link owns no entry of this app's making. Stepping back there leads
  // to another site, or -- as here, where it is the first entry -- nowhere at
  // all, which would leave the dialog open with no way to dismiss it.
  it('removes the parameter in place when it did not push the entry', async () => {
    const { history, writes } = mountAddress('/?prefs=account')
    expect(showPreferencesDialog()).toBe('account')

    closePreferences()

    await waitFor(() => expect(showPreferencesDialog()).toBeNull())
    expect(history.get()).toBe('/')
    expect(writes).toEqual([{ value: '/', replace: true }])
  })

  // The claim on a pushed entry belongs to the tree that made it. This
  // registration unmounts under a running app -- the app-root ErrorBoundary
  // tears its subtree down when it catches, and the desktop launcher does the
  // same when the connection drops -- and the tree that comes back pushed
  // nothing. A claim that outlives the unmount makes the next close step back
  // over an entry that this app never wrote, which leads to another site and
  // leaves the dialog open.
  it('drops its claim on the pushed entry when the router goes away', async () => {
    const first = mountAddress()
    openPreferences('account')
    await waitFor(() => expect(showPreferencesDialog()).toBe('account'))

    first.unmount()

    const { history, writes } = mountAddress('/?prefs=account')
    expect(showPreferencesDialog()).toBe('account')

    closePreferences()

    await waitFor(() => expect(showPreferencesDialog()).toBeNull())
    expect(history.get()).toBe('/')
    expect(writes).toEqual([{ value: '/', replace: true }])
  })

  it('keeps the other search parameters', async () => {
    const { history } = mountAddress('/?tab=logs')

    openPreferences('appearance')
    await waitFor(() => expect(history.get()).toBe('/?tab=logs&prefs=appearance'))

    closePreferences()
    await waitFor(() => expect(history.get()).toBe('/?tab=logs'))
  })

  // The desktop launcher registers the Apple-menu item for the whole session,
  // and it runs before the app connects -- where there is no Router, and no
  // mount of the dialog either.
  it('does nothing while no Router is mounted', () => {
    expect(showPreferencesDialog()).toBeNull()
    openPreferences('appearance')
    closePreferences()
    expect(showPreferencesDialog()).toBeNull()
  })
})

/**
 * Where the registration mounts, pinned rather than left to a reviewer.
 *
 * Neither half fails loudly on its own. Every case above mounts
 * `PreferencesAddress` itself, so removing the one production mount leaves
 * this file green while `openPreferences` does nothing anywhere in the app --
 * the user menu item, the $mod+, shortcut and the desktop Apple-menu item all
 * stop opening Preferences, and no unit test says a word. A SECOND mount is
 * the other failure: the registration is one module-level signal, so the
 * second one replaces the first, and the copy that survives is whichever
 * subtree rendered last.
 *
 * A source scan rather than a render: mounting is a fact about the app tree,
 * and rendering the real root here would need the whole provider stack, the
 * router and the platform bridge. The same shape as the guard in
 * `ElevationPromptHost.test.tsx`, which pins the sibling registration.
 */
describe('preferencesAddress mounting', () => {
  const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..')
  const srcRoot = join(frontendRoot, 'src')
  // A JSX MOUNT, never the bare name: this module defines the component and
  // several files carry prose about it, and neither is a mount.
  const MOUNT = /<PreferencesAddress[\s/>]/

  it('mounts once, at the app root, inside the Router', () => {
    const mounts = collectFiles(srcRoot, {
      matches: name => (name.endsWith('.ts') || name.endsWith('.tsx')) && !name.includes('.test.'),
      alsoSkip: new Set(['generated']),
    })
      .filter(file => MOUNT.test(readFileSync(file, 'utf-8')))
      .map(file => relative(frontendRoot, file))
      .sort()

    expect(
      mounts,
      'The Preferences address registration mounts ONCE, in src/app.tsx, inside '
      + 'the Router. Without it every entry point to Preferences does nothing; '
      + 'a second mount replaces the first registration.',
    ).toEqual(['src/app.tsx'])
  })
})
