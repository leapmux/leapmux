import process from 'node:process'
import { registerCacheReplacement } from './prune-build-caches.mjs'

// GitHub runs this before the build and the separate pruning entrypoint after success.
try {
  registerCacheReplacement()
}
catch (error) {
  console.error(`Cache replacement registration failed: ${error.message}`)
  process.exitCode = 1
}
