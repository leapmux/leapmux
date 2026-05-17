// Frontend build identity, baked at Vite build time from the
// LEAPMUX_* env vars exported by `task build-frontend` (see
// `Taskfile.yaml` `build-frontend`). Lives in its own module so the
// SSR/prerender entry (`entry-server.tsx`) can import the values
// without pulling in `systemInfo.ts`'s auth/platform graph.

export interface BuildInfo {
  version: string
  commitHash: string
  commitTime: string
  buildTime: string
  branch: string
}

export const frontendBuildInfo: BuildInfo = {
  version: import.meta.env.LEAPMUX_VERSION || '',
  commitHash: import.meta.env.LEAPMUX_COMMIT_HASH || '',
  commitTime: import.meta.env.LEAPMUX_COMMIT_TIME || '',
  buildTime: import.meta.env.LEAPMUX_BUILD_TIME || '',
  branch: import.meta.env.LEAPMUX_BRANCH || '',
}
