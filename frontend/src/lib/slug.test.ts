import { describe, expect, it } from 'vitest'
import { slugify } from './slug'

describe('slugify', () => {
  it('reduces a windows drive root to its letter', () => {
    expect(slugify('C:\\')).toBe('c')
    expect(slugify('D:/')).toBe('d')
  })

  // The fold that a delete-every-separator rule got wrong: both of these
  // reduced to `srvab`, so one drive menu offered two rows with one test id.
  it('keeps a UNC server and share apart', () => {
    expect(slugify('\\\\srv\\a-b')).toBe('srv-a-b')
    expect(slugify('\\\\srv\\ab')).toBe('srv-ab')
  })

  it('folds a run of non-alphanumerics to one hyphen and trims the ends', () => {
    expect(slugify('worker · a/b')).toBe('worker-a-b')
    expect(slugify('  Leading and trailing  ')).toBe('leading-and-trailing')
  })

  // Lossy by design, which is why a caller that needs uniqueness resolves the
  // collision itself.
  it('answers the empty string for a label with no alphanumerics', () => {
    expect(slugify('· / ·')).toBe('')
    expect(slugify('')).toBe('')
  })
})
