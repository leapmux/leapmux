---
title: "Device Sync"
description: "How LeapMux keeps a workspace layout in sync across your browsers and devices in near-real-time, and how the turn-end sound picks a single client."
type: docs
weight: 6
---

LeapMux keeps a workspace's **layout** in sync across every client where you have it open — your browser tabs, windows, and devices. Open the same workspace in two places and its tiling tree, tabs, splits, and floating windows stay identical in both, in near-real-time. This is layout sync, not content mirroring: what you type into an agent chat or terminal composer in one client does not echo into another's input area.

## What syncs

When a workspace is open in more than one of your clients, these match within about one frame plus one network round-trip:

- **The tiling tree** — splits, grids, and leaf tiles, including dragging a divider to resize.
- **Tabs** — agent, terminal, file, and image tabs: whether they exist, their order, which tile they live in, and which Worker hosts them.
- **Floating windows** — position, size, opacity, and the tiles inside them.
- **Workspace lifecycle** — creating, renaming, or deleting a workspace updates the sidebar everywhere.
- **Tab titles, file-tab paths, and image-tab references** — these are sent over the Worker's end-to-end-encrypted channel, not the Hub's layout sync, so the Hub never sees them. An image tab carries a reference to the chat message the image came in, never the image itself, so your other clients fetch the picture from the Worker the same way this one did.

For the layout primitives themselves, see [Tabs & Layout](/docs/using/tabs-and-layout/).

## Your own devices and tabs

The common case is one person, several clients. Open a workspace on your laptop and your phone, or in two browser tabs, and a split or tab you create in one appears in the others. There is nothing to set up — no session to start, no toggle; sync is always on. Your edits apply optimistically, appearing instantly before the Hub confirms them.

If two of your clients change the same thing at the same instant, LeapMux resolves it automatically. Last write wins; there is no merge prompt. Every client converges on the same result, and you rarely notice it.

## Presence: who hears the turn-end sound

LeapMux tracks one presence fact per workspace: which client is **active** — the one you most recently used or brought to the front. It exists for a single purpose: when an agent finishes a turn, only the active client plays the turn-end sound, so the same agent does not ring on every open tab and device at once. Your other clients viewing that workspace stay silent. If no clear active client can be determined, LeapMux plays the sound rather than drop it.

You don't choose the active client — it follows whichever client you last used or brought to the front. To keep a particular client quiet regardless, set **Turn-end sound** to **None** in its **Preferences** dialog (see [Settings & Preferences](/docs/using/settings/)). The sound is also skipped for trivial single-exchange turns and plays at most once per minute.

{{< callout type="info" >}}
All tabs and windows of one browser profile count as a **single** client, because they share one login session — so they never compete for the turn-end sound among themselves. A separate browser, another device, or an authorized app is a distinct client.
{{< /callout >}}

There are no other presence features — no avatars, "who's viewing" badges, remote cursors, or typing indicators.

## Who can see a workspace

Workspace access is strictly owner-only: you see — and sync — exactly the workspaces you own.

## Troubleshooting

| Symptom | Likely cause | What to do |
| --- | --- | --- |
| A change in one tab didn't appear in another | The tabs aren't on the same workspace, or one lost its connection | Confirm both show the same workspace; reload the one that's behind. |
| Every device played the turn-end sound when an agent finished | No clear active client could be determined (a tie between clients, or presence not yet settled after a reconnect) | Interact with the client you want active, or set **Turn-end sound** to **None** on the others. Tabs of one browser share a session and never each play the sound. |

For broader diagnostics, see [Troubleshooting](/docs/reference/troubleshooting/).
