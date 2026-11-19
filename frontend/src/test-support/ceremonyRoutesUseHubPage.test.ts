import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const routeFiles = [
  'src/routes/login.tsx',
  'src/routes/signup.tsx',
  'src/routes/setup.tsx',
  'src/routes/recover-account/index.tsx',
  'src/routes/recover-account/complete.tsx',
  'src/routes/verify-email.tsx',
  'src/routes/elevate.tsx',
  'src/routes/auth/idp/complete-signup.tsx',
]

describe('ceremonyRoutesUseHubPage', () => {
  it('every ceremony route renders through HubPage', () => {
    for (const file of routeFiles) {
      const source = readFileSync(resolve(import.meta.dirname, '..', '..', file), 'utf-8')
      expect(source, `${file} must render <HubPage`).toContain('<HubPage')
    }
  })
})
