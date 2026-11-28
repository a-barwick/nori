"use client";

import { useState } from "react";
import { Globe } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import {
  deleteModelKey,
  deleteServiceKey,
  errorText,
  saveModelKey,
  saveServiceKey,
  type ModelKeyProvider,
  type ModelKeysResponse,
  type ServiceKeysResponse,
  type ServiceKeyStatus,
} from "@/lib/assistant/api";
import { announce } from "@/lib/assistant/announce";
import { byokPromptLabel, needsByokSetup } from "@/lib/assistant/modelSetup";
import { useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { Capabilities, CapabilitiesServiceKeys } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { BrandIconWell, ProviderBrandIcon } from "./BrandIcons";
import { Confirm, Empty, Failure, Field, inputClass, Loading, Notice, Row, Rows, Section } from "./ui";

export { byokPromptLabel, needsByokSetup };

const TRIAL_BYOK_NOTICE = "Bring-your-own API keys are available on paid plans. Your trial includes hosted credits.";

export function resolveServiceKeys(
  serviceKeys?: ServiceKeysResponse,
  capabilitiesServiceKeys?: CapabilitiesServiceKeys,
): ServiceKeyStatus[] {
  if (serviceKeys?.services && serviceKeys.services.length > 0) {
    return serviceKeys.services;
  }
  const exaConfigured = capabilitiesServiceKeys?.exa_configured ?? false;
  const platformAvailable = capabilitiesServiceKeys?.platform_exa_available ?? false;
  return [
    {
      service: "exa",
      label: "Exa Web Search",
      configured: exaConfigured,
      source: exaConfigured ? "byok" : platformAvailable ? "platform" : "none",
    },
  ];
}

function serviceLabel(service: ServiceKeyStatus): string {
  if (service.label) return service.label;
  if (service.service === "exa") return "Exa Web Search";
  return service.service.charAt(0).toUpperCase() + service.service.slice(1);
}

function serviceMaskedKey(service: ServiceKeyStatus): string | undefined {
  if (!service.configured) return undefined;
  if (service.masked_key) return service.masked_key;
  if (service.last4) return `••••${service.last4}`;
  return "Configured";
}

export function ApiKeysSettings({
  onKeySaved,
  hasHomeChat = false,
}: {
  onKeySaved?: (provider: ModelKeyProvider) => void;
  hasHomeChat?: boolean;
}) {
  const keys = useResource<ModelKeysResponse>("/settings/model-keys", { poll: 0, versioned: false });
  const serviceKeys = useResource<ServiceKeysResponse>("/settings/service-keys", { poll: 0, versioned: false });
  const capabilities = useResource<Capabilities>("/capabilities", { poll: 120_000 });
  const invalidate = useResourceInvalidation();

  if (keys.loading) return <Loading />;
  if (!keys.data) {
    return <Failure error={keys.error ?? "Providers are unavailable."} onRetry={() => void keys.reload()} />;
  }

  const entitlement = capabilities.data?.entitlement;
  const allowByok = serviceKeys.data?.allow_byok ?? entitlement?.allow_byok ?? true;
  const trialCreditsExhausted = Boolean(
    entitlement &&
      entitlement.credit_grant_usd > 0 &&
      entitlement.credit_remaining_usd <= 0 &&
      !entitlement.allow_byok,
  );
  const resolvedServices = resolveServiceKeys(serviceKeys.data, capabilities.data?.service_keys);

  const updateProvider = (updated: ModelKeyProvider) => {
    if (!keys.data) return;
    keys.apply({
      ...keys.data,
      providers: keys.data.providers.map((item) => (item.provider === updated.provider ? updated : item)),
    });
    invalidate("/settings/model-keys", "/settings/models", "/capabilities");
  };

  const updateService = (updated: ServiceKeyStatus) => {
    const current = serviceKeys.data?.services ?? resolvedServices;
    const exists = current.some((item) => item.service === updated.service);
    serviceKeys.apply({
      allow_byok: serviceKeys.data?.allow_byok ?? allowByok,
      services: exists
        ? current.map((item) => (item.service === updated.service ? { ...item, ...updated } : item))
        : [...current, updated],
    });
    invalidate("/settings/service-keys", "/capabilities");
  };

  return (
    <div className="space-y-6">
      <ApiKeysContent
        data={keys.data}
        error={keys.error}
        onRetry={() => void keys.reload()}
        onProviderUpdated={updateProvider}
        onKeySaved={onKeySaved}
        hasHomeChat={hasHomeChat}
        allowByok={allowByok}
        showTrialNotice={!trialCreditsExhausted}
      />

      <ServiceKeysContent
        services={resolvedServices}
        allowByok={allowByok}
        showTrialNotice={false}
        error={serviceKeys.data ? undefined : serviceKeys.error}
        onRetry={() => void serviceKeys.reload()}
        onServiceUpdated={updateService}
      />
    </div>
  );
}

export function ApiKeysContent({
  data,
  error,
  onRetry,
  onProviderUpdated,
  onKeySaved,
  initialEditingProvider,
  hasHomeChat = false,
  allowByok = true,
  showTrialNotice = true,
}: {
  data: ModelKeysResponse;
  error?: unknown;
  onRetry?: () => void;
  onProviderUpdated: (updated: ModelKeyProvider) => void;
  onKeySaved?: (provider: ModelKeyProvider) => void;
  initialEditingProvider?: string | null;
  hasHomeChat?: boolean;
  allowByok?: boolean;
  showTrialNotice?: boolean;
}) {
  const [editingProvider, setEditingProvider] = useState<string | null>(initialEditingProvider ?? null);
  const [draftKey, setDraftKey] = useState("");
  const [busyProvider, setBusyProvider] = useState<{ provider: string; action: "save" | "delete" } | null>(null);
  const [providerErrors, setProviderErrors] = useState<Record<string, string>>({});

  const requiresKey = allowByok && needsByokSetup(data, hasHomeChat);
  const primaryLabel = byokPromptLabel(data);

  const toggleEditing = (provider: string) => {
    if (!allowByok) return;
    setEditingProvider((current) => (current === provider ? null : provider));
    setDraftKey("");
    setProviderErrors((prev) => {
      if (!(provider in prev)) return prev;
      const next = { ...prev };
      delete next[provider];
      return next;
    });
  };

  const cancelEditing = (provider: string) => {
    setEditingProvider(null);
    setDraftKey("");
    setProviderErrors((prev) => {
      if (!(provider in prev)) return prev;
      const next = { ...prev };
      delete next[provider];
      return next;
    });
  };

  const handleSave = async (provider: ModelKeyProvider) => {
    if (!allowByok) return;
    const trimmed = draftKey.trim();
    if (!trimmed) {
      setProviderErrors((prev) => ({ ...prev, [provider.provider]: "Enter an API key." }));
      return;
    }
    setBusyProvider({ provider: provider.provider, action: "save" });
    setProviderErrors((prev) => {
      if (!(provider.provider in prev)) return prev;
      const next = { ...prev };
      delete next[provider.provider];
      return next;
    });
    try {
      const updated = await saveModelKey(provider.provider, trimmed);
      setDraftKey("");
      setEditingProvider(null);
      onProviderUpdated(updated);
      onKeySaved?.(updated);
      announce(`${provider.label} key saved`);
    } catch (err) {
      setProviderErrors((prev) => ({ ...prev, [provider.provider]: errorText(err) }));
    } finally {
      setBusyProvider(null);
    }
  };

  const handleDelete = async (provider: ModelKeyProvider) => {
    setBusyProvider({ provider: provider.provider, action: "delete" });
    setProviderErrors((prev) => {
      if (!(provider.provider in prev)) return prev;
      const next = { ...prev };
      delete next[provider.provider];
      return next;
    });
    try {
      const updated = await deleteModelKey(provider.provider);
      if (editingProvider === provider.provider) {
        setEditingProvider(null);
        setDraftKey("");
      }
      onProviderUpdated(updated);
      announce(`${provider.label} key removed`);
    } catch (err) {
      setProviderErrors((prev) => ({ ...prev, [provider.provider]: errorText(err) }));
    } finally {
      setBusyProvider(null);
    }
  };

  return (
    <Section title="Providers">
      <div className="space-y-3">
        {!allowByok && showTrialNotice && <Notice>{TRIAL_BYOK_NOTICE}</Notice>}

        {requiresKey && (
          <Notice tone="warning">
            Add your {primaryLabel} API key below to start chatting and running tasks with Nori.
          </Notice>
        )}

        {!!error && <Failure error={error} onRetry={onRetry} />}

        {data.providers.length === 0 ? (
          <Empty>No model providers are available.</Empty>
        ) : (
          <Rows label="Providers">
            {data.providers.map((provider) => {
              const isEditing = allowByok && editingProvider === provider.provider;
              const isSaving = busyProvider?.provider === provider.provider && busyProvider.action === "save";
              const isDeleting = busyProvider?.provider === provider.provider && busyProvider.action === "delete";
              const isBusy = isSaving || isDeleting;
              const providerError = providerErrors[provider.provider];

              return (
                <li key={provider.provider}>
                  <Row
                    onClick={allowByok ? () => toggleEditing(provider.provider) : undefined}
                    leading={<ProviderBrandIcon provider={provider.provider} />}
                    title={provider.label}
                    trailing={
                      provider.configured && provider.last4 ? (
                        <span className="font-mono text-mono-data text-ink-2">••••{provider.last4}</span>
                      ) : !allowByok ? (
                        <span className="text-meta text-disabled">Paid plans</span>
                      ) : undefined
                    }
                    chevron={allowByok}
                  />

                  {!isEditing && providerError && (
                    <div className="px-4 pb-3">
                      <Notice tone="danger">{providerError}</Notice>
                    </div>
                  )}

                  {isEditing && (
                    <form
                      className="space-y-3 border-t border-hairline px-4 py-4"
                      onSubmit={(event) => {
                        event.preventDefault();
                        void handleSave(provider);
                      }}
                    >
                      <Field label={provider.configured ? `Replace ${provider.label} API key` : `${provider.label} API key`}>
                        <input
                          type="password"
                          autoComplete="new-password"
                          data-1p-ignore
                          data-lpignore="true"
                          data-bwignore="true"
                          spellCheck={false}
                          value={draftKey}
                          onChange={(event) => {
                            setDraftKey(event.target.value);
                            if (providerError) {
                              setProviderErrors((prev) => {
                                const next = { ...prev };
                                delete next[provider.provider];
                                return next;
                              });
                            }
                          }}
                          placeholder="Paste your API key"
                          className={cn(inputClass, "font-mono")}
                          disabled={isBusy}
                          autoFocus
                        />
                      </Field>

                      <p className="text-caption text-ink-3">
                        Encrypted at rest and never shown again after saving.
                      </p>

                      {providerError && <Notice tone="danger">{providerError}</Notice>}

                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <div className="flex items-center gap-2">
                          <Button type="submit" size="sm" variant="primary" disabled={isBusy || !draftKey.trim()}>
                            {isSaving ? "Saving…" : "Save"}
                          </Button>
                          <Button
                            type="button"
                            size="sm"
                            variant="quiet"
                            disabled={isBusy}
                            onClick={() => cancelEditing(provider.provider)}
                          >
                            Cancel
                          </Button>
                        </div>

                        {provider.configured && (
                          <Confirm
                            danger
                            title={`Remove ${provider.label} key?`}
                            body="Models using this key will require another key or model selection."
                            confirm="Remove"
                            onConfirm={() => void handleDelete(provider)}
                            trigger={
                              <Button type="button" size="sm" variant="danger" disabled={!!busyProvider}>
                                {isDeleting ? "Removing…" : "Remove"}
                              </Button>
                            }
                          />
                        )}
                      </div>
                    </form>
                  )}
                </li>
              );
            })}
          </Rows>
        )}
      </div>
    </Section>
  );
}

export function ServiceKeysContent({
  services,
  allowByok = true,
  showTrialNotice = true,
  error,
  onRetry,
  onServiceUpdated,
  initialEditingService,
}: {
  services: ServiceKeyStatus[];
  allowByok?: boolean;
  showTrialNotice?: boolean;
  error?: unknown;
  onRetry?: () => void;
  onServiceUpdated: (updated: ServiceKeyStatus) => void;
  initialEditingService?: string | null;
}) {
  const [editingService, setEditingService] = useState<string | null>(initialEditingService ?? null);
  const [draftKey, setDraftKey] = useState("");
  const [busyService, setBusyService] = useState<{ service: string; action: "save" | "delete" } | null>(null);
  const [serviceErrors, setServiceErrors] = useState<Record<string, string>>({});

  if (services.length === 0 && !error) return null;

  const toggleEditing = (serviceId: string) => {
    if (!allowByok) return;
    setEditingService((current) => (current === serviceId ? null : serviceId));
    setDraftKey("");
    setServiceErrors((prev) => {
      if (!(serviceId in prev)) return prev;
      const next = { ...prev };
      delete next[serviceId];
      return next;
    });
  };

  const cancelEditing = (serviceId: string) => {
    setEditingService(null);
    setDraftKey("");
    setServiceErrors((prev) => {
      if (!(serviceId in prev)) return prev;
      const next = { ...prev };
      delete next[serviceId];
      return next;
    });
  };

  const handleSave = async (item: ServiceKeyStatus) => {
    if (!allowByok) return;
    const trimmed = draftKey.trim();
    if (!trimmed) {
      setServiceErrors((prev) => ({ ...prev, [item.service]: "Enter an API key." }));
      return;
    }
    setBusyService({ service: item.service, action: "save" });
    setServiceErrors((prev) => {
      if (!(item.service in prev)) return prev;
      const next = { ...prev };
      delete next[item.service];
      return next;
    });
    try {
      const updated = await saveServiceKey(item.service, trimmed);
      setDraftKey("");
      setEditingService(null);
      onServiceUpdated(updated);
      announce(`${serviceLabel(item)} key saved`);
    } catch (err) {
      setServiceErrors((prev) => ({ ...prev, [item.service]: errorText(err) }));
    } finally {
      setBusyService(null);
    }
  };

  const handleDelete = async (item: ServiceKeyStatus) => {
    setBusyService({ service: item.service, action: "delete" });
    setServiceErrors((prev) => {
      if (!(item.service in prev)) return prev;
      const next = { ...prev };
      delete next[item.service];
      return next;
    });
    try {
      const updated = await deleteServiceKey(item.service);
      if (editingService === item.service) {
        setEditingService(null);
        setDraftKey("");
      }
      onServiceUpdated(updated);
      announce(`${serviceLabel(item)} key removed`);
    } catch (err) {
      setServiceErrors((prev) => ({ ...prev, [item.service]: errorText(err) }));
    } finally {
      setBusyService(null);
    }
  };

  return (
    <Section title="Web search">
      <div className="space-y-3">
        {!allowByok && showTrialNotice && <Notice>{TRIAL_BYOK_NOTICE}</Notice>}

        {!!error && <Failure error={error} onRetry={onRetry} />}

        {services.length > 0 && (
          <Rows label="Web search">
            {services.map((item) => {
              const label = serviceLabel(item);
              const masked = serviceMaskedKey(item);
              const isEditing = allowByok && editingService === item.service;
              const isSaving = busyService?.service === item.service && busyService.action === "save";
              const isDeleting = busyService?.service === item.service && busyService.action === "delete";
              const isBusy = isSaving || isDeleting;
              const serviceError = serviceErrors[item.service];

              return (
                <li key={item.service}>
                  <Row
                    onClick={allowByok ? () => toggleEditing(item.service) : undefined}
                    leading={
                      <BrandIconWell brand={false}>
                        <Globe size={17} strokeWidth={1.85} aria-hidden />
                      </BrandIconWell>
                    }
                    title={label}
                    trailing={
                      masked ? (
                        <span className="font-mono text-mono-data text-ink-2">{masked}</span>
                      ) : !allowByok ? (
                        <span className="text-meta text-disabled">Paid plans</span>
                      ) : undefined
                    }
                    chevron={allowByok}
                  />

                  {!isEditing && serviceError && (
                    <div className="px-4 pb-3">
                      <Notice tone="danger">{serviceError}</Notice>
                    </div>
                  )}

                  {isEditing && (
                    <form
                      className="space-y-3 border-t border-hairline px-4 py-4"
                      onSubmit={(event) => {
                        event.preventDefault();
                        void handleSave(item);
                      }}
                    >
                      <Field label={item.configured ? `Replace ${label} API key` : `${label} API key`}>
                        <input
                          type="password"
                          autoComplete="new-password"
                          data-1p-ignore
                          data-lpignore="true"
                          data-bwignore="true"
                          spellCheck={false}
                          value={draftKey}
                          onChange={(event) => {
                            setDraftKey(event.target.value);
                            if (serviceError) {
                              setServiceErrors((prev) => {
                                const next = { ...prev };
                                delete next[item.service];
                                return next;
                              });
                            }
                          }}
                          placeholder="Paste your Exa API key"
                          className={cn(inputClass, "font-mono")}
                          disabled={isBusy}
                          autoFocus
                        />
                      </Field>

                      <p className="text-caption text-ink-3">
                        Encrypted at rest and never shown again after saving. Uses your own Exa key instead of hosted credits.
                      </p>

                      {serviceError && <Notice tone="danger">{serviceError}</Notice>}

                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <div className="flex items-center gap-2">
                          <Button type="submit" size="sm" variant="primary" disabled={isBusy || !draftKey.trim()}>
                            {isSaving ? "Validating…" : "Save"}
                          </Button>
                          <Button
                            type="button"
                            size="sm"
                            variant="quiet"
                            disabled={isBusy}
                            onClick={() => cancelEditing(item.service)}
                          >
                            Cancel
                          </Button>
                        </div>

                        {item.configured && (
                          <Confirm
                            danger
                            title={`Remove ${label} key?`}
                            body="Web search will use hosted credits when available."
                            confirm="Remove"
                            onConfirm={() => void handleDelete(item)}
                            trigger={
                              <Button type="button" size="sm" variant="danger" disabled={!!busyService}>
                                {isDeleting ? "Removing…" : "Remove"}
                              </Button>
                            }
                          />
                        )}
                      </div>
                    </form>
                  )}
                </li>
              );
            })}
          </Rows>
        )}
      </div>
    </Section>
  );
}
