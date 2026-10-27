import { createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'

const requireRenovate = createRequire(new URL('../.github/renovate/package.json', import.meta.url))

// Renovate exposes no public policy API. Resolve internals from its dedicated pinned manifest.
export function renovateModule(relative) {
  return import(pathToFileURL(requireRenovate.resolve(`renovate/dist/${relative}`)).href)
}
