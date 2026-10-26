import type { ReactNode } from "react";
import { Medallion } from "@/components/mark/NoriMark";

/** Shared framing for the live conversation and the isolated design preview. */
export function NovaShell({ children, actions, preview = false }: {
  children: ReactNode;
  actions?: ReactNode;
  preview?: boolean;
}) {
  return (
    <div className="nova-backdrop">
      <div className="nova-window">
        <header className="nova-header">
          <a href={preview ? "/redesign" : "/"} className="nova-brand" aria-label="Nova home">
            <span className="nova-logo" aria-hidden />
            <span>Nova</span>
          </a>
          <span className="nova-header-divider" aria-hidden />
          <div className="nova-assistant-name"><Medallion size={32} /><span>Nori</span></div>
          <div className="nova-header-actions">{actions}</div>
          {preview && <span className="nova-profile" aria-label="Maya, sample profile">M</span>}
        </header>
        {children}
      </div>
    </div>
  );
}
