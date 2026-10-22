/*
 * Copyright (c) 2025-2026, s0up and the autobrr contributors.
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

import type { InstanceMetadata } from "@/hooks/useInstanceMetadata"

const CACHE_PREFIX = "qui.instance-meta-cache."
const CACHE_TTL_MS = 10 * 60 * 1000 // 10 minutes

interface CacheEntry {
  data: InstanceMetadata
  timestamp: number
}

/**
 * Read cached instance metadata from localStorage.
 * Returns undefined if cache is missing, expired, or corrupted.
 */
export function getCachedInstanceMetadata(instanceId: number): InstanceMetadata | undefined {
  try {
    const raw = localStorage.getItem(CACHE_PREFIX + instanceId)
    if (!raw) return undefined
    const entry = JSON.parse(raw) as CacheEntry
    if (Date.now() - entry.timestamp > CACHE_TTL_MS) return undefined
    return entry.data
  } catch {
    return undefined
  }
}

/**
 * Save instance metadata to localStorage for use as a fallback
 * when an instance is slow or unavailable.
 */
export function setCachedInstanceMetadata(instanceId: number, data: InstanceMetadata): void {
  try {
    const entry: CacheEntry = { data, timestamp: Date.now() }
    localStorage.setItem(CACHE_PREFIX + instanceId, JSON.stringify(entry))
  } catch {
    // ignore quota / serialization errors
  }
}
