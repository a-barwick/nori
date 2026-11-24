import type { MouseEventHandler, ReactNode } from "react";
import Link from "next/link";
import { Medallion } from "@/components/mark/NoriMark";
import type { MarkState } from "@/lib/types";

export function AppFrame({ children, actions, state = "idle", status, onToggleActivity, activityOpen }: {
  children: ReactNode;
  actions?: ReactNode;
  state?: MarkState;
  status?: string;
  onToggleActivity?: MouseEventHandler<HTMLButtonElement>;
  activityOpen?: boolean;
}) {
  const identity = <>
    <Medallion size={48} state={state} watch />
    <span className="nori-identity-text"><strong>Nori</strong>{status && <small>{status}</small>}</span>
  </>;
  return (
    <div className="nori-backdrop">
      <div className="nori-window">
        <header className="nori-header">
          <Link href="/" className="nori-brand" aria-label="Nori home">
            <span className="nori-logo" aria-hidden />
            <span>Nori</span>
          </Link>
          <span className="nori-header-divider" aria-hidden />
          {onToggleActivity ? (
            <button type="button" className="nori-assistant-name" aria-label="Nori: open agent panel" aria-expanded={activityOpen} onClick={onToggleActivity}>
              {identity}
            </button>
          ) : <div className="nori-assistant-name">{identity}</div>}
          <div className="nori-header-actions">{actions}</div>
        </header>
        {children}
      </div>
    </div>
  );
}
