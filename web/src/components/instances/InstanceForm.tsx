/*
 * Copyright (c) 2025-2026, s0up and the autobrr contributors.
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

import { Button } from "@/components/ui/button"
import { FieldHelp } from "@/components/ui/field-help"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { useInstances } from "@/hooks/useInstances"
import { DEFAULT_REANNOUNCE_SETTINGS, instanceUrlSchema } from "@/lib/instance-validation"
import { formatErrorMessage } from "@/lib/format-error-message"
import type { Instance, InstanceClientType, InstanceFormData } from "@/types"
import { useForm } from "@tanstack/react-form"
import { useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

type InstanceAuthType = "none" | "usernamePassword" | "apiKey"

const DEFAULT_QBITTORRENT_HOST = "http://localhost:8080"
const DEFAULT_TRANSMISSION_HOST = "http://localhost:9091"

interface InstanceFormProps {
  instance?: Instance
  onSuccess: () => void
  onCancel: () => void
  /** When provided, renders without internal buttons (for external DialogFooter) */
  formId?: string
}

function getInstanceAuthType(instance?: Instance): InstanceAuthType {
  if (instance?.clientType !== "transmission" && instance?.hasApiKey) {
    return "apiKey"
  }
  return instance?.username ? "usernamePassword" : "none"
}

function defaultHostForClientType(clientType: InstanceClientType): string {
  return clientType === "transmission" ? DEFAULT_TRANSMISSION_HOST : DEFAULT_QBITTORRENT_HOST
}

function getInstanceFormDefaults(instance?: Instance): InstanceFormData {
  const clientType: InstanceClientType = instance?.clientType ?? "qbittorrent"
  return {
    clientType,
    name: instance?.name ?? "",
    host: instance?.host ?? defaultHostForClientType(clientType),
    username: instance?.username ?? "",
    password: "",
    apiKey: clientType !== "transmission" && instance?.hasApiKey ? "<redacted>" : "",
    basicUsername: clientType !== "transmission" ? (instance?.basicUsername ?? "") : "",
    basicPassword: clientType !== "transmission" && instance?.basicUsername ? "<redacted>" : "",
    tlsSkipVerify: instance?.tlsSkipVerify ?? false,
    hasLocalFilesystemAccess: instance?.hasLocalFilesystemAccess ?? false,
    reannounceSettings: instance?.reannounceSettings ?? DEFAULT_REANNOUNCE_SETTINGS,
  }
}

function getAuthValidationError(data: InstanceFormData, authType: InstanceAuthType, instance?: Instance) {
  if (authType === "usernamePassword") {
    if (!data.username?.trim()) {
      return "Username is required for username/password authentication"
    }

    if (!data.password?.trim() && !instance?.username) {
      return "Password is required for username/password authentication"
    }
  }

  if (authType === "apiKey") {
    const hasPreservedAPIKey = instance?.hasApiKey && data.apiKey === "<redacted>"
    if (!hasPreservedAPIKey && !data.apiKey?.trim()) {
      return "API key is required for API key authentication"
    }
  }

  return undefined
}

export function InstanceForm({ instance, onSuccess, onCancel, formId }: InstanceFormProps) {
  const { t } = useTranslation("instances")
  const { createInstance, updateInstance, isCreating, isUpdating } = useInstances()
  const [clientType, setClientType] = useState<InstanceClientType>(() => instance?.clientType ?? "qbittorrent")

  // Transmission authenticates with the daemon's own RPC credentials; the
  // API-key option and the reverse-proxy basic auth layer do not apply.
  const isTransmission = clientType === "transmission"
  const [showBasicAuth, setShowBasicAuth] = useState(!isTransmission && !!instance?.basicUsername)
  const [authType, setAuthType] = useState<InstanceAuthType>(() => getInstanceAuthType(instance))

  const switchClientType = (next: InstanceClientType) => {
    if (instance || next === clientType) {
      return
    }
    setClientType(next)
    // API key auth is a qBittorrent-only feature.
    if (next === "transmission" && authType === "apiKey") {
      setAuthType("none")
    }
    if (next === "transmission") {
      setShowBasicAuth(false)
    }
    // Follow the default port when the user has not typed a custom host yet.
    const currentHost = form.getFieldValue("host")
    if (currentHost === DEFAULT_QBITTORRENT_HOST || currentHost === DEFAULT_TRANSMISSION_HOST) {
      form.setFieldValue("host", defaultHostForClientType(next))
    }
  }

  const handleSubmit = (data: InstanceFormData) => {
    const effectiveAuthType: InstanceAuthType = isTransmission && authType === "apiKey" ? "none" : authType
    const authValidationError = getAuthValidationError(data, effectiveAuthType, instance)
    if (authValidationError) {
      toast.error(t("form.toast.missingCredentialsTitle"), {
        description: authValidationError,
      })
      return
    }

    let submitData: InstanceFormData

    if (showBasicAuth && !isTransmission) {
      // If basic auth is enabled, only include basicPassword if it's not the redacted placeholder
      if (data.basicPassword === "<redacted>") {
        // Don't send basicPassword at all - this preserves existing password
        const { basicPassword, ...dataWithoutPassword } = data
        submitData = dataWithoutPassword
      } else {
        // Send the actual password (could be empty to clear, or new password)
        submitData = data
      }
    } else {
      // Basic auth disabled - clear basic auth credentials
      submitData = {
        ...data,
        basicUsername: "",
        basicPassword: "",
      }
    }

    if (effectiveAuthType === "none") {
      submitData = {
        ...submitData,
        username: "",
        password: "",
        apiKey: "",
      }
    }

    if (effectiveAuthType === "usernamePassword") {
      submitData = {
        ...submitData,
        apiKey: "",
      }
      if (submitData.password === "") {
        const { password, ...rest } = submitData
        submitData = rest
      }
    }

    if (effectiveAuthType === "apiKey") {
      submitData = {
        ...submitData,
        username: "",
        password: "",
      }
      if (submitData.apiKey === "" || submitData.apiKey === "<redacted>") {
        const { apiKey, ...rest } = submitData
        submitData = rest
      }
    }

    submitData = {
      ...submitData,
      clientType,
    }

    if (instance) {
      updateInstance({ id: instance.id, data: submitData }, {
        onSuccess: () => {
          toast.success(t("form.toast.instanceUpdatedTitle"), {
            description: t("form.toast.instanceUpdatedDescription"),
          })
          onSuccess()
        },
        onError: (error) => {
          toast.error(t("form.toast.updateFailedTitle"), {
            description: error instanceof Error ? formatErrorMessage(error.message) : t("form.toast.updateFailedDescription"),
          })
        },
      })
    } else {
      createInstance(submitData, {
        onSuccess: () => {
          toast.success(t("form.toast.instanceCreatedTitle"), {
            description: t("form.toast.instanceCreatedDescription"),
          })
          onSuccess()
        },
        onError: (error) => {
          toast.error(t("form.toast.createFailedTitle"), {
            description: error instanceof Error ? formatErrorMessage(error.message) : t("form.toast.createFailedDescription"),
          })
        },
      })
    }
  }

  const form = useForm({
    defaultValues: getInstanceFormDefaults(instance),
    onSubmit: ({ value }) => {
      handleSubmit(value)
    },
  })

  const prevInstanceId = useRef(instance?.id)
  useEffect(() => {
    if (prevInstanceId.current !== instance?.id) {
      prevInstanceId.current = instance?.id
      form.reset(getInstanceFormDefaults(instance))
      setShowBasicAuth(instance?.clientType !== "transmission" && !!instance?.basicUsername)
      setAuthType(getInstanceAuthType(instance))
      setClientType(instance?.clientType ?? "qbittorrent")
    }
  }, [instance, form])

  return (
    <>
      <form
        id={formId}
        onSubmit={(e) => {
          e.preventDefault()
          form.handleSubmit()
        }}
        className="space-y-4"
      >
        <div className="space-y-2">
          <Label htmlFor="client-type" className="flex items-center gap-2">
            {t("form.labels.clientType")}
            <FieldHelp>{t("form.labels.clientTypeDescription")}</FieldHelp>
          </Label>
          <div className="grid grid-cols-2 gap-2" id="client-type">
            <Button
              type="button"
              variant={clientType === "qbittorrent" ? "default" : "outline"}
              disabled={!!instance}
              onClick={() => switchClientType("qbittorrent")}
            >
              {t("form.clientType.qbittorrent")}
            </Button>
            <Button
              type="button"
              variant={clientType === "transmission" ? "default" : "outline"}
              disabled={!!instance}
              onClick={() => switchClientType("transmission")}
            >
              {t("form.clientType.transmission")}
            </Button>
          </div>
        </div>

        <form.Field
          name="name"
          validators={{
            onChange: ({ value }) =>
              !value ? t("form.validation.nameRequired") : undefined,
          }}
        >
          {(field) => (
            <div className="space-y-2">
              <Label htmlFor={field.name}>{t("form.labels.instanceName")}</Label>
              <Input
                id={field.name}
                value={field.state.value}
                onBlur={field.handleBlur}
                onChange={(e) => field.handleChange(e.target.value)}
                placeholder={t("form.placeholders.instanceName")}
                data-1p-ignore
                autoComplete="off"
              />
              {field.state.meta.isTouched && field.state.meta.errors[0] && (
                <p className="text-sm text-destructive">{field.state.meta.errors[0]}</p>
              )}
            </div>
          )}
        </form.Field>

        <form.Field
          name="host"
          validators={{
            onChange: ({ value }) => {
              const result = instanceUrlSchema.safeParse(value)
              return result.success ? undefined : result.error.issues[0]?.message
            },
          }}
        >
          {(field) => (
            <div className="space-y-2">
              <Label htmlFor={field.name}>{t("form.labels.url")}</Label>
              <Input
                id={field.name}
                value={field.state.value}
                onBlur={() => {
                  field.handleBlur()
                  const parsed = instanceUrlSchema.safeParse(field.state.value)
                  if (parsed.success && parsed.data !== field.state.value) {
                    field.handleChange(parsed.data)
                  }
                }}
                onChange={(e) => field.handleChange(e.target.value)}
                placeholder={isTransmission ? t("form.placeholders.transmissionUrl") : t("form.placeholders.url")}
              />
              {field.state.meta.isTouched && field.state.meta.errors[0] && (
                <p className="text-sm text-destructive">{field.state.meta.errors[0]}</p>
              )}
            </div>
          )}
        </form.Field>

        <form.Field name="tlsSkipVerify">
          {(field) => (
            <div className="flex items-center justify-between gap-4 rounded-lg border bg-muted/40 p-4">
              <Label htmlFor="tls-skip-verify" className="flex items-center gap-2">
                {t("form.labels.skipTlsVerification")}
                <FieldHelp>{t("form.labels.skipTlsDescription")}</FieldHelp>
              </Label>
              <Switch
                id="tls-skip-verify"
                checked={field.state.value}
                onCheckedChange={(checked) => field.handleChange(checked)}
              />
            </div>
          )}
        </form.Field>

        <form.Field name="hasLocalFilesystemAccess">
          {(field) => (
            <div className="flex items-center justify-between gap-4 rounded-lg border bg-muted/40 p-4">
              <Label htmlFor="local-filesystem-access" className="flex items-center gap-2">
                {t("form.labels.localFilesystemAccess")}
                <FieldHelp>{t("form.labels.localFilesystemDescription")}</FieldHelp>
              </Label>
              <Switch
                id="local-filesystem-access"
                checked={field.state.value}
                onCheckedChange={(checked) => field.handleChange(checked)}
              />
            </div>
          )}
        </form.Field>

        <div className="space-y-2">
          <Label htmlFor="auth-type" className="flex items-center gap-2">
            {isTransmission ? t("preferences.settingsPanel.labels.transmissionAuth") : t("form.labels.authType")}
            <FieldHelp>{isTransmission ? t("preferences.settingsPanel.labels.transmissionAuthDescription") : t("form.labels.authTypeDescription")}</FieldHelp>
          </Label>
          <select
            id="auth-type"
            value={authType}
            onChange={(e) => setAuthType(e.target.value as InstanceAuthType)}
            className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background"
          >
            <option value="none">{t("form.authType.none")}</option>
            <option value="usernamePassword">{t("form.authType.usernamePassword")}</option>
            {!isTransmission && <option value="apiKey">{t("form.authType.apiKey")}</option>}
          </select>
        </div>

        {authType === "usernamePassword" && (
          <>
            <form.Field name="username">
              {(field) => (
                <div className="space-y-2">
                  <Label htmlFor={field.name}>{t("form.labels.username")}</Label>
                  <Input
                    id={field.name}
                    value={field.state.value}
                    onBlur={field.handleBlur}
                    onChange={(e) => field.handleChange(e.target.value)}
                    placeholder={t("form.placeholders.username")}
                    data-1p-ignore
                    autoComplete="off"
                  />
                </div>
              )}
            </form.Field>

            <form.Field
              name="password"
            >
              {(field) => (
                <div className="space-y-2">
                  <Label htmlFor={field.name}>{t("form.labels.password")}</Label>
                  <Input
                    id={field.name}
                    type="password"
                    value={field.state.value}
                    onBlur={field.handleBlur}
                    onChange={(e) => field.handleChange(e.target.value)}
                    placeholder={instance ? t("form.placeholders.passwordExisting") : t("form.placeholders.passwordNew")}
                    data-1p-ignore
                    autoComplete="off"
                  />
                  {field.state.meta.isTouched && field.state.meta.errors[0] && (
                    <p className="text-sm text-destructive">{field.state.meta.errors[0]}</p>
                  )}
                </div>
              )}
            </form.Field>
          </>
        )}

        {!isTransmission && authType === "apiKey" && (
          <form.Field name="apiKey">
            {(field) => (
              <div className="space-y-2">
                <Label htmlFor={field.name}>{t("form.authType.apiKey")}</Label>
                <Input
                  id={field.name}
                  type="password"
                  value={field.state.value}
                  onBlur={() => {
                    field.handleBlur()
                    if (instance?.hasApiKey && field.state.value === "") {
                      field.handleChange("<redacted>")
                    }
                  }}
                  onFocus={() => {
                    if (field.state.value === "<redacted>") {
                      field.handleChange("")
                    }
                  }}
                  onChange={(e) => field.handleChange(e.target.value)}
                  placeholder={instance ? t("form.placeholders.apiKeyExisting") : t("form.placeholders.apiKeyNew")}
                  data-1p-ignore
                  autoComplete="off"
                />
              </div>
            )}
          </form.Field>
        )}

        {!isTransmission && (
          <div className="space-y-4">
            <div className="flex items-center justify-between">
              <Label htmlFor="basic-auth-toggle" className="flex items-center gap-2">
                {t("form.labels.httpBasicAuth")}
                <FieldHelp>{t("form.labels.httpBasicAuthDescription")}</FieldHelp>
              </Label>
              <Switch
                id="basic-auth-toggle"
                checked={showBasicAuth}
                onCheckedChange={setShowBasicAuth}
              />
            </div>

            {showBasicAuth && (
              <div className="space-y-4 pl-6 border-l-2 border-muted">
                <form.Field name="basicUsername">
                  {(field) => (
                    <div className="space-y-2">
                      <Label htmlFor={field.name}>{t("form.labels.basicAuthUsername")}</Label>
                      <Input
                        id={field.name}
                        value={field.state.value}
                        onBlur={field.handleBlur}
                        onChange={(e) => field.handleChange(e.target.value)}
                        placeholder={t("form.placeholders.basicAuthUsername")}
                        data-1p-ignore
                        autoComplete="off"
                      />
                    </div>
                  )}
                </form.Field>

                <form.Field
                  name="basicPassword"
                  validators={{
                    onChange: ({ value }) =>
                      showBasicAuth && value === "" ? t("form.validation.basicAuthPasswordRequired") : undefined,
                  }}
                >
                  {(field) => (
                    <div className="space-y-2">
                      <Label htmlFor={field.name}>{t("form.labels.basicAuthPassword")}</Label>
                      <Input
                        id={field.name}
                        type="password"
                        value={field.state.value}
                        onBlur={field.handleBlur}
                        onFocus={() => {
                        // Clear the redacted placeholder when user focuses to edit
                          if (field.state.value === "<redacted>") {
                            field.handleChange("")
                          }
                        }}
                        onChange={(e) => field.handleChange(e.target.value)}
                        placeholder={t("form.placeholders.basicAuthPassword")}
                        data-1p-ignore
                        autoComplete="off"
                      />
                      {field.state.meta.errors[0] && (
                        <p className="text-sm text-destructive">{field.state.meta.errors[0]}</p>
                      )}
                    </div>
                  )}
                </form.Field>
              </div>
            )}
          </div>
        )}

        {!formId && (
          <div className="flex gap-2">
            <form.Subscribe
              selector={(state) => [state.canSubmit, state.isSubmitting]}
            >
              {([canSubmit, isSubmitting]) => (
                <Button
                  type="submit"
                  disabled={!canSubmit || isSubmitting || isCreating || isUpdating}
                >
                  {(isCreating || isUpdating) ? t("form.buttons.saving") : instance ? t("form.buttons.updateInstance") : t("form.buttons.addInstance")}
                </Button>
              )}
            </form.Subscribe>

            <Button
              type="button"
              variant="outline"
              onClick={onCancel}
            >
              {t("form.buttons.cancel")}
            </Button>
          </div>
        )}
      </form>

    </>
  )
}
