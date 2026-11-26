"use client";

import { useState } from "react";
import { KeyRound, Lock } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import {
  deleteModelKey,
  errorText,
  saveModelKey,
  type ModelKeyProvider,
  type ModelKeysResponse,
} from "@/lib/assistant/api";
import { announce } from "@/lib/assistant/announce";
import { relative } from "@/lib/assistant/format";
import { useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import { cn } from "@/lib/utils";
import { Confirm, Empty, Failure, Field, inputClass, Loading, Notice, Pill, Section } from "./ui";

export function needsByokSetup(data?: ModelKeysResponse): boolean {
  return !!data?.byok_required && !data.providers.some((provider) => provider.configured);
}

export function byokPromptLabel(data?: ModelKeysResponse): string {
  return data?.providers[0]?.label || "Vercel AI Gateway";
}

export function ApiKeysSettings() {
  const keys = useResource<ModelKeysResponse>("/settings/model-keys", { poll: 0, versioned: false });
  const invalidate = useResourceInvalidation();

  if (keys.loading) return <Loading />;
  if (!keys.data) {
    return <Failure error={keys.error ?? "API keys are unavailable."} onRetry={() => void keys.reload()} />;
  }

  const updateProvider = (updated: ModelKeyProvider) => {
    if (!keys.data) return;
    keys.apply({
      ...keys.data,
      providers: keys.data.providers.map((item) => (item.provider === updated.provider ? updated : item)),
    });
    invalidate("/settings/model-keys", "/capabilities");
  };

  return (
    <ApiKeysContent
      data={keys.data}
      error={keys.error}
      onRetry={() => void keys.reload()}
      onProviderUpdated={updateProvider}
    />
  );
}

export function ApiKeysContent({
  data,
  error,
  onRetry,
  onProviderUpdated,
  initialEditingProvider,
}: {
  data: ModelKeysResponse;
  error?: unknown;
  onRetry?: () => void;
  onProviderUpdated: (updated: ModelKeyProvider) => void;
  initialEditingProvider?: string | null;
}) {
  const [editingProvider, setEditingProvider] = useState<string | null>(initialEditingProvider ?? null);
  const [draftKey, setDraftKey] = useState("");
  const [busyProvider, setBusyProvider] = useState<{ provider: string; action: "save" | "delete" } | null>(null);
  const [providerErrors, setProviderErrors] = useState<Record<string, string>>({});

  const requiresKey = needsByokSetup(data);
  const primaryLabel = byokPromptLabel(data);

  const startEditing = (provider: string) => {
    setEditingProvider(provider);
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
    <div className="space-y-4">
      {requiresKey && (
        <Notice tone="warning">
          Add your {primaryLabel} API key below to start chatting and running tasks with Nori.
        </Notice>
      )}

      {!!error && <Failure error={error} onRetry={onRetry} />}

      <Notice tone="neutral">
        <span className="inline-flex items-center gap-2">
          <Lock size={14} className="shrink-0" aria-hidden />
          <span>API keys are encrypted at rest and never displayed again after saving.</span>
        </span>
      </Notice>

      {data.providers.length === 0 ? (
        <Empty>No model key providers are available.</Empty>
      ) : (
        <Section title="Model Providers">
          <ul
            aria-label="Model providers"
            className="divide-y divide-hairline overflow-hidden rounded-card border border-card-border bg-surface"
          >
            {data.providers.map((provider) => {
              const isEditing = editingProvider === provider.provider;
              const isSaving = busyProvider?.provider === provider.provider && busyProvider.action === "save";
              const isDeleting = busyProvider?.provider === provider.provider && busyProvider.action === "delete";
              const isBusy = isSaving || isDeleting;
              const providerError = providerErrors[provider.provider];

              return (
                <li key={provider.provider} className="px-4 py-3.5">
                  <div className="flex flex-wrap items-center gap-3">
                    <span
                      className={cn(
                        "grid size-9 shrink-0 place-items-center rounded-[10px]",
                        provider.configured ? "bg-accent-subtle text-accent-text" : "bg-fill-1 text-ink-2",
                      )}
                    >
                      <KeyRound size={18} strokeWidth={1.85} aria-hidden />
                    </span>

                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-row font-semibold text-ink">{provider.label}</span>
                        <Pill tone={provider.configured ? "ok" : data.byok_required ? "warning" : "neutral"}>
                          {provider.configured ? "Configured" : data.byok_required ? "Required" : "Not configured"}
                        </Pill>
                      </div>
                      <p className="mt-0.5 text-meta text-ink-2">
                        {provider.configured ? (
                          <>
                            <span>Configured</span>
                            {provider.last4 && (
                              <>
                                {" "}
                                <span className="font-mono text-mono-data text-ink">••••{provider.last4}</span>
                              </>
                            )}
                            {provider.updated_at && <span> · Updated {relative(provider.updated_at)}</span>}
                          </>
                        ) : (
                          "Not configured"
                        )}
                      </p>
                    </div>

                    {!isEditing && (
                      <div className="flex shrink-0 items-center gap-2">
                        <Button
                          size="sm"
                          variant={provider.configured ? "neutral" : "primary"}
                          disabled={!!busyProvider}
                          onClick={() => startEditing(provider.provider)}
                        >
                          {provider.configured ? "Replace" : "Add key"}
                        </Button>
                        {provider.configured && (
                          <Confirm
                            danger
                            title={`Remove ${provider.label} key?`}
                            body={
                              data.byok_required
                                ? "Nori will not be able to run models until you add a new key."
                                : "Your saved key will be permanently deleted."
                            }
                            confirm="Remove"
                            onConfirm={() => void handleDelete(provider)}
                            trigger={
                              <Button size="sm" variant="quiet" disabled={!!busyProvider}>
                                {isDeleting ? "Removing…" : "Remove"}
                              </Button>
                            }
                          />
                        )}
                      </div>
                    )}
                  </div>

                  {!isEditing && providerError && (
                    <div className="mt-3">
                      <Notice tone="danger">{providerError}</Notice>
                    </div>
                  )}

                  {isEditing && (
                    <form
                      className="mt-3.5 space-y-3 border-t border-hairline pt-3.5"
                      onSubmit={(event) => {
                        event.preventDefault();
                        void handleSave(provider);
                      }}
                    >
                      <Field label={provider.configured ? `New ${provider.label} API key` : `${provider.label} API key`}>
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

                      {providerError && <Notice tone="danger">{providerError}</Notice>}

                      <div className="flex flex-wrap items-center gap-2">
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
                    </form>
                  )}
                </li>
              );
            })}
          </ul>
        </Section>
      )}
    </div>
  );
}
