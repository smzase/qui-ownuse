/*
 * Copyright (c) 2025-2026, s0up and the autobrr contributors.
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const stubs = vi.hoisted(() => {
  const pending = new Promise<never>(() => {})
  const call = () => pending
  return {
    translation: { t: (key: string) => key, i18n: { language: "en" } },
    api: new Proxy({}, { get: () => call }),
    selfUpdate: { overlay: "hidden", setConfirmOpen: () => {} },
    auth: { user: { role: "admin", permissions: [] as string[] } },
  }
})

vi.mock("react-i18next", async (importOriginal) => ({
  ...await importOriginal<typeof import("react-i18next")>(),
  useTranslation: () => stubs.translation,
}))
vi.mock("@/lib/api", () => ({
  api: stubs.api,
  APIError: class extends Error {},
}))
vi.mock("@/hooks/useSelfUpdate", () => ({ useSelfUpdate: () => stubs.selfUpdate }))
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => stubs.auth }))
vi.mock("@/components/settings/UsersManager", () => ({ UsersManager: () => <div data-testid="section-users" /> }))
vi.mock("@/components/indexers/IndexersPage", () => ({ IndexersPage: () => <div data-testid="section-indexers" /> }))
vi.mock("@/components/settings/GazelleSettingsCard", () => ({ GazelleSettingsCard: () => null }))
vi.mock("@/components/settings/ArrInstancesManager", () => ({ ArrInstancesManager: () => <div data-testid="section-integrations" /> }))
vi.mock("@/components/settings/ClientApiKeysManager", () => ({ ClientApiKeysManager: () => <div data-testid="section-client-api" /> }))
vi.mock("@/components/settings/ExternalProgramsManager", () => ({ ExternalProgramsManager: () => <div data-testid="section-external-programs" /> }))
vi.mock("@/components/settings/NotificationsManager", () => ({ NotificationsManager: () => <div data-testid="section-notifications" /> }))
vi.mock("@/components/settings/DateTimePreferencesForm", () => ({ DateTimePreferencesForm: () => <div data-testid="section-datetime" /> }))
vi.mock("@/components/settings/LogSettingsPanel", () => ({ LogSettingsPanel: () => <div data-testid="section-logs" /> }))
vi.mock("@/components/themes/ThemeSelector", () => ({ ThemeSelector: () => <div data-testid="section-themes" /> }))

import { SETTINGS_NAV_GROUPS, SETTINGS_TABS, type SettingsTab } from "@/components/settings/tabs"
import { Settings } from "./Settings"

// Sections that Settings.tsx renders inline show a translation key that only they render.
const INLINE_MARKERS: Partial<Record<SettingsTab, string>> = {
  application: "application.loading",
  instances: "instances.title",
  "search-cache": "searchCache.disabled",
  api: "apiKeys.cardTitle",
  security: "security.changePassword.title",
}

function isShown(tab: SettingsTab) {
  const marker = INLINE_MARKERS[tab]
  return marker ? screen.queryAllByText(marker).length > 0 : screen.queryByTestId(`section-${tab}`) !== null
}

afterEach(cleanup)
beforeEach(() => {
  stubs.auth.user.role = "admin"
  stubs.auth.user.permissions = []
})

function renderSettings(tab: SettingsTab) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <Settings search={{ tab }} onSearchChange={() => {}} />
    </QueryClientProvider>
  )
}

describe("Settings sections", () => {
  it.each(SETTINGS_TABS)("renders only the %s section when it is active", (tab) => {
    renderSettings(tab)

    expect(isShown(tab)).toBe(true)
    for (const other of SETTINGS_TABS) {
      if (other !== tab) expect(isShown(other), other).toBe(false)
    }
  })

  const restrictedSections = [
    ["users", undefined],
    ["indexers", "manage_global_settings"],
    ["search-cache", "manage_global_settings"],
    ["themes", "manage_global_settings"],
    ["external-programs", "manage_external_programs"],
    ["notifications", "manage_notifications"],
    ["integrations", "manage_arr"],
    ["logs", "manage_logs"],
  ] as const

  it.each(restrictedSections)("hides %s and rejects its direct URL for a user without permission", (tab) => {
    stubs.auth.user.role = "user"
    renderSettings(tab)

    expect(isShown("application")).toBe(true)
    expect(isShown(tab)).toBe(false)
    const label = SETTINGS_NAV_GROUPS.flatMap(group => group.tabs.map(entry => ({ ...entry }))).find(entry => entry.tab === tab)!.labelKey
    expect(within(screen.getByRole("navigation")).queryByRole("button", { name: label })).toBeNull()
  })

  it.each(restrictedSections.filter((entry) => entry[1] !== undefined))("allows %s when its permission is granted", (tab, permission) => {
    stubs.auth.user.role = "user"
    stubs.auth.user.permissions = [permission]
    renderSettings(tab)

    expect(isShown(tab)).toBe(true)
    expect(isShown("application")).toBe(false)
    expect(within(screen.getByRole("navigation")).queryByRole("button", { name: "tabs.users" })).toBeNull()
  })
})
