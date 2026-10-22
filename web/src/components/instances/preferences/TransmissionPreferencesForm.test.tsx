/*
 * Copyright (c) 2026, s0up and the autobrr contributors.
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

import { useAddTorrentPreferences } from "@/hooks/usePreferenceValues"
import { api } from "@/lib/api"
import type { PreferenceField } from "@/types"
import { TransmissionPreferencesForm } from "./TransmissionPreferencesForm"

vi.mock("@/lib/api", () => ({ api: {
  getTransmissionPreferences: vi.fn(),
  updateTransmissionPreferences: vi.fn(),
  getInstancePreferenceView: vi.fn(),
} }))
vi.mock("react-i18next", async importOriginal => ({
  ...await importOriginal<typeof import("react-i18next")>(),
  useTranslation: () => ({ t: (key: string) => key }),
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

afterEach(() => {
  cleanup()
  vi.resetAllMocks()
  vi.unstubAllGlobals()
})

function SavePathConsumer() {
  const preferences = useAddTorrentPreferences(1)
  return <output aria-label="add torrent save path">{preferences.defaults.savePath}</output>
}

it("refreshes the preferences used by the torrent screen after a Transmission save", async () => {
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  vi.mocked(api.getTransmissionPreferences).mockResolvedValue({ "download-dir": "/data/before" })
  vi.mocked(api.updateTransmissionPreferences).mockResolvedValue({ "download-dir": "/data/after" })
  vi.mocked(api.getInstancePreferenceView)
    .mockResolvedValueOnce([{ key: "save_path", value: "/data/before" }] as PreferenceField[])
    .mockResolvedValue([{ key: "save_path", value: "/data/after" }] as PreferenceField[])
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const ui = render(
    <QueryClientProvider client={client}>
      <SavePathConsumer />
      <TransmissionPreferencesForm instanceId={1} />
    </QueryClientProvider>
  )
  await waitFor(() => expect(ui.getByLabelText("add torrent save path").textContent).toBe("/data/before"))
  const input = await ui.findByLabelText("preferences.transmission.torrents.downloadDir")
  fireEvent.change(input, { target: { value: "/data/after" } })
  fireEvent.click(ui.getByRole("button", { name: "preferences.common.saveChanges" }))
  await waitFor(() => expect(api.updateTransmissionPreferences).toHaveBeenCalledWith(1, { "download-dir": "/data/after" }))
  await waitFor(() => expect(ui.getByLabelText("add torrent save path").textContent).toBe("/data/after"))
})
