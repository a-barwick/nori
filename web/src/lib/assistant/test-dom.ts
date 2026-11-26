import { Window } from "happy-dom";
import { act, createElement, useLayoutEffect, type ReactNode } from "react";
import { createRoot } from "react-dom/client";

export function installTestDOM() {
  const win = new Window({ url: "http://localhost/" });
  const globals = {
    window: win,
    document: win.document,
    navigator: win.navigator,
    localStorage: win.localStorage,
    sessionStorage: win.sessionStorage,
    HTMLElement: win.HTMLElement,
    IS_REACT_ACT_ENVIRONMENT: true,
  };
  const previous = new Map<string, PropertyDescriptor | undefined>();
  for (const [key, value] of Object.entries(globals)) {
    previous.set(key, Object.getOwnPropertyDescriptor(globalThis, key));
    Object.defineProperty(globalThis, key, { value, configurable: true, writable: true });
  }
  return () => {
    for (const [key, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else Reflect.deleteProperty(globalThis, key);
    }
    void win.happyDOM.close();
  };
}

export function mountTestHook<T>(hook: () => T, wrap: (node: ReactNode) => ReactNode = (node) => node) {
  const target = document.createElement("div");
  document.body.append(target);
  const root = createRoot(target);
  let current!: T;
  function Probe() {
    const value = hook();
    useLayoutEffect(() => { current = value; }, [value]);
    return null;
  }
  function render() {
    void act(() => root.render(wrap(createElement(Probe))));
  }
  render();
  return {
    get current() { return current; },
    render,
    unmount() {
      void act(() => root.unmount());
      target.remove();
    },
  };
}

export async function settleTestDOM() {
  await act(async () => {});
}
