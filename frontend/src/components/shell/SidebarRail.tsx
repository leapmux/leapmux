import type { Component } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import PanelLeftOpen from 'lucide-solid/icons/panel-left-open'
import PanelRightOpen from 'lucide-solid/icons/panel-right-open'
import { For, Show } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import * as styles from './CollapsibleSidebar.css'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface SidebarRailProps {
  /** Section definitions. */
  sections: SidebarSectionDef[]
  /** Which side the sidebar is on (determines expand icon direction). */
  side: 'left' | 'right'
  /** Expand the outer Resizable panel. */
  onExpand: () => void
  /** Open a section and expand the sidebar. */
  onExpandSection: (sectionId: string) => void
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export const SidebarRail: Component<SidebarRailProps> = (props) => {
  const ExpandIcon = () => props.side === 'left' ? PanelLeftOpen : PanelRightOpen
  const railVariant = () => props.side === 'left' ? styles.sidebarRailLeft : styles.sidebarRailRight

  const topSections = () => props.sections.filter(
    s => s.visible !== false && (s.railPosition ?? 'top') === 'top' && !s.railOnly,
  )
  const bottomSections = () => props.sections.filter(
    s => s.visible !== false && s.railPosition === 'bottom',
  )
  const railOnlyTop = () => props.sections.filter(
    s => s.visible !== false && s.railOnly && (s.railPosition ?? 'top') === 'top',
  )

  return (
    <div class={`${styles.sidebarRail} ${railVariant()}`}>
      {/* Expand button */}
      <IconButton icon={ExpandIcon()} iconSize="lg" size="lg" title={`Expand ${props.side} sidebar`} onClick={() => props.onExpand()} />

      {/* Top-positioned section icons + badges */}
      <For each={topSections()}>
        {section => (
          <>
            <IconButton
              icon={section.railIcon}
              iconSize="lg"
              size="lg"
              title={section.railTitle ?? section.title}
              onClick={() => {
                props.onExpandSection(section.id)
                props.onExpand()
              }}
            />
            <Show when={section.railBadge}>
              {section.railBadge?.()}
            </Show>
          </>
        )}
      </For>

      {/* Rail-only top sections */}
      <For each={railOnlyTop()}>
        {section => (
          <Show
            when={section.railElement}
            fallback={<IconButton icon={section.railIcon} iconSize="lg" size="lg" title={section.railTitle ?? section.title} />}
          >
            {section.railElement}
          </Show>
        )}
      </For>

      {/* Bottom-positioned sections (pushed to bottom) */}
      <Show when={bottomSections().length > 0}>
        <div class={styles.marginTopAuto}>
          <For each={bottomSections()}>
            {section => (
              <Show
                when={section.railElement}
                fallback={(
                  <IconButton
                    icon={section.railIcon}
                    iconSize="lg"
                    size="lg"
                    title={section.railTitle ?? section.title}
                    onClick={() => {
                      if (!section.railOnly) {
                        props.onExpandSection(section.id)
                      }
                      props.onExpand()
                    }}
                  />
                )}
              >
                {section.railElement}
              </Show>
            )}
          </For>
        </div>
      </Show>
    </div>
  )
}
