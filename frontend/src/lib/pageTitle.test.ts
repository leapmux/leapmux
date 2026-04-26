import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { setDashboardTitle, setPageTitle, setWorkspaceTitle } from './pageTitle'

const ORIGINAL_TITLE = document.title

beforeEach(() => {
  document.title = ''
})
afterEach(() => {
  document.title = ORIGINAL_TITLE
})

describe('setPageTitle', () => {
  it('renders "$part - LeapMux" for a non-empty part', () => {
    setPageTitle('Login')
    expect(document.title).toBe('Login - LeapMux')
  })

  it('falls back to bare "LeapMux" when part is empty', () => {
    setPageTitle('')
    expect(document.title).toBe('LeapMux')
  })

  it('handles parts with separator characters', () => {
    setPageTitle('Org - Settings')
    expect(document.title).toBe('Org - Settings - LeapMux')
  })

  it('overwrites the previous title on each call', () => {
    setPageTitle('First')
    setPageTitle('Second')
    expect(document.title).toBe('Second - LeapMux')
  })
})

describe('setWorkspaceTitle', () => {
  it('uses the workspace title verbatim', () => {
    setWorkspaceTitle('My Workspace')
    expect(document.title).toBe('My Workspace - LeapMux')
  })

  it('falls back to "Untitled" when the workspace title is undefined', () => {
    setWorkspaceTitle(undefined)
    expect(document.title).toBe('Untitled - LeapMux')
  })

  it('falls back to "Untitled" when the workspace title is null', () => {
    setWorkspaceTitle(null)
    expect(document.title).toBe('Untitled - LeapMux')
  })

  it('falls back to "Untitled" when the workspace title is an empty string', () => {
    setWorkspaceTitle('')
    expect(document.title).toBe('Untitled - LeapMux')
  })
})

describe('setDashboardTitle', () => {
  it('renders "Dashboard - LeapMux"', () => {
    setDashboardTitle()
    expect(document.title).toBe('Dashboard - LeapMux')
  })
})
