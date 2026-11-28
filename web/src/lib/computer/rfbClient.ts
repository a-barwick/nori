/**
 * Minimal RFC 6143 (RFB 3.8) WebSocket client for Nori's live desktop canvas.
 * Connects strictly through Nori Core's authenticated /v1/computer/sessions/{id}/ws proxy.
 * Supports Raw (0), CopyRect (1), and DesktopSize (-223) encodings with optional interactive input.
 */

export type RFBConnectionState = "connecting" | "connected" | "reconnecting" | "disconnected" | "error";

export interface RFBClientOptions {
  canvas: HTMLCanvasElement;
  url: string;
  viewOnly: boolean;
  onStateChange?: (state: RFBConnectionState, detail?: string) => void;
  refreshURL?: () => Promise<string>;
}

class ByteQueue {
  private buf = new Uint8Array(0);

  get length(): number {
    return this.buf.length;
  }

  push(chunk: Uint8Array) {
    if (chunk.length === 0) return;
    const next = new Uint8Array(this.buf.length + chunk.length);
    next.set(this.buf, 0);
    next.set(chunk, this.buf.length);
    this.buf = next;
  }

  peek(n: number): Uint8Array | null {
    if (this.buf.length < n) return null;
    return this.buf.subarray(0, n);
  }

  take(n: number): Uint8Array | null {
    if (this.buf.length < n) return null;
    const out = this.buf.slice(0, n);
    this.buf = this.buf.subarray(n);
    return out;
  }
}

export class NoriRFBClient {
  private canvas: HTMLCanvasElement;
  private ctx: CanvasRenderingContext2D | null;
  private url: string;
  private viewOnly: boolean;
  private onStateChange?: (state: RFBConnectionState, detail?: string) => void;
  private refreshURL?: () => Promise<string>;

  private ws: WebSocket | null = null;
  private queue = new ByteQueue();
  private stage: "version" | "security" | "security_result" | "server_init" | "normal" = "version";
  private width = 1280;
  private height = 800;
  private pendingRects = 0;
  private currentRect: { x: number; y: number; w: number; h: number; enc: number } | null = null;
  private closed = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempts = 0;
  private pointerButtons = 0;

  constructor(opts: RFBClientOptions) {
    this.canvas = opts.canvas;
    this.ctx = opts.canvas.getContext("2d");
    this.url = opts.url;
    this.viewOnly = opts.viewOnly;
    this.onStateChange = opts.onStateChange;
    this.refreshURL = opts.refreshURL;
    this.bindInput();
  }

  setViewOnly(viewOnly: boolean) {
    this.viewOnly = viewOnly;
    this.canvas.style.cursor = viewOnly ? "default" : "crosshair";
  }

  connect() {
    this.closed = false;
    this.openSocket();
  }

  disconnect() {
    this.closed = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.unbindInput();
    if (this.ws) {
      try {
        this.ws.close(1000, "closed");
      } catch {
        // ignore close errors
      }
      this.ws = null;
    }
    this.onStateChange?.("disconnected");
  }

  private openSocket() {
    if (this.closed) return;
    this.stage = "version";
    this.queue = new ByteQueue();
    this.pendingRects = 0;
    this.currentRect = null;
    this.onStateChange?.(this.reconnectAttempts > 0 ? "reconnecting" : "connecting");

    const ws = new WebSocket(this.url, ["binary"]);
    ws.binaryType = "arraybuffer";
    this.ws = ws;

    ws.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) {
        this.queue.push(new Uint8Array(event.data));
        this.processQueue();
      }
    };

    ws.onerror = () => {
      // Browser always follows onerror with onclose, which drives reconnect/disconnect cleanly
      // without flashing an error state before retries run.
    };

    ws.onclose = () => {
      if (this.closed || this.ws !== ws) return;
      this.ws = null;
      this.scheduleReconnect();
    };
  }

  private scheduleReconnect() {
    if (this.closed) return;
    if (this.reconnectAttempts < 5) {
      this.reconnectAttempts += 1;
      this.onStateChange?.("reconnecting");
      this.reconnectTimer = setTimeout(() => {
        this.reconnectTimer = null;
        void this.reconnectNow();
      }, Math.min(1000 * this.reconnectAttempts, 5000));
    } else {
      this.onStateChange?.("disconnected", "Could not connect to your computer desktop.");
    }
  }

  private async reconnectNow() {
    if (this.closed) return;
    if (this.refreshURL) {
      try {
        const nextURL = await this.refreshURL();
        if (this.closed) return;
        this.url = nextURL;
      } catch {
        if (this.closed) return;
        this.scheduleReconnect();
        return;
      }
    }
    this.openSocket();
  }

  private send(bytes: Uint8Array) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(bytes);
    }
  }

  private processQueue() {
    while (true) {
      if (this.stage === "version") {
        const header = this.queue.take(12);
        if (!header) return;
        this.send(new TextEncoder().encode("RFB 003.008\n"));
        this.stage = "security";
        continue;
      }

      if (this.stage === "security") {
        const peek = this.queue.peek(1);
        if (!peek) return;
        const count = peek[0];
        if (count === 0) {
          this.onStateChange?.("error", "Desktop handshake failed");
          return;
        }
        const types = this.queue.take(1 + count);
        if (!types) return;
        // Choose SecurityType 1 (None) — authentication is enforced by Nori Core's proxy.
        this.send(new Uint8Array([1]));
        this.stage = "security_result";
        continue;
      }

      if (this.stage === "security_result") {
        const res = this.queue.take(4);
        if (!res) return;
        const view = new DataView(res.buffer, res.byteOffset, res.byteLength);
        if (view.getUint32(0) !== 0) {
          this.onStateChange?.("error", "Desktop rejected session");
          return;
        }
        // ClientInit: shared = 1
        this.send(new Uint8Array([1]));
        this.stage = "server_init";
        continue;
      }

      if (this.stage === "server_init") {
        const peek = this.queue.peek(24);
        if (!peek) return;
        const view = new DataView(peek.buffer, peek.byteOffset, peek.byteLength);
        const nameLen = view.getUint32(20);
        const full = this.queue.take(24 + nameLen);
        if (!full) return;
        this.width = view.getUint16(0) || 1280;
        this.height = view.getUint16(2) || 800;
        this.canvas.width = this.width;
        this.canvas.height = this.height;
        this.sendSetPixelFormat();
        this.sendSetEncodings();
        this.sendFramebufferUpdateRequest(false);
        this.stage = "normal";
        this.reconnectAttempts = 0;
        this.onStateChange?.("connected");
        continue;
      }

      if (this.pendingRects > 0) {
        if (!this.processRect()) return;
        if (this.pendingRects === 0) {
          this.sendFramebufferUpdateRequest(true);
        }
        continue;
      }

      const msgHead = this.queue.peek(1);
      if (!msgHead) return;
      const msgType = msgHead[0];
      if (msgType === 0) {
        // FramebufferUpdate
        const header = this.queue.take(4);
        if (!header) return;
        const view = new DataView(header.buffer, header.byteOffset, header.byteLength);
        this.pendingRects = view.getUint16(2);
        if (this.pendingRects === 0) {
          this.sendFramebufferUpdateRequest(true);
        }
      } else if (msgType === 1) {
        // SetColourMapEntries
        const peek = this.queue.peek(6);
        if (!peek) return;
        const count = new DataView(peek.buffer, peek.byteOffset, peek.byteLength).getUint16(4);
        if (!this.queue.take(6 + count * 6)) return;
      } else if (msgType === 2) {
        // Bell
        this.queue.take(1);
      } else if (msgType === 3) {
        // ServerCutText
        const peek = this.queue.peek(8);
        if (!peek) return;
        const len = new DataView(peek.buffer, peek.byteOffset, peek.byteLength).getUint32(4);
        if (!this.queue.take(8 + len)) return;
      } else {
        // Unknown server frame; resync cleanly by closing & reconnecting
        this.ws?.close();
        return;
      }
    }
  }

  private processRect(): boolean {
    if (!this.currentRect) {
      const hdr = this.queue.take(12);
      if (!hdr) return false;
      const v = new DataView(hdr.buffer, hdr.byteOffset, hdr.byteLength);
      this.currentRect = {
        x: v.getUint16(0),
        y: v.getUint16(2),
        w: v.getUint16(4),
        h: v.getUint16(6),
        enc: v.getInt32(8),
      };
    }

    const r = this.currentRect;
    if (r.enc === -223) {
      // DesktopSize pseudo-encoding
      this.width = r.w;
      this.height = r.h;
      this.canvas.width = r.w;
      this.canvas.height = r.h;
      this.currentRect = null;
      this.pendingRects -= 1;
      return true;
    }

    if (r.enc === 1) {
      // CopyRect
      const src = this.queue.take(4);
      if (!src) return false;
      const v = new DataView(src.buffer, src.byteOffset, src.byteLength);
      const sx = v.getUint16(0);
      const sy = v.getUint16(2);
      if (this.ctx) {
        this.ctx.drawImage(this.canvas, sx, sy, r.w, r.h, r.x, r.y, r.w, r.h);
      }
      this.currentRect = null;
      this.pendingRects -= 1;
      return true;
    }

    // Raw encoding (0): 4 bytes/pixel (BGRA / little-endian 32-bit RGBX)
    const byteLen = r.w * r.h * 4;
    const pixels = this.queue.take(byteLen);
    if (!pixels) return false;
    if (this.ctx && r.w > 0 && r.h > 0) {
      const img = this.ctx.createImageData(r.w, r.h);
      const dst = img.data;
      for (let i = 0; i < byteLen; i += 4) {
        dst[i] = pixels[i + 2];     // R
        dst[i + 1] = pixels[i + 1]; // G
        dst[i + 2] = pixels[i];     // B
        dst[i + 3] = 255;           // A
      }
      this.ctx.putImageData(img, r.x, r.y);
    }
    this.currentRect = null;
    this.pendingRects -= 1;
    return true;
  }

  private sendSetPixelFormat() {
    // 32-bit true-color little-endian BGRX
    const buf = new Uint8Array(20);
    const v = new DataView(buf.buffer);
    buf[0] = 0;
    buf[4] = 32; // bits-per-pixel
    buf[5] = 24; // depth
    buf[6] = 0;  // big-endian = false
    buf[7] = 1;  // true-color = true
    v.setUint16(8, 255);  // red-max
    v.setUint16(10, 255); // green-max
    v.setUint16(12, 255); // blue-max
    buf[14] = 16; // red-shift
    buf[15] = 8;  // green-shift
    buf[16] = 0;  // blue-shift
    this.send(buf);
  }

  private sendSetEncodings() {
    const encodings = [1, 0, -223]; // CopyRect, Raw, DesktopSize
    const buf = new Uint8Array(4 + encodings.length * 4);
    const v = new DataView(buf.buffer);
    buf[0] = 2;
    v.setUint16(2, encodings.length);
    encodings.forEach((enc, idx) => v.setInt32(4 + idx * 4, enc));
    this.send(buf);
  }

  private sendFramebufferUpdateRequest(incremental: boolean) {
    const buf = new Uint8Array(10);
    const v = new DataView(buf.buffer);
    buf[0] = 3;
    buf[1] = incremental ? 1 : 0;
    v.setUint16(2, 0);
    v.setUint16(4, 0);
    v.setUint16(6, this.width);
    v.setUint16(8, this.height);
    this.send(buf);
  }

  private sendPointer(x: number, y: number, mask: number) {
    if (this.viewOnly || this.stage !== "normal") return;
    const buf = new Uint8Array(6);
    const v = new DataView(buf.buffer);
    buf[0] = 5;
    buf[1] = mask & 0xff;
    v.setUint16(2, Math.max(0, Math.min(this.width - 1, Math.round(x))));
    v.setUint16(4, Math.max(0, Math.min(this.height - 1, Math.round(y))));
    this.send(buf);
  }

  private sendKey(keysym: number, down: boolean) {
    if (this.viewOnly || this.stage !== "normal" || !keysym) return;
    const buf = new Uint8Array(8);
    const v = new DataView(buf.buffer);
    buf[0] = 4;
    buf[1] = down ? 1 : 0;
    v.setUint32(4, keysym >>> 0);
    this.send(buf);
  }

  private canvasCoords(e: PointerEvent | WheelEvent): { x: number; y: number } {
    const rect = this.canvas.getBoundingClientRect();
    if (!rect.width || !rect.height) return { x: 0, y: 0 };
    return {
      x: ((e.clientX - rect.left) / rect.width) * this.width,
      y: ((e.clientY - rect.top) / rect.height) * this.height,
    };
  }

  private onPointerMove = (e: PointerEvent) => {
    if (this.viewOnly) return;
    const { x, y } = this.canvasCoords(e);
    this.sendPointer(x, y, this.pointerButtons);
  };

  private onPointerDown = (e: PointerEvent) => {
    if (this.viewOnly) return;
    this.canvas.focus();
    this.pointerButtons |= 1 << (e.button === 2 ? 2 : e.button === 1 ? 1 : 0);
    const { x, y } = this.canvasCoords(e);
    this.sendPointer(x, y, this.pointerButtons);
  };

  private onPointerUp = (e: PointerEvent) => {
    if (this.viewOnly) return;
    this.pointerButtons &= ~(1 << (e.button === 2 ? 2 : e.button === 1 ? 1 : 0));
    const { x, y } = this.canvasCoords(e);
    this.sendPointer(x, y, this.pointerButtons);
  };

  private onWheel = (e: WheelEvent) => {
    if (this.viewOnly) return;
    e.preventDefault();
    const { x, y } = this.canvasCoords(e);
    const bit = e.deltaY < 0 ? 1 << 3 : 1 << 4;
    this.sendPointer(x, y, this.pointerButtons | bit);
    this.sendPointer(x, y, this.pointerButtons);
  };

  private onKeyDown = (e: KeyboardEvent) => {
    if (this.viewOnly) return;
    const keysym = domKeyToX11Keysym(e.key);
    if (!keysym) return;
    e.preventDefault();
    this.sendKey(keysym, true);
  };

  private onKeyUp = (e: KeyboardEvent) => {
    if (this.viewOnly) return;
    const keysym = domKeyToX11Keysym(e.key);
    if (!keysym) return;
    e.preventDefault();
    this.sendKey(keysym, false);
  };

  private onContextMenu = (e: MouseEvent) => {
    if (!this.viewOnly) e.preventDefault();
  };

  private bindInput() {
    this.canvas.tabIndex = 0;
    this.canvas.addEventListener("pointermove", this.onPointerMove);
    this.canvas.addEventListener("pointerdown", this.onPointerDown);
    this.canvas.addEventListener("pointerup", this.onPointerUp);
    this.canvas.addEventListener("wheel", this.onWheel, { passive: false });
    this.canvas.addEventListener("keydown", this.onKeyDown);
    this.canvas.addEventListener("keyup", this.onKeyUp);
    this.canvas.addEventListener("contextmenu", this.onContextMenu);
  }

  private unbindInput() {
    this.canvas.removeEventListener("pointermove", this.onPointerMove);
    this.canvas.removeEventListener("pointerdown", this.onPointerDown);
    this.canvas.removeEventListener("pointerup", this.onPointerUp);
    this.canvas.removeEventListener("wheel", this.onWheel);
    this.canvas.removeEventListener("keydown", this.onKeyDown);
    this.canvas.removeEventListener("keyup", this.onKeyUp);
    this.canvas.removeEventListener("contextmenu", this.onContextMenu);
  }
}

const SPECIAL_KEYSYMS: Record<string, number> = {
  Backspace: 0xff08,
  Tab: 0xff09,
  Enter: 0xff0d,
  Escape: 0xff1b,
  Insert: 0xff63,
  Delete: 0xffff,
  Home: 0xff50,
  End: 0xff57,
  PageUp: 0xff55,
  PageDown: 0xff56,
  ArrowLeft: 0xff51,
  ArrowUp: 0xff52,
  ArrowRight: 0xff53,
  ArrowDown: 0xff54,
  Shift: 0xffe1,
  Control: 0xffe3,
  Alt: 0xffe9,
  Meta: 0xffe7,
};

function domKeyToX11Keysym(key: string): number {
  if (SPECIAL_KEYSYMS[key]) return SPECIAL_KEYSYMS[key];
  if (key.length === 1) {
    const code = key.codePointAt(0) ?? 0;
    if (code >= 0x20 && code <= 0x7e) return code;
    if (code >= 0xa0 && code <= 0xff) return code;
    if (code > 0xff) return 0x01000000 | code;
  }
  return 0;
}
