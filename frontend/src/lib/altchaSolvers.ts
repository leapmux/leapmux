// Dynamic ALTCHA solver registration.
//
// The altcha widget build pre-registers solvers for the SHA and PBKDF2
// families only. The memory-hard workers (SCRYPT, ARGON2ID) ship in the
// package as sibling worker files speaking the same message protocol, but
// are not wired into the widget's registry — and their WASM payloads are
// heavy enough that they should not sit in the default bundle. This module
// registers them on demand, keyed on the algorithm the hub reports /
// issues, so an admin switching algorithms needs no frontend deploy.
//
// Registration is an idempotent map set: calling this repeatedly for the
// same algorithm just re-points the entry at the same (browser-cached)
// worker chunk.

interface AltchaWorkerGlobal {
  algorithms: Map<string, () => Worker | Promise<Worker>>
}

declare global {
  interface Window {
    $altcha: AltchaWorkerGlobal
  }
}

// The challenge carries the authoritative algorithm (it is what must be
// solved), but it may be empty or missing in malformed responses — treat
// those as nothing to register rather than throwing.
export async function ensureAltchaSolver(algorithm: string | undefined | null): Promise<void> {
  const registry = typeof window !== 'undefined' ? window.$altcha?.algorithms : undefined
  if (!registry || !algorithm || registry.has(algorithm)) {
    return
  }

  switch (algorithm) {
    case 'SCRYPT': {
      // ?url emits the worker file as its own asset and hands back its
      // URL; the dynamic import makes that asset a lazily-fetched chunk.
      const { default: workerUrl } = await import('altcha/workers/scrypt?url')
      registry.set('SCRYPT', () => new Worker(workerUrl, { name: 'altcha-scrypt' }))
      return
    }
    case 'ARGON2ID': {
      const { default: workerUrl } = await import('altcha/workers/argon2id?url')
      registry.set('ARGON2ID', () => new Worker(workerUrl, { name: 'altcha-argon2id' }))
      break
    }
    default:
      // SHA-* and PBKDF2/* are pre-registered by the 'altcha' import.
      // Anything else is either pre-registered or genuinely unsupported;
      // the widget surfaces "Unsupported algorithm" on solve.
      break
  }
}
