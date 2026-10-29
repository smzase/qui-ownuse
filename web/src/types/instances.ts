/*
 * Copyright (c) 2025-2026, s0up and the autobrr contributors.
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

export type InstanceClientType = "qbittorrent" | "transmission"
// How qui reaches an instance's torrent data. Remote is SSH with a confirmed host key.
export type FilesystemMode = "none" | "local" | "remote"

export interface Instance {
  id: number
  clientType: InstanceClientType
  name: string
  host: string
  username: string
  hasApiKey?: boolean
  basicUsername?: string
  tlsSkipVerify: boolean
  hasLocalFilesystemAccess: boolean
  capabilities: FilesystemCapabilities
  filesystemMode: FilesystemMode
  // Hardlink mode settings (per-instance)
  useHardlinks: boolean
  hardlinkBaseDir: string
  hardlinkDirPreset: "flat" | "by-tracker" | "by-instance"
  // Reflink mode (copy-on-write) - mutually exclusive with hardlink mode
  useReflinks: boolean
  // Fallback to regular mode when reflink/hardlink fails
  fallbackToRegularMode: boolean
  sortOrder: number
  isActive: boolean
  reannounceSettings: InstanceReannounceSettings
}

// What qui can do with an instance's files, from models.FilesystemCapabilitiesOf.
export interface FilesystemCapabilities {
  read: boolean
  identity: boolean
  trustedIdentity: boolean
  write: boolean
  content: boolean
}

export interface InstanceFormData {
  clientType: InstanceClientType
  name: string
  host: string
  username?: string
  password?: string
  apiKey?: string
  basicUsername?: string
  basicPassword?: string
  tlsSkipVerify: boolean
  hasLocalFilesystemAccess: boolean
  // Hardlink mode settings (per-instance)
  useHardlinks?: boolean
  hardlinkBaseDir?: string
  hardlinkDirPreset?: "flat" | "by-tracker" | "by-instance"
  // Reflink mode (copy-on-write) - mutually exclusive with hardlink mode
  useReflinks?: boolean
  // Fallback to regular mode when reflink/hardlink fails
  fallbackToRegularMode?: boolean
  reannounceSettings: InstanceReannounceSettings
}

export interface InstanceReannounceSettings {
  enabled: boolean
  initialWaitSeconds: number
  reannounceIntervalSeconds: number
  maxAgeSeconds: number
  maxRetries: number
  aggressive: boolean
  monitorAll: boolean
  excludeCategories: boolean
  categories: string[]
  excludeTags: boolean
  tags: string[]
  excludeTrackers: boolean
  trackers: string[]
}

// Reannounce settings constraints - shared across components
export const REANNOUNCE_CONSTRAINTS = {
  MIN_INITIAL_WAIT: 5,
  MIN_INTERVAL: 5,
  MIN_MAX_AGE: 60,
  MIN_MAX_RETRIES: 1,
  MAX_MAX_RETRIES: 50,
  DEFAULT_MAX_RETRIES: 50,
} as const

export interface InstanceCrossSeedCompletionSettings {
  instanceId: number
  enabled: boolean
  categories: string[]
  tags: string[]
  excludeCategories: string[]
  excludeTags: string[]
  indexerIds: number[]
  bypassTorznabCache: boolean
  delaySeconds: number
}

/**
 * A torrent match found by the backend using proper release metadata parsing (rls library).
 */
export interface LocalCrossSeedMatch {
  instanceId: number
  instanceName: string
  hash: string
  name: string
  size: number
  progress: number
  savePath: string
  contentPath: string
  category: string
  tags: string
  state: string
  tracker: string
  trackerHealth?: string
  matchType: "content_path" | "hardlink" | "reflink" | "name" | "release"
}

/** An instance with local filesystem access that the delete check did not read. */
export interface UncheckedInstance {
  instanceId: number
  instanceName: string
  /** offline: qui cannot read its torrents. disabled: it is turned off in qui. */
  reason: "offline" | "disabled"
}

export interface InstanceReannounceActivity {
  instanceId: number
  hash: string
  torrentName?: string
  trackers?: string
  outcome: "skipped" | "failed" | "succeeded" | "started"
  reason?: string
  timestamp: string
}

export interface InstanceReannounceCandidate {
  instanceId: number
  hash: string
  torrentName?: string
  trackers?: string
  timeActiveSeconds?: number
  category?: string
  tags?: string
  state: "watching" | "reannouncing" | "cooldown"
  hasTrackerProblem: boolean
  waitingForInitial: boolean
}

export interface InstanceError {
  id: number
  instanceId: number
  errorType: string
  errorMessage: string
  occurredAt: string
}

export interface InstanceResponse extends Instance {
  ownerId: number
  shared: boolean
  connected: boolean
  hasDecryptionError: boolean
  recentErrors?: InstanceError[]
  connectionStatus?: string
}

export interface InstanceCapabilities {
  supportsTorrentCreation: boolean
  supportsTorrentExport: boolean
  supportsSetTags: boolean
  supportsSetComment: boolean
  supportsTrackerHealth: boolean
  supportsTrackerEditing: boolean
  supportsRenameTorrent: boolean
  supportsRenameFile: boolean
  supportsRenameFolder: boolean
  supportsFilePriority: boolean
  supportsSubcategories: boolean
  subcategoriesAlwaysEnabled: boolean
  supportsTorrentTmpPath: boolean
  supportsPathAutocomplete: boolean
  supportsTestEmail: boolean
  supportsFreeSpacePathSource: boolean
  supportsSetRSSFeedURL: boolean
  supportsShareLimitsAction: boolean
  supportsShareLimitsMode?: boolean
  webAPIVersion?: string
}

/**
 * Transmission daemon session settings exposed through qui's
 * Transmission-only preferences surface. Keys use the daemon's own
 * spelling so values pass through unmodified. Fields the daemon does
 * not report (version-specific options) are absent.
 */
export interface TransmissionPreferences {
  // Torrents
  "download-dir"?: string
  "incomplete-dir"?: string
  "incomplete-dir-enabled"?: boolean
  "start-added-torrents"?: boolean
  "rename-partial-files"?: boolean
  "download-queue-enabled"?: boolean
  "download-queue-size"?: number
  "seed-queue-enabled"?: boolean
  "seed-queue-size"?: number
  "seedRatioLimit"?: number
  "seedRatioLimited"?: boolean
  "idle-seeding-limit"?: number
  "idle-seeding-limit-enabled"?: boolean
  // Speed
  "speed-limit-up"?: number
  "speed-limit-up-enabled"?: boolean
  "speed-limit-down"?: number
  "speed-limit-down-enabled"?: boolean
  "alt-speed-enabled"?: boolean
  "alt-speed-up"?: number
  "alt-speed-down"?: number
  "alt-speed-time-enabled"?: boolean
  "alt-speed-time-begin"?: number
  "alt-speed-time-end"?: number
  "alt-speed-time-day"?: number
  // Peers
  "peer-limit-per-torrent"?: number
  "peer-limit-global"?: number
  encryption?: "required" | "preferred" | "tolerated"
  "pex-enabled"?: boolean
  "dht-enabled"?: boolean
  "lpd-enabled"?: boolean
  "blocklist-enabled"?: boolean
  "blocklist-url"?: string
  "blocklist-size"?: number
  // Network
  "peer-port"?: number
  "peer-port-random-on-start"?: boolean
  "port-forwarding-enabled"?: boolean
  "utp-enabled"?: boolean
  "default-trackers"?: string
}
