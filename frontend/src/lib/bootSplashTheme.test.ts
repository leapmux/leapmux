import { describe, expect, it } from 'vitest'
import { bootSplashDark, bootSplashLight, resolveBootPolarity } from './bootSplashTheme'

describe('resolveBootPolarity', () => {
  it('honours an explicit light or dark pin', () => {
    expect(resolveBootPolarity('light', true)).toBe('light')
    expect(resolveBootPolarity('dark', false)).toBe('dark')
  })

  it('follows the OS when mode is system or absent', () => {
    expect(resolveBootPolarity('system', true)).toBe('dark')
    expect(resolveBootPolarity('system', false)).toBe('light')
    expect(resolveBootPolarity(undefined, true)).toBe('dark')
    expect(resolveBootPolarity('nope', false)).toBe('light')
  })
})

describe('boot splash palette', () => {
  it('uses Default theme backgrounds that disagree by polarity', () => {
    expect(bootSplashLight.background).not.toBe(bootSplashDark.background)
    expect(bootSplashLight.foreground).not.toBe(bootSplashDark.foreground)
  })
})
