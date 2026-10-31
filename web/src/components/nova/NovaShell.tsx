import type { MouseEventHandler, ReactNode } from "react";
import Link from "next/link";
import { Medallion } from "@/components/mark/NoriMark";
import type { MarkState } from "@/lib/types";

/** Framing for the live conversation: identity header over the app body. */
export function NovaShell({ children, actions, state = "idle", status, onToggleActivity, activityOpen }: {
  children: ReactNode;
  actions?: ReactNode;
  state?: MarkState;
  status?: string;
  onToggleActivity?: MouseEventHandler<HTMLButtonElement>;
  activityOpen?: boolean;
}) {
  const identity = <>
    <Medallion size={48} state={state} watch />
    <span className="nova-identity-text"><strong>Nori</strong>{status && <small>{status}</small>}</span>
  </>;
  return (
    <div className="nova-backdrop">
      <div className="nova-window">
        <header className="nova-header">
          <Link href="/" className="nova-brand" aria-label="Nova home">
            <span className="nova-logo" aria-hidden />
            <span>Nova</span>
          </Link>
          <span className="nova-header-divider" aria-hidden />
          {onToggleActivity ? (
            <button type="button" className="nova-assistant-name" aria-label="Nori: open agent panel" aria-expanded={activityOpen} onClick={onToggleActivity}>
              {identity}
            </button>
          ) : <div className="nova-assistant-name">{identity}</div>}
          <div className="nova-header-actions">{actions}</div>
        </header>
        {children}
      </div>
    </div>
  );
}
