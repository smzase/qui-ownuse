import { describe, expect, it, vi } from "vitest"

vi.mock("@/lib/api", () => {
  throw new Error("Free themes must not request a license")
})

import { useHasPremiumAccess, usePremiumAccess } from "./useLicense"

describe("free themes in the fork", () => {
  it("grants access without a license request", () => {
    expect(useHasPremiumAccess().hasPremiumAccess).toBe(true)
    expect(usePremiumAccess().data.hasPremiumAccess).toBe(true)
  })
})
