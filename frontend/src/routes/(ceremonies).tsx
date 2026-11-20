import type { JSX } from 'solid-js'
import { useLocation } from '@solidjs/router'
import { onCleanup, Show } from 'solid-js'
import { HubPage } from '~/components/common/HubPage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { normalizeRoutePath } from '~/lib/postAuthNavigate'

/**
 * How the `(ceremonies)` layout wraps one ceremony route: the signed-out gate
 * and the optional desktop slot, declared ON the route module it configures.
 *
 * `gate` picks what `SignedOutOnly` does with a signed-in arrival:
 *
 * - `redirect` (the credential pages) bounces to `?redirect=` or the app.
 * - `explain` (the one page whose address carries a single-use token) renders
 *   the conflict panel instead of a silent bounce that loses the tokened
 *   address from the tab's history.
 * - `false` keeps the route open: `verify-email` and `elevate` serve
 *   signed-in visitors mid-flow, and `auth/idp/complete-signup` completes a
 *   provider sign-up through a token of its own.
 *
 * `inDesktop` replaces HubPage's `ContinueInBrowser` fallback in desktop
 * remote-Hub mode; without it, HubPage falls back to `ContinueInBrowser`.
 */
export interface CeremonyConfig {
  gate: 'redirect' | 'explain' | false
  inDesktop?: (isCurrentArrival: () => boolean) => JSX.Element
}

/**
 * Every ceremony route module's `ceremonyConfig`, keyed by module path.
 *
 * The escaped parens are load-bearing: the glob engine reads a bare `(…)` as a
 * group, so an unescaped `(ceremonies)` matches nothing.
 */
const ceremonyConfigModules = import.meta.glob(
  [
    './\\(ceremonies\\)/**/*.{ts,tsx}',
    '!./\\(ceremonies\\)/**/*.test.*',
    '!./\\(ceremonies\\)/**/*.spec.*',
    '!./\\(ceremonies\\)/**/*.css.*',
  ],
  { eager: true, import: 'ceremonyConfig' },
) as Record<string, CeremonyConfig>

/**
 * The module-path keys of `ceremonyConfigModules` as route addresses: the file
 * path under the group minus its extension, with a trailing `index` naming its
 * directory. `recover-account/index.tsx` is `/recover-account`; every other
 * file names its own segment. Lowercased because the router matches a static
 * segment case-insensitively, so the lookup must read `/Login` as `/login`.
 */
function routeAddress(modulePath: string): string {
  const segments = modulePath
    .split('/')
    // the `./` and `(ceremonies)` prefix segments
    .slice(2)
    .join('/')
    .replace(/\.(?:ts|tsx)$/, '')
    .split('/')
  if (segments.at(-1) === 'index')
    segments.pop()
  return `/${segments.join('/')}`.toLowerCase()
}

/** Every ceremony route module's config, keyed by its address. */
export const ceremonyConfigs: ReadonlyMap<string, CeremonyConfig> = new Map(
  Object.entries(ceremonyConfigModules).map(([modulePath, config]) => [
    routeAddress(modulePath),
    config,
  ]),
)

/**
 * The config for one ceremony address. Throws when the address maps to no
 * route module: the layout only mounts for its own children, so a miss is a
 * derivation gap between the map and the router, and a silent fallback there
 * would drop the route's gate. An address the router matches with a trailing
 * slash reads as the same one here.
 */
export function ceremonyConfigFor(address: string): CeremonyConfig {
  const config = ceremonyConfigs.get(normalizeRoutePath(address).toLowerCase())
  if (!config)
    throw new Error(`no ceremonyConfig for ${address}`)
  return config
}

/**
 * Parent of every ceremony route (`/login`, `/signup`, `/setup`,
 * `/verify-email`, `/elevate`, `/recover-account`, `/recover-account/complete`,
 * `/auth/idp/complete-signup`). The parenthesized file name is a route GROUP:
 * it adds no URL segment, so the group's routes keep their exact addresses.
 *
 * This layout owns the `<HubPage>` wrapper every ceremony route needs — a new
 * route under the group renders through it by existing — and reads the route's
 * `ceremonyConfig` for the two decisions that differ per route. The gate wraps
 * the PAGE and HubPage wraps the CONTENT, so `SignedOutOnly` still runs when
 * HubPage falls back to its desktop view; `login` passes `BrowserSignIn` as
 * that view, because the desktop app cannot use the Hub's own sign-in form.
 *
 * The keyed Show remounts the subtree per address: `SignedOutOnly` decides
 * once per mount on the arrival it saw, and a reused instance would answer a
 * previous page's arrival after this one navigated in.
 */
export default function CeremonyLayout(props: { children?: JSX.Element }) {
  const location = useLocation()
  return (
    <Show keyed when={location.pathname}>
      {(pathname) => {
        const { gate, inDesktop } = ceremonyConfigFor(pathname)
        // This owner survives the guard hiding a form that just signed in.
        // A different pathname ends this arrival. Query and fragment changes
        // stay on the same arrival and can update its destination.
        let mounted = true
        onCleanup(() => {
          mounted = false
        })
        const isCurrentArrival = () => mounted && location.pathname === pathname
        const page = () => (
          <HubPage inDesktop={inDesktop && (() => inDesktop(isCurrentArrival))}>
            {props.children}
          </HubPage>
        )
        return gate === false
          ? page()
          : (
              <SignedOutOnly whenSignedIn={gate}>
                {page()}
              </SignedOutOnly>
            )
      }}
    </Show>
  )
}
