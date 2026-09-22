import { expect, test } from './fixtures'

/**
 * The shared tab must not carry one spec's emulation into the next.
 *
 * Every spec in this project runs in ONE browser tab, which the fixture resets
 * between tests. A reset that misses a knob is invisible at the site that set
 * it and invisible at the site that breaks: `196` asserted that a segmented
 * control slides, and it failed because `074` and `186` -- two files and two
 * hundred tests earlier -- had emulated `prefers-reduced-motion: reduce`, which
 * suppresses the transition. The report named the pill group.
 *
 * This file runs AFTER every spec that emulates a media feature (074, 186, 196
 * are the three), so a reset that stops being total fails HERE, in a test whose
 * name states the actual fault.
 *
 * `fixtures.ts` holds the matching structural half: the reset returns
 * `Required<Parameters<Page['emulateMedia']>[0]>`, so a Playwright release that
 * adds a sixth media feature fails the type check until the reset names it.
 */
test.describe('shared tab isolation', () => {
  test('starts every test with the media emulation reset', async ({ page }) => {
    const media = await page.evaluate(() => ({
      colorScheme: matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light',
      contrastMore: matchMedia('(prefers-contrast: more)').matches,
      forcedColors: matchMedia('(forced-colors: active)').matches,
      print: matchMedia('print').matches,
      reducedMotion: matchMedia('(prefers-reduced-motion: reduce)').matches,
    }))

    // Each value is the one the reset states EXPLICITLY, never the machine's
    // own setting. A developer who turns on Reduce Motion or Increase Contrast
    // in the OS would otherwise fail a suite that is not about either.
    expect(media).toEqual({
      colorScheme: 'light',
      contrastMore: false,
      forcedColors: false,
      print: false,
      reducedMotion: false,
    })
  })
})
