import type { ParentComponent } from 'solid-js'
import type { TunnelStore } from '~/stores/tunnel.store'
import { useContext } from 'solid-js'
import { createStableContext } from '~/lib/createStableContext'

const TunnelContext = createStableContext<TunnelStore>('context/TunnelContext')

export const TunnelProvider: ParentComponent<{ store: TunnelStore }> = (props) => {
  // eslint-disable-next-line solid/reactivity -- store is a stable object, not a reactive primitive
  const store = props.store
  return (
    <TunnelContext.Provider value={store}>
      {props.children}
    </TunnelContext.Provider>
  )
}

export function useTunnel(): TunnelStore | undefined {
  return useContext(TunnelContext)
}
