/*
 * Copyright (c) 2026, s0up and the autobrr contributors.
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { useSyncStreamManager } from "@/contexts/SyncStreamContext"
import { useTorrentSelection } from "@/contexts/TorrentSelectionContext"
import { api } from "@/lib/api"
import { cn } from "@/lib/utils"
import { useQueryClient } from "@tanstack/react-query"
import { RefreshCw } from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

export function TorrentRefreshButton({ instanceIds, onRefresh }: {
  instanceIds: number[]
  onRefresh: () => void
}) {
  const { t } = useTranslation("common")
  const queryClient = useQueryClient()
  const { resetInstanceStreams } = useSyncStreamManager()
  const { clearSelection } = useTorrentSelection()
  const [refreshing, setRefreshing] = useState(false)
  const requestRef = useRef<AbortController | null>(null)

  useEffect(() => () => requestRef.current?.abort(), [])

  const refresh = async () => {
    if (requestRef.current || instanceIds.length === 0) return
    const controller = new AbortController()
    requestRef.current = controller
    setRefreshing(true)
    try {
      const results = await Promise.allSettled(instanceIds.map(id => api.refreshTorrents(id, controller.signal)))
      if (controller.signal.aborted) return
      if (results.some(result => result.status === "rejected")) {
        toast.error(t("header.forceRefreshError"))
        return
      }

      // Unified queries can contain any refreshed member, so discard those too.
      const listQueries = {
        predicate: (query: { queryKey: readonly unknown[] }) =>
          query.queryKey[0] === "torrents-list" &&
          (query.queryKey[1] === 0 || instanceIds.includes(query.queryKey[1] as number) || query.queryKey[8] === true),
      }
      await queryClient.cancelQueries(listQueries)
      if (controller.signal.aborted) return
      resetInstanceStreams(instanceIds)
      queryClient.removeQueries(listQueries)
      clearSelection()
      onRefresh()
      toast.success(t("header.forceRefreshSuccess"))
    } catch {
      if (!controller.signal.aborted) toast.error(t("header.forceRefreshError"))
    } finally {
      if (!controller.signal.aborted) {
        requestRef.current = null
        setRefreshing(false)
      }
    }
  }

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t("header.forceRefresh")}
          aria-busy={refreshing}
          disabled={refreshing || instanceIds.length === 0}
          onClick={() => void refresh()}
        >
          <RefreshCw className={cn("h-4 w-4", refreshing && "animate-spin")} />
        </Button>
      </TooltipTrigger>
      <TooltipContent>{t("header.forceRefresh")}</TooltipContent>
    </Tooltip>
  )
}
