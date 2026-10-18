/*
 * Copyright (c) 2026, s0up and the autobrr contributors.
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

import { TorrentRefreshButton } from "./TorrentRefreshButton"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  refresh: vi.fn(),
  stream: { resetInstanceStreams: vi.fn() },
  selection: { clearSelection: vi.fn() },
  success: vi.fn(),
  error: vi.fn(),
}))
vi.mock("@/lib/api", () => ({ api: { refreshTorrents: mocks.refresh } }))
vi.mock("@/contexts/SyncStreamContext", () => ({ useSyncStreamManager: () => mocks.stream }))
vi.mock("@/contexts/TorrentSelectionContext", () => ({ useTorrentSelection: () => mocks.selection }))
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock("sonner", () => ({ toast: { success: mocks.success, error: mocks.error } }))
vi.mock("@/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => children,
  TooltipTrigger: ({ children }: { children: React.ReactNode }) => children,
  TooltipContent: () => null,
}))

afterEach(cleanup)
beforeEach(() => vi.clearAllMocks())

function setup(ids = [1]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const onRefresh = vi.fn()
  const view = render(<QueryClientProvider client={client}><TorrentRefreshButton instanceIds={ids} onRefresh={onRefresh} /></QueryClientProvider>)
  return { client, onRefresh, ...view, button: screen.getByRole("button", { name: "header.forceRefresh" }) as HTMLButtonElement }
}

describe("TorrentRefreshButton", () => {
  it("waits for a full sync then discards every loaded page and resets streams and selection", async () => {
    let finish!: () => void
    mocks.refresh.mockReturnValue(new Promise<void>(resolve => { finish = resolve }))
    const { client, onRefresh, button } = setup()
    client.setQueryData(["torrents-list", 1, "", 0], { torrents: [{ name: "" }] })
    client.setQueryData(["torrents-list", 1, "", 3], { torrents: [{ name: "Old page" }] })
    client.setQueryData(["torrents-list", 0], { torrents: [] })
    client.setQueryData(["torrents-list", 2], { torrents: [{ name: "Other instance" }] })
    fireEvent.click(button)
    fireEvent.click(button)
    expect(button.disabled).toBe(true)
    expect(mocks.refresh).toHaveBeenCalledTimes(1)
    expect(onRefresh).not.toHaveBeenCalled()
    await act(async () => finish())
    await waitFor(() => expect(onRefresh).toHaveBeenCalledOnce())
    expect(client.getQueryData(["torrents-list", 1, "", 0])).toBeUndefined()
    expect(client.getQueryData(["torrents-list", 1, "", 3])).toBeUndefined()
    expect(client.getQueryData(["torrents-list", 0])).toBeUndefined()
    expect(client.getQueryData(["torrents-list", 2])).toBeDefined()
    expect(mocks.stream.resetInstanceStreams).toHaveBeenCalledWith([1])
    expect(mocks.selection.clearSelection).toHaveBeenCalledOnce()
    expect(mocks.success).toHaveBeenCalledWith("header.forceRefreshSuccess")
    expect(button.disabled).toBe(false)
  })

  it("refreshes all selected instances", async () => {
    mocks.refresh.mockResolvedValue(undefined)
    const { onRefresh, button } = setup([1, 2])
    fireEvent.click(button)
    await waitFor(() => expect(onRefresh).toHaveBeenCalledOnce())
    expect(mocks.refresh.mock.calls.map(call => call[0])).toEqual([1, 2])
  })

  it("keeps the current list and allows retry after a failed refresh", async () => {
    mocks.refresh.mockRejectedValue(new Error("offline"))
    const { client, onRefresh, button } = setup()
    client.setQueryData(["torrents-list", 1], { torrents: [] })
    fireEvent.click(button)
    await waitFor(() => expect(mocks.error).toHaveBeenCalledOnce())
    expect(client.getQueryData(["torrents-list", 1])).toBeDefined()
    expect(onRefresh).not.toHaveBeenCalled()
    expect(mocks.stream.resetInstanceStreams).not.toHaveBeenCalled()
    expect(button.disabled).toBe(false)
  })

  it("does not reset a different page after navigation", async () => {
    let finish!: () => void
    mocks.refresh.mockReturnValue(new Promise<void>(resolve => { finish = resolve }))
    const { button, onRefresh, unmount } = setup()
    fireEvent.click(button)
    const signal = mocks.refresh.mock.calls[0][1] as AbortSignal
    unmount()
    expect(signal.aborted).toBe(true)
    await act(async () => finish())
    expect(onRefresh).not.toHaveBeenCalled()
    expect(mocks.stream.resetInstanceStreams).not.toHaveBeenCalled()
    expect(mocks.success).not.toHaveBeenCalled()
  })
})
