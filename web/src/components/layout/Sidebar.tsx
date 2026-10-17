/*
 * Copyright (c) 2025-2026, s0up and the autobrr contributors.
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger
} from "@/components/ui/dropdown-menu"
import { UnifiedScopeDropdownSection } from "@/components/layout/UnifiedScopeDropdownSection"
import { Logo } from "@/components/ui/Logo"
import { NapsterLogo } from "@/components/ui/NapsterLogo"
import { Separator } from "@/components/ui/separator"
import { SwizzinLogo } from "@/components/ui/SwizzinLogo"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { UpdateBanner } from "@/components/ui/UpdateBanner"
import { useAuth } from "@/hooks/useAuth"
import { useCrossSeedInstanceState } from "@/hooks/useCrossSeedInstanceState"
import { usePersistedUnifiedInstanceFilter } from "@/hooks/usePersistedUnifiedInstanceFilter"
import { usePersistedSidebarNavigation, type SidebarNavigationId } from "@/hooks/usePersistedSidebarNavigation"
import { useTheme } from "@/hooks/useTheme"
import { changeLanguage, languageNames, supportedLanguages } from "@/i18n"
import { api } from "@/lib/api"
import { getAppVersion } from "@/lib/build-info"
import { demoLinks, isDemo } from "@/lib/demo"
import { normalizeUnifiedInstanceIds } from "@/lib/instances"
import { cn } from "@/lib/utils"
import { useQuery } from "@tanstack/react-query"
import { Link, useLocation, useNavigate, useSearch } from "@tanstack/react-router"
import { navigateWithSearch } from "@/lib/router-search"
import {
  Archive,
  ArrowDown,
  ArrowUp,
  Check,
  Code,
  Copyright,
  FileText,
  GitBranch,
  Globe,
  HardDrive,
  HardDriveDownload,
  Home,
  Loader2,
  LogOut,
  Rss,
  Search,
  SearchCode,
  Settings,
  Settings2,
  Zap
} from "lucide-react"
import { useCallback, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

interface NavItem {
  id: SidebarNavigationId
  titleKey: string
  href: string
  icon: React.ComponentType<{ className?: string }>
  params?: Record<string, string>
  search?: Record<string, unknown>
  isActive?: (pathname: string, search: Record<string, unknown> | undefined) => boolean
}

const navigation: NavItem[] = [
  {
    id: "dashboard",
    titleKey: "nav.dashboard",
    href: "/dashboard",
    icon: Home,
  },
  {
    id: "search",
    titleKey: "nav.search",
    href: "/search",
    icon: Search,
  },
  {
    id: "cross-seed",
    titleKey: "nav.crossSeed",
    href: "/cross-seed",
    icon: GitBranch,
    params: {},
  },
  {
    id: "automations",
    titleKey: "nav.automations",
    href: "/automations",
    icon: Zap,
  },
  {
    id: "backups",
    titleKey: "nav.backups",
    href: "/backups",
    icon: Archive,
  },
  {
    id: "rss",
    titleKey: "nav.rss",
    href: "/rss",
    icon: Rss,
  },
  {
    id: "settings",
    titleKey: "nav.settings",
    href: "/settings",
    icon: Settings,
    isActive: (pathname, search) => pathname === "/settings" && search?.tab !== "logs",
  },
  {
    id: "logs",
    titleKey: "nav.logs",
    href: "/settings",
    icon: FileText,
    search: { tab: "logs" },
    isActive: (pathname, search) => pathname === "/settings" && search?.tab === "logs",
  },
]

const navigationById = new Map(navigation.map(item => [item.id, item]))

export function Sidebar() {
  const { t, i18n } = useTranslation("common")
  const location = useLocation()
  const navigate = useNavigate()
  const routeSearch = useSearch({ strict: false }) as Record<string, unknown> | undefined
  const { logout } = useAuth()
  const { theme } = useTheme()
  const [sidebarNavigation, setSidebarNavigation] = usePersistedSidebarNavigation()
  const [customizerOpen, setCustomizerOpen] = useState(false)

  const { data: instances } = useQuery({
    queryKey: ["instances"],
    queryFn: () => api.getInstances(),
  })
  const activeInstances = useMemo(
    () => (instances ?? []).filter(instance => instance.isActive),
    [instances]
  )
  // Transmission instances render below qBittorrent ones, separated by a
  // single divider; the divider only appears when both groups are present.
  const qbittorrentInstances = useMemo(
    () => activeInstances.filter(instance => instance.clientType !== "transmission"),
    [activeInstances]
  )
  const transmissionInstances = useMemo(
    () => activeInstances.filter(instance => instance.clientType === "transmission"),
    [activeInstances]
  )
  const hasActiveQbittorrent = qbittorrentInstances.length > 0
  const orderedNavigation = useMemo(
    () => sidebarNavigation.order
      .map(id => navigationById.get(id))
      .filter((item): item is NavItem => item !== undefined),
    [sidebarNavigation.order]
  )
  const visibleNavigation = useMemo(
    () => orderedNavigation.filter(item => !sidebarNavigation.hidden.includes(item.id) && (item.id !== "rss" || hasActiveQbittorrent)),
    [hasActiveQbittorrent, orderedNavigation, sidebarNavigation.hidden]
  )
  const moveNavigationItem = useCallback((id: SidebarNavigationId, direction: -1 | 1) => {
    setSidebarNavigation(previous => {
      const index = previous.order.indexOf(id)
      const targetIndex = index + direction
      if (id === "dashboard" || index < 1 || targetIndex < 1 || targetIndex >= previous.order.length) {
        return previous
      }

      const order = [...previous.order]
      const current = order[index]
      order[index] = order[targetIndex]
      order[targetIndex] = current
      return { ...previous, order }
    })
  }, [setSidebarNavigation])
  const toggleNavigationItem = useCallback((id: SidebarNavigationId, visible: boolean) => {
    if (id === "dashboard") return
    setSidebarNavigation(previous => ({
      ...previous,
      hidden: visible
        ? previous.hidden.filter(hiddenId => hiddenId !== id)
        : [...previous.hidden, id],
    }))
  }, [setSidebarNavigation])
  const activeInstanceIds = useMemo(
    () => activeInstances.map(instance => instance.id),
    [activeInstances]
  )
  const [persistedUnifiedFilter, saveUnifiedFilter] = usePersistedUnifiedInstanceFilter()
  const normalizedUnifiedInstanceIds = useMemo(
    () => normalizeUnifiedInstanceIds(persistedUnifiedFilter, activeInstanceIds),
    [persistedUnifiedFilter, activeInstanceIds]
  )
  const effectiveUnifiedInstanceIds = normalizedUnifiedInstanceIds.length > 0? normalizedUnifiedInstanceIds: activeInstanceIds
  const isAllInstancesActive = location.pathname === "/instances" || location.pathname === "/instances/"
  const hasMultipleActiveInstances = activeInstances.length > 1
  const applyUnifiedScope = useCallback((nextIds: number[]) => {
    const normalizedIds = normalizeUnifiedInstanceIds(nextIds, activeInstanceIds)
    saveUnifiedFilter(normalizedIds)
    const nextSearch: Record<string, unknown> = isAllInstancesActive ? { ...(routeSearch || {}) } : {}

    navigateWithSearch({
      navigate,
      to: "/instances",
      search: nextSearch,
      replace: isAllInstancesActive,
    })
  }, [activeInstanceIds, isAllInstancesActive, navigate, routeSearch, saveUnifiedFilter])
  const toggleUnifiedScopeInstance = useCallback((instanceId: number) => {
    const currentlySelected = effectiveUnifiedInstanceIds.includes(instanceId)
    const nextIds = currentlySelected? effectiveUnifiedInstanceIds.filter(id => id !== instanceId): [...effectiveUnifiedInstanceIds, instanceId]

    if (nextIds.length === 0) {
      return
    }

    applyUnifiedScope(nextIds)
  }, [applyUnifiedScope, effectiveUnifiedInstanceIds])
  const hasConfiguredInstances = (instances?.length ?? 0) > 0

  const { state: crossSeedInstanceState } = useCrossSeedInstanceState()

  const renderInstanceLink = (instance: { id: number; name: string; connected?: boolean; clientType?: string }) => {
    const instancePath = `/instances/${instance.id}`
    const isActive = location.pathname === instancePath || location.pathname.startsWith(`${instancePath}/`)
    const csState = crossSeedInstanceState[instance.id]
    const hasRss =
      instance.clientType === "qbittorrent" &&
      (csState?.rssEnabled || csState?.rssRunning)
    const hasSearch = csState?.searchRunning

    return (
      <Link
        key={instance.id}
        to="/instances/$instanceId"
        params={{ instanceId: instance.id.toString() }}
        className={cn(
          "flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-all duration-200 ease-out",
          isActive? "bg-sidebar-primary text-sidebar-primary-foreground": "text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
        )}
      >
        {instance.clientType === "transmission" ? (
          <HardDriveDownload className="h-4 w-4 flex-shrink-0" />
        ) : (
          <HardDrive className="h-4 w-4 flex-shrink-0" />
        )}
        <span className="truncate max-w-36" title={instance.name}>{instance.name}</span>
        <span className="ml-auto flex items-center gap-1.5">
          {hasRss && (
            <Tooltip>
              <TooltipTrigger asChild>
                <span className="flex items-center">
                  {csState?.rssRunning ? (
                    <Loader2 className={cn(
                      "h-3 w-3 animate-spin",
                      isActive ? "text-sidebar-primary-foreground/70" : "text-sidebar-foreground/70"
                    )} />
                  ) : (
                    <Rss className={cn(
                      "h-3 w-3",
                      isActive ? "text-sidebar-primary-foreground/70" : "text-sidebar-foreground/70"
                    )} />
                  )}
                </span>
              </TooltipTrigger>
              <TooltipContent side="right" className="text-xs">
                {csState?.rssRunning ? t("sidebar.rssRunning") : t("sidebar.rssEnabled")}
              </TooltipContent>
            </Tooltip>
          )}
          {hasSearch && (
            <Tooltip>
              <TooltipTrigger asChild>
                <span className="flex items-center">
                  <SearchCode className={cn(
                    "h-3 w-3",
                    isActive ? "text-sidebar-primary-foreground/70" : "text-sidebar-foreground/70"
                  )} />
                </span>
              </TooltipTrigger>
              <TooltipContent side="right" className="text-xs">
                {t("sidebar.scanRunning")}
              </TooltipContent>
            </Tooltip>
          )}
          <span
            className={cn(
              "h-2 w-2 rounded-full flex-shrink-0",
              instance.connected ? "bg-green-500" : "bg-red-500"
            )}
          />
        </span>
      </Link>
    )
  }

  const appVersion = getAppVersion()

  const brand = (
    <span className="flex items-center gap-2">
      {theme === "swizzin" ? (
        <SwizzinLogo className="h-5 w-5" />
      ) : theme === "napster" ? (
        <NapsterLogo className="h-5 w-5" />
      ) : (
        <Logo className="h-5 w-5" />
      )}
      qui
    </span>
  )

  return (
    <div className="flex h-full w-64 flex-col border-r bg-sidebar border-sidebar-border">
      <div className="flex items-center justify-between p-6">
        <h2 className="flex items-center text-lg font-semibold text-sidebar-foreground">
          {isDemo ? (
            <a href="/" target="_top" className="flex items-center gap-2 hover:opacity-80">{brand}</a>
          ) : brand}
        </h2>
        <Dialog open={customizerOpen} onOpenChange={setCustomizerOpen}>
          <Tooltip>
            <TooltipTrigger asChild>
              <DialogTrigger asChild>
                <Button variant="ghost" size="icon" className="h-8 w-8 text-sidebar-foreground/70 hover:text-sidebar-foreground" aria-label={t("sidebar.customize")}>
                  <Settings2 className="h-4 w-4" />
                </Button>
              </DialogTrigger>
            </TooltipTrigger>
            <TooltipContent side="right" className="text-xs">{t("sidebar.customize")}</TooltipContent>
          </Tooltip>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>{t("sidebar.customizeTitle")}</DialogTitle>
            </DialogHeader>
            <div className="space-y-1">
              {sidebarNavigation.order.map((id, index) => {
                const item = navigationById.get(id)
                if (!item) return null
                const isDashboard = id === "dashboard"
                const isVisible = isDashboard || !sidebarNavigation.hidden.includes(id)
                return (
                  <div key={id} className="flex items-center gap-2 rounded-md border px-3 py-2">
                    <Checkbox
                      checked={isVisible}
                      disabled={isDashboard}
                      onCheckedChange={(checked) => toggleNavigationItem(id, checked === true)}
                      aria-label={isVisible ? t("sidebar.hideItem", { item: t(item.titleKey) }) : t("sidebar.showItem", { item: t(item.titleKey) })}
                    />
                    <span className="min-w-0 flex-1 truncate text-sm">{t(item.titleKey)}</span>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      disabled={isDashboard || index <= 1}
                      onClick={() => moveNavigationItem(id, -1)}
                      aria-label={t("sidebar.moveUp")}
                    >
                      <ArrowUp className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      disabled={isDashboard || index === sidebarNavigation.order.length - 1}
                      onClick={() => moveNavigationItem(id, 1)}
                      aria-label={t("sidebar.moveDown")}
                    >
                      <ArrowDown className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                )
              })}
            </div>
          </DialogContent>
        </Dialog>
      </div>

      <nav className="flex flex-1 min-h-0 flex-col overflow-y-auto px-3">
        <div className="space-y-1">
          {!isDemo && visibleNavigation.map((item) => {
            const Icon = item.icon
            const isActive = item.isActive? item.isActive(location.pathname, routeSearch): location.pathname === item.href

            return (
              <Link
                key={item.id}
                to={item.href}
                params={item.params}
                search={item.search}
                className={cn(
                  "flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-all duration-200 ease-out",
                  isActive? "bg-sidebar-primary text-sidebar-primary-foreground": "text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
                )}
              >
                <Icon className="h-4 w-4" />
                {t(item.titleKey)}
              </Link>
            )
          })}
          {isDemo && demoLinks.map((link) => (
            <a
              key={link.href}
              href={link.href}
              target="_top"
              className="flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium text-sidebar-foreground transition-all duration-200 ease-out hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
            >
              <link.icon className="h-4 w-4" />
              {t(link.labelKey)}
            </a>
          ))}
        </div>

        <Separator className="my-4" />

        <p className="px-3 text-xs font-semibold uppercase tracking-wider text-sidebar-foreground/70">
          {t("sidebar.instances")}
        </p>
        <div className="mt-1 space-y-1 pr-1">
          {hasMultipleActiveInstances && (
            <>
              <UnifiedScopeDropdownSection
                activeInstances={activeInstances}
                effectiveUnifiedInstanceIds={effectiveUnifiedInstanceIds}
                isAllInstancesRoute={isAllInstancesActive}
                onResetUnifiedScope={() => applyUnifiedScope(activeInstanceIds)}
                onToggleUnifiedScopeInstance={toggleUnifiedScopeInstance}
                scopeKeyPrefix="sidebar-scope"
                variant="sidebar"
              />
              <Separator className="my-2" />
            </>
          )}
          {qbittorrentInstances.map((instance) => renderInstanceLink(instance))}
          {transmissionInstances.length > 0 && (
            <>
              <Separator className="my-2" />
              {transmissionInstances.map((instance) => renderInstanceLink(instance))}
            </>
          )}
          {activeInstances.length === 0 && (
            <p className="px-3 py-2 text-sm text-sidebar-foreground/50">
              {hasConfiguredInstances ? t("sidebar.allInstancesDisabled") : t("sidebar.noInstancesConfigured")}
            </p>
          )}
        </div>

        <div className="mt-auto space-y-3 pt-3">
          <UpdateBanner />

          {!isDemo && (
            <Button
              variant="ghost"
              className="w-full justify-start"
              onClick={() => logout()}
            >
              <LogOut className="mr-2 h-4 w-4" />
              {t("sidebar.logout")}
            </Button>
          )}
        </div>
      </nav>

      <div className="flex-shrink-0 p-3">
        <Separator className="mx-3 mb-3" />

        <div className="flex items-center justify-between px-3 pb-3">
          <div className="flex flex-col gap-1 text-[10px] text-sidebar-foreground/40 select-none">
            <span className="font-medium text-sidebar-foreground/50">{t("sidebar.version", { version: appVersion })}</span>
            <div className="flex items-center gap-1">
              <Copyright className="h-2.5 w-2.5" />
              <span>{new Date().getFullYear()} autobrr</span>
            </div>
          </div>
          <div className="flex items-center gap-0.5">
            <DropdownMenu>
              <Tooltip>
                <TooltipTrigger asChild>
                  <DropdownMenuTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-6 w-6 text-sidebar-foreground/40 hover:text-sidebar-foreground"
                    >
                      <Globe className="h-3.5 w-3.5" />
                    </Button>
                  </DropdownMenuTrigger>
                </TooltipTrigger>
                <TooltipContent side="top" className="text-xs">
                  {t("sidebar.language")}
                </TooltipContent>
              </Tooltip>
              <DropdownMenuContent align="end" side="top">
                {supportedLanguages.map((lng) => (
                  <DropdownMenuItem
                    key={lng}
                    onClick={() => changeLanguage(lng)}
                    className="flex items-center justify-between gap-4"
                  >
                    {languageNames[lng]}
                    {i18n.language === lng && <Check className="h-3.5 w-3.5" />}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
            <Button
              variant="ghost"
              size="icon"
              className="h-6 w-6 text-sidebar-foreground/40 hover:text-sidebar-foreground"
              asChild
            >
              <a
                href="https://github.com/autobrr/qui"
                target="_blank"
                rel="noopener noreferrer"
                aria-label={t("sidebar.viewOnGitHub")}
              >
                <Code className="h-3.5 w-3.5" />
              </a>
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
