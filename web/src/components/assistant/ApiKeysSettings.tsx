"use client";

import { useState } from "react";
import { Button } from "@/components/ui/primitives";
import {
  deleteModelKey,
  errorText,
  saveModelKey,
  type ModelKeyProvider,
  type ModelKeysResponse,
} from "@/lib/assistant/api";
import { announce } from "@/lib/assistant/announce";
import { byokPromptLabel, needsByokSetup } from "@/lib/assistant/modelSetup";
import { useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import { cn } from "@/lib/utils";
import { ProviderBrandIcon } from "./BrandIcons";
import { Confirm, Empty, Failure, Field, inputClass, Loading, Notice, Row, Rows, Section } from "./ui";

export { byokPromptLabel, needsByokSetup };

export function ApiKeysSettings({
  onKeySaved,
  hasHomeChat = false,
}: {
  onKeySaved?: (provider: ModelKeyProvider) => void;
  hasHomeChat?: boolean;
}) {
  const keys = useResource<ModelKeysResponse>("/settings/model-keys", { poll: 0, versioned: false });
  const invalidate = useResourceInvalidation();

  if (keys.loading) return <Loading />;
  if (!keys.data) {
    return <Failure error={keys.error ?? "Providers are unavailable."} onRetry={() => void keys.reload()} />;
  }

  const updateProvider = (updated: ModelKeyProvider) => {
    if (!keys.data) return;
    keys.apply({
      ...keys.data,
      providers: keys.data.providers.map((item) => (item.provider === updated.provider ? updated : item)),
    });
    invalidate("/settings/model-keys", "/settings/models", "/capabilities");
  };

  return (
    <ApiKeysContent
      data={keys.data}
      error={keys.error}
      onRetry={() => void keys.reload()}
      onProviderUpdated={updateProvider}
      onKeySaved={onKeySaved}
      hasHomeChat={hasHomeChat}
    />
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
}: {
  data: ModelKeysResponse;
  error?: unknown;
  onRetry?: () => void;
  onProviderUpdated: (updated: ModelKeyProvider) => void;
  onKeySaved?: (provider: ModelKeyProvider) => void;
  initialEditingProvider?: string | null;
  hasHomeChat?: boolean;
}) {
  const [editingProvider, setEditingProvider] = useState<string | null>(initialEditingProvider ?? null);
  const [draftKey, setDraftKey] = useState("");
  const [busyProvider, setBusyProvider] = useState<{ provider: string; action: "save" | "delete" } | null>(null);
  const [providerErrors, setProviderErrors] = useState<Record<string, string>>({});

  const requiresKey = needsByokSetup(data, hasHomeChat);
  const primaryLabel = byokPromptLabel(data);

  const toggleEditing = (provider: string) => {
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
              const isEditing = editingProvider === provider.provider;
              const isSaving = busyProvider?.provider === provider.provider && busyProvider.action === "save";
              const isDeleting = busyProvider?.provider === provider.provider && busyProvider.action === "delete";
              const isBusy = isSaving || isDeleting;
              const providerError = providerErrors[provider.provider];

              return (
                <li key={provider.provider}>
                  <Row
                    onClick={() => toggleEditing(provider.provider)}
                    leading={<ProviderBrandIcon provider={provider.provider} />}
                    title={provider.label}
                    trailing={
                      provider.configured && provider.last4 ? (
                        <span className="font-mono text-mono-data text-ink-2">••••{provider.last4}</span>
                      ) : undefined
                    }
                    chevron
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
