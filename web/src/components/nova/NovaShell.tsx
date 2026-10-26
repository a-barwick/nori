import type { MouseEventHandler, ReactNode } from "react";
import { Medallion } from "@/components/mark/NoriMark";
import type { MarkState } from "@/lib/types";

/** Shared framing for the live conversation and the isolated design preview. */
export function NovaShell({ children, actions, preview = false, state = "idle", status, onToggleActivity, activityOpen }: {
  children: ReactNode;
  actions?: ReactNode;
  preview?: boolean;
  state?: MarkState;
  status?: string;
  onToggleActivity?: MouseEventHandler<HTMLButtonElement>;
  activityOpen?: boolean;
}) {
  const identity = <>
    <Medallion size={48} look="onigiri" state={state} watch />
    <span className="nova-identity-text"><strong>Nori</strong>{status && <small>{status}</small>}</span>
  </>;
  return (
    <div className="nova-backdrop">
      <div className="nova-window">
        <header className="nova-header">
          <a href={preview ? "/redesign" : "/"} className="nova-brand" aria-label="Nova home">
            <span className="nova-logo" aria-hidden />
            <span>Nova</span>
          </a>
          <span className="nova-header-divider" aria-hidden />
          {onToggleActivity ? (
            <button type="button" className="nova-assistant-name" aria-label="Nori: open agent panel" aria-expanded={activityOpen} onClick={onToggleActivity}>
              {identity}
            </button>
          ) : <div className="nova-assistant-name">{identity}</div>}
          <div className="nova-header-actions">{actions}</div>
          {preview && <span className="nova-profile" aria-label="Maya, sample profile">M</span>}
        </header>
        {children}
      </div>
    </div>
  );
}
