/*
 * Copyright (c) 2025-2026, s0oup and the autobrr contributors.
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

import { Button } from "@/components/ui/button"
import { FieldHelp } from "@/components/ui/field-help"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { api } from "@/lib/api"
import { formatErrorMessage } from "@/lib/format-error-message"
import type { TransmissionPreferences } from "@/types"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Gauge, Globe, HardDrive, RefreshCw, Users } from "lucide-react"
import { useEffect, useState, type FormEvent } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { PreferencesFormShell } from "./PreferencesFormShell"

// Transmission alt-speed day bitmasks: sun=1 mon=2 tue=4 wed=8 thu=16 fri=32 sat=64.
const DAY_EVERYDAY = 127
const DAY_WEEKDAYS = 62
const DAY_WEEKENDS = 65

const ENCRYPTION_PREFER = "preferred"
const ENCRYPTION_ALLOW = "tolerated"
const ENCRYPTION_REQUIRE = "required"

function minutesToTimeInput(minutes: number | undefined): string {
  const total = minutes ?? 0
  const hours = Math.floor(total / 60) % 24
  const mins = total % 60
  return `${hours.toString().padStart(2, "0")}:${mins.toString().padStart(2, "0")}`
}

function timeInputToMinutes(value: string): number {
  const [hours, mins] = value.split(":").map(part => parseInt(part, 10))
  if (Number.isNaN(hours) || Number.isNaN(mins)) return 0
  return hours * 60 + mins
}

interface TransmissionPreferencesFormProps {
  instanceId: number
  section?: "torrents" | "speed" | "peers" | "network"
  onSuccess?: () => void
}

export function TransmissionPreferencesForm({ instanceId, section = "torrents", onSuccess }: TransmissionPreferencesFormProps) {
  const { t } = useTranslation("instances")
  const queryClient = useQueryClient()
  // Each section is force-mounted by the preferences dialog so unsaved edits
  // survive tab switches. Keep labels and controls unique across those mounts.
  const fieldId = (name: string) => `tr-${section}-${name}`
  const [draft, setDraft] = useState<TransmissionPreferences | null>(null)
  const [dirty, setDirty] = useState<ReadonlySet<string>>(new Set())

  const { data: preferences, isLoading, error } = useQuery({
    queryKey: ["transmission-preferences", instanceId],
    queryFn: () => api.getTransmissionPreferences(instanceId),
    staleTime: 30000,
  })

  // Sync the draft from the server while the user has not edited anything.
  useEffect(() => {
    if (preferences && dirty.size === 0) {
      setDraft(preferences)
    }
  }, [preferences, dirty.size])

  const setField = <K extends keyof TransmissionPreferences>(key: K, value: TransmissionPreferences[K]) => {
    setDraft(prev => (prev ? { ...prev, [key]: value } : prev))
    setDirty(prev => new Set(prev).add(key))
  }

  const saveMutation = useMutation({
    mutationFn: (settings: Partial<TransmissionPreferences>) =>
      api.updateTransmissionPreferences(instanceId, settings),
    onSuccess: updated => {
      queryClient.setQueryData(["transmission-preferences", instanceId], updated)
      setDraft(updated)
      setDirty(new Set())
      toast.success(t("preferences.transmission.toast.success"))
      onSuccess?.()
    },
    onError: err => {
      toast.error(t("preferences.transmission.toast.error"), {
        description: formatErrorMessage(err instanceof Error ? err.message : String(err)),
      })
    },
  })

  const blocklistMutation = useMutation({
    mutationFn: () => api.updateTransmissionBlocklist(instanceId),
    onSuccess: result => {
      queryClient.setQueryData<TransmissionPreferences | undefined>(
        ["transmission-preferences", instanceId],
        prev => (prev ? { ...prev, "blocklist-size": result["blocklist-size"] } : prev)
      )
      toast.success(t("preferences.transmission.peers.blocklistUpdated", { count: result["blocklist-size"] }))
    },
    onError: err => {
      toast.error(t("preferences.transmission.toast.error"), {
        description: formatErrorMessage(err instanceof Error ? err.message : String(err)),
      })
    },
  })

  const handleSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    if (!draft || dirty.size === 0) return
    const patch: Partial<TransmissionPreferences> = {}
    dirty.forEach(key => {
      (patch as Record<string, unknown>)[key] = draft[key as keyof TransmissionPreferences]
    })
    saveMutation.mutate(patch)
  }

  if (isLoading && !draft) {
    return (
      <div className="flex items-center justify-center py-8" role="status" aria-live="polite">
        <p className="text-sm text-muted-foreground">{t("preferences.transmission.loading")}</p>
      </div>
    )
  }

  if (error && !draft) {
    return (
      <div className="flex items-center justify-center py-8" role="alert">
        <p className="text-sm text-muted-foreground">{t("preferences.transmission.loadFailed")}</p>
      </div>
    )
  }

  if (!draft) {
    return null
  }

  const scheduleDays = [DAY_EVERYDAY, DAY_WEEKDAYS, DAY_WEEKENDS].includes(draft["alt-speed-time-day"] ?? DAY_EVERYDAY)
    ? draft["alt-speed-time-day"] ?? DAY_EVERYDAY
    : DAY_EVERYDAY

  return (
    <PreferencesFormShell
      onSubmit={handleSubmit}
      footer={(
        <Button
          type="submit"
          disabled={dirty.size === 0 || saveMutation.isPending}
          className="min-w-32"
        >
          {saveMutation.isPending ? t("preferences.common.saving") : t("preferences.common.saveChanges")}
        </Button>
      )}
    >
      <div className="space-y-8">
        {section === "torrents" && <section className="space-y-4">
          <h4 className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            <HardDrive className="h-4 w-4" aria-hidden="true" />
            {t("preferences.transmission.torrents.title")}
          </h4>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div className="space-y-2">
              <Label htmlFor={fieldId("download-dir")} className="text-sm font-medium">
                {t("preferences.transmission.torrents.downloadDir")}
              </Label>
              <Input
                id={fieldId("download-dir")}
                value={draft["download-dir"] ?? ""}
                onChange={e => setField("download-dir", e.target.value)}
                className="font-mono"
              />
            </div>
            <div className="space-y-2">
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("incomplete-enabled")}
                  checked={draft["incomplete-dir-enabled"] ?? false}
                  onCheckedChange={checked => setField("incomplete-dir-enabled", checked)}
                />
                <Label htmlFor={fieldId("incomplete-enabled")} className="text-sm font-medium">
                  {t("preferences.transmission.torrents.tempFolder")}
                </Label>
              </div>
              {(draft["incomplete-dir-enabled"] ?? false) && (
                <Input
                  aria-label={t("preferences.transmission.torrents.tempFolderPath")}
                  value={draft["incomplete-dir"] ?? ""}
                  onChange={e => setField("incomplete-dir", e.target.value)}
                  className="font-mono"
                />
              )}
            </div>
            <div className="flex items-center gap-3">
              <Switch
                id={fieldId("start-added-torrents")}
                checked={draft["start-added-torrents"] ?? false}
                onCheckedChange={checked => setField("start-added-torrents", checked)}
              />
              <Label htmlFor={fieldId("start-added-torrents")} className="text-sm font-medium">
                {t("preferences.transmission.torrents.startAdded")}
              </Label>
            </div>
            <div className="flex items-center gap-3">
              <Switch
                id={fieldId("rename-partial-files")}
                checked={draft["rename-partial-files"] ?? false}
                onCheckedChange={checked => setField("rename-partial-files", checked)}
              />
              <Label htmlFor={fieldId("rename-partial-files")} className="text-sm font-medium">
                {t("preferences.transmission.torrents.appendPart")}
              </Label>
            </div>
            <div className="space-y-2">
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("dl-queue-enabled")}
                  checked={draft["download-queue-enabled"] ?? false}
                  onCheckedChange={checked => setField("download-queue-enabled", checked)}
                />
                <Label htmlFor={fieldId("dl-queue-enabled")} className="text-sm font-medium">
                  {t("preferences.transmission.torrents.downloadQueue")}
                </Label>
              </div>
              {(draft["download-queue-enabled"] ?? false) && (
                <Input
                  type="number"
                  min="0"
                  aria-label={t("preferences.transmission.torrents.downloadQueueSize")}
                  value={draft["download-queue-size"] ?? 0}
                  onChange={e => setField("download-queue-size", parseInt(e.target.value, 10) || 0)}
                  className="w-32"
                />
              )}
            </div>
            <div className="space-y-2">
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("seed-ratio-limited")}
                  checked={draft["seedRatioLimited"] ?? false}
                  onCheckedChange={checked => setField("seedRatioLimited", checked)}
                />
                <Label htmlFor={fieldId("seed-ratio-limited")} className="text-sm font-medium">
                  {t("preferences.transmission.torrents.stopRatio")}
                </Label>
              </div>
              {(draft["seedRatioLimited"] ?? false) && (
                <Input
                  type="number"
                  min="0"
                  step="0.05"
                  aria-label={t("preferences.transmission.torrents.stopRatioValue")}
                  value={draft["seedRatioLimit"] ?? 0}
                  onChange={e => setField("seedRatioLimit", parseFloat(e.target.value) || 0)}
                  className="w-32"
                />
              )}
            </div>
            <div className="space-y-2">
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("idle-limited")}
                  checked={draft["idle-seeding-limit-enabled"] ?? false}
                  onCheckedChange={checked => setField("idle-seeding-limit-enabled", checked)}
                />
                <Label htmlFor={fieldId("idle-limited")} className="text-sm font-medium">
                  {t("preferences.transmission.torrents.stopIdle")}
                </Label>
              </div>
              {(draft["idle-seeding-limit-enabled"] ?? false) && (
                <div className="flex items-center gap-2">
                  <Input
                    type="number"
                    min="0"
                    aria-label={t("preferences.transmission.torrents.stopIdleValue")}
                    value={draft["idle-seeding-limit"] ?? 0}
                    onChange={e => setField("idle-seeding-limit", parseInt(e.target.value, 10) || 0)}
                    className="w-32"
                  />
                  <span className="text-sm text-muted-foreground">{t("preferences.transmission.torrents.minutes")}</span>
                </div>
              )}
            </div>
          </div>
        </section>}

        {/* Speed */}
        {section === "speed" && <section className="space-y-4 border-t border-border pt-6">
          <h4 className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            <Gauge className="h-4 w-4" aria-hidden="true" />
            {t("preferences.transmission.speed.title")}
          </h4>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div className="space-y-2">
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("speed-up-enabled")}
                  checked={draft["speed-limit-up-enabled"] ?? false}
                  onCheckedChange={checked => setField("speed-limit-up-enabled", checked)}
                />
                <Label htmlFor={fieldId("speed-up-enabled")} className="text-sm font-medium">
                  {t("preferences.transmission.speed.uploadLimit")}
                </Label>
              </div>
              {(draft["speed-limit-up-enabled"] ?? false) && (
                <div className="flex items-center gap-2">
                  <Input
                    type="number"
                    min="0"
                    aria-label={t("preferences.transmission.speed.uploadLimitValue")}
                    value={draft["speed-limit-up"] ?? 0}
                    onChange={e => setField("speed-limit-up", parseInt(e.target.value, 10) || 0)}
                    className="w-32"
                  />
                  <span className="text-sm text-muted-foreground">{t("preferences.transmission.speed.kbps")}</span>
                </div>
              )}
            </div>
            <div className="space-y-2">
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("speed-down-enabled")}
                  checked={draft["speed-limit-down-enabled"] ?? false}
                  onCheckedChange={checked => setField("speed-limit-down-enabled", checked)}
                />
                <Label htmlFor={fieldId("speed-down-enabled")} className="text-sm font-medium">
                  {t("preferences.transmission.speed.downloadLimit")}
                </Label>
              </div>
              {(draft["speed-limit-down-enabled"] ?? false) && (
                <div className="flex items-center gap-2">
                  <Input
                    type="number"
                    min="0"
                    aria-label={t("preferences.transmission.speed.downloadLimitValue")}
                    value={draft["speed-limit-down"] ?? 0}
                    onChange={e => setField("speed-limit-down", parseInt(e.target.value, 10) || 0)}
                    className="w-32"
                  />
                  <span className="text-sm text-muted-foreground">{t("preferences.transmission.speed.kbps")}</span>
                </div>
              )}
            </div>
          </div>

          <div className="space-y-4 border-t border-border pt-4">
            <div className="flex items-center gap-3">
              <Switch
                id={fieldId("alt-enabled")}
                checked={draft["alt-speed-enabled"] ?? false}
                onCheckedChange={checked => setField("alt-speed-enabled", checked)}
              />
              <Label htmlFor={fieldId("alt-enabled")} className="text-sm font-medium">
                {t("preferences.transmission.speed.altLimits")}
              </Label>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
              <div className="flex items-center gap-2">
                <Input
                  type="number"
                  min="0"
                  aria-label={t("preferences.transmission.speed.altUpload")}
                  value={draft["alt-speed-up"] ?? 0}
                  onChange={e => setField("alt-speed-up", parseInt(e.target.value, 10) || 0)}
                  className="w-32"
                />
                <span className="text-sm text-muted-foreground">{t("preferences.transmission.speed.altUploadUnit")}</span>
              </div>
              <div className="flex items-center gap-2">
                <Input
                  type="number"
                  min="0"
                  aria-label={t("preferences.transmission.speed.altDownload")}
                  value={draft["alt-speed-down"] ?? 0}
                  onChange={e => setField("alt-speed-down", parseInt(e.target.value, 10) || 0)}
                  className="w-32"
                />
                <span className="text-sm text-muted-foreground">{t("preferences.transmission.speed.altDownloadUnit")}</span>
              </div>
            </div>

            <div className="space-y-4">
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("alt-time-enabled")}
                  checked={draft["alt-speed-time-enabled"] ?? false}
                  onCheckedChange={checked => setField("alt-speed-time-enabled", checked)}
                />
                <Label htmlFor={fieldId("alt-time-enabled")} className="text-sm font-medium">
                  {t("preferences.transmission.speed.schedule")}
                </Label>
              </div>
              {(draft["alt-speed-time-enabled"] ?? false) && (
                <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
                  <div className="space-y-2">
                    <Label htmlFor={fieldId("alt-from")} className="text-sm font-medium">
                      {t("preferences.transmission.speed.from")}
                    </Label>
                    <Input
                      id={fieldId("alt-from")}
                      type="time"
                      value={minutesToTimeInput(draft["alt-speed-time-begin"])}
                      onChange={e => setField("alt-speed-time-begin", timeInputToMinutes(e.target.value))}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor={fieldId("alt-to")} className="text-sm font-medium">
                      {t("preferences.transmission.speed.to")}
                    </Label>
                    <Input
                      id={fieldId("alt-to")}
                      type="time"
                      value={minutesToTimeInput(draft["alt-speed-time-end"])}
                      onChange={e => setField("alt-speed-time-end", timeInputToMinutes(e.target.value))}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor={fieldId("alt-days")} className="text-sm font-medium">
                      {t("preferences.transmission.speed.onDays")}
                    </Label>
                    <Select
                      value={scheduleDays.toString()}
                      onValueChange={value => setField("alt-speed-time-day", parseInt(value, 10))}
                    >
                      <SelectTrigger id={fieldId("alt-days")} className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={DAY_EVERYDAY.toString()}>
                          {t("preferences.transmission.speed.everyday")}
                        </SelectItem>
                        <SelectItem value={DAY_WEEKDAYS.toString()}>
                          {t("preferences.transmission.speed.weekdays")}
                        </SelectItem>
                        <SelectItem value={DAY_WEEKENDS.toString()}>
                          {t("preferences.transmission.speed.weekends")}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>
              )}
            </div>
          </div>
        </section>}

        {/* Peers */}
        {section === "peers" && <section className="space-y-4 border-t border-border pt-6">
          <h4 className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            <Users className="h-4 w-4" aria-hidden="true" />
            {t("preferences.transmission.peers.title")}
          </h4>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div className="space-y-2">
              <Label htmlFor={fieldId("peer-limit-torrent")} className="text-sm font-medium">
                {t("preferences.transmission.peers.maxPeersPerTorrent")}
              </Label>
              <Input
                id={fieldId("peer-limit-torrent")}
                type="number"
                min="0"
                value={draft["peer-limit-per-torrent"] ?? 0}
                onChange={e => setField("peer-limit-per-torrent", parseInt(e.target.value, 10) || 0)}
                className="w-32"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor={fieldId("peer-limit-global")} className="text-sm font-medium">
                {t("preferences.transmission.peers.maxPeersOverall")}
              </Label>
              <Input
                id={fieldId("peer-limit-global")}
                type="number"
                min="0"
                value={draft["peer-limit-global"] ?? 0}
                onChange={e => setField("peer-limit-global", parseInt(e.target.value, 10) || 0)}
                className="w-32"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor={fieldId("encryption")} className="text-sm font-medium">
                {t("preferences.transmission.peers.encryption")}
              </Label>
              <Select
                value={draft.encryption ?? ENCRYPTION_ALLOW}
                onValueChange={value => setField("encryption", value as TransmissionPreferences["encryption"])}
              >
                <SelectTrigger id={fieldId("encryption")} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ENCRYPTION_PREFER}>
                    {t("preferences.transmission.peers.encryptionPrefer")}
                  </SelectItem>
                  <SelectItem value={ENCRYPTION_ALLOW}>
                    {t("preferences.transmission.peers.encryptionAllow")}
                  </SelectItem>
                  <SelectItem value={ENCRYPTION_REQUIRE}>
                    {t("preferences.transmission.peers.encryptionRequire")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-3">
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("pex")}
                  checked={draft["pex-enabled"] ?? false}
                  onCheckedChange={checked => setField("pex-enabled", checked)}
                />
                <Label htmlFor={fieldId("pex")} className="text-sm font-medium">{t("preferences.transmission.network.pex")}</Label>
              </div>
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("dht")}
                  checked={draft["dht-enabled"] ?? false}
                  onCheckedChange={checked => setField("dht-enabled", checked)}
                />
                <Label htmlFor={fieldId("dht")} className="text-sm font-medium">{t("preferences.transmission.network.dht")}</Label>
              </div>
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("lpd")}
                  checked={draft["lpd-enabled"] ?? false}
                  onCheckedChange={checked => setField("lpd-enabled", checked)}
                />
                <Label htmlFor={fieldId("lpd")} className="text-sm font-medium">{t("preferences.transmission.network.lpd")}</Label>
              </div>
            </div>
          </div>

          <div className="space-y-4 border-t border-border pt-4">
            <div className="flex items-center gap-3">
              <Switch
                id={fieldId("blocklist-enabled")}
                checked={draft["blocklist-enabled"] ?? false}
                onCheckedChange={checked => setField("blocklist-enabled", checked)}
              />
              <Label htmlFor={fieldId("blocklist-enabled")} className="text-sm font-medium">
                {t("preferences.transmission.peers.blocklistEnable")}
              </Label>
            </div>
            {(draft["blocklist-enabled"] ?? false) && (
              <div className="space-y-3">
                <div className="space-y-2">
                  <Label htmlFor={fieldId("blocklist-url")} className="text-sm font-medium">
                    {t("preferences.transmission.peers.blocklistUrl")}
                  </Label>
                  <Input
                    id={fieldId("blocklist-url")}
                    type="url"
                    value={draft["blocklist-url"] ?? ""}
                    onChange={e => setField("blocklist-url", e.target.value)}
                    className="font-mono"
                  />
                </div>
                <div className="flex items-center gap-3">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => blocklistMutation.mutate()}
                    disabled={blocklistMutation.isPending}
                  >
                    <RefreshCw className={`mr-2 h-4 w-4 ${blocklistMutation.isPending ? "animate-spin" : ""}`} aria-hidden="true" />
                    {t("preferences.transmission.peers.blocklistUpdate")}
                  </Button>
                  <span className="text-sm text-muted-foreground">
                    {t("preferences.transmission.peers.blocklistSize", { count: draft["blocklist-size"] ?? 0 })}
                  </span>
                </div>
              </div>
            )}
          </div>
        </section>}

        {/* Network */}
        {section === "network" && <section className="space-y-4 border-t border-border pt-6">
          <h4 className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            <Globe className="h-4 w-4" aria-hidden="true" />
            {t("preferences.transmission.network.title")}
          </h4>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div className="space-y-2">
              <Label htmlFor={fieldId("peer-port")} className="text-sm font-medium">
                {t("preferences.transmission.network.peerPort")}
              </Label>
              <Input
                id={fieldId("peer-port")}
                type="number"
                min="1"
                max="65535"
                value={draft["peer-port"] ?? 0}
                onChange={e => setField("peer-port", parseInt(e.target.value, 10) || 0)}
                className="w-32"
                disabled={draft["peer-port-random-on-start"] ?? false}
              />
            </div>
            <div className="space-y-3">
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("port-random")}
                  checked={draft["peer-port-random-on-start"] ?? false}
                  onCheckedChange={checked => setField("peer-port-random-on-start", checked)}
                />
                <Label htmlFor={fieldId("port-random")} className="text-sm font-medium">
                  {t("preferences.transmission.network.randomizePort")}
                </Label>
              </div>
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("port-forwarding")}
                  checked={draft["port-forwarding-enabled"] ?? false}
                  onCheckedChange={checked => setField("port-forwarding-enabled", checked)}
                />
                <Label htmlFor={fieldId("port-forwarding")} className="text-sm font-medium">
                  {t("preferences.transmission.network.portForwarding")}
                </Label>
              </div>
              <div className="flex items-center gap-3">
                <Switch
                  id={fieldId("utp")}
                  checked={draft["utp-enabled"] ?? false}
                  onCheckedChange={checked => setField("utp-enabled", checked)}
                />
                <Label htmlFor={fieldId("utp")} className="text-sm font-medium">
                  {t("preferences.transmission.network.utp")}
                </Label>
              </div>
            </div>
          </div>
          {draft["default-trackers"] !== undefined && (
            <div className="space-y-2">
              <Label htmlFor={fieldId("default-trackers")} className="flex items-center gap-2 text-sm font-medium">
                {t("preferences.transmission.network.defaultTrackers")}
                <FieldHelp>{t("preferences.transmission.network.defaultTrackersHelp")}</FieldHelp>
              </Label>
              <textarea
                id={fieldId("default-trackers")}
                value={draft["default-trackers"] ?? ""}
                onChange={e => setField("default-trackers", e.target.value)}
                rows={4}
                className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm font-mono shadow-xs placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              />
            </div>
          )}
        </section>}
      </div>
    </PreferencesFormShell>
  )
}
