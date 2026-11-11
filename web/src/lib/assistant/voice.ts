"use client";

import { useCallback, useEffect, useRef, useState } from "react";

type Recognition = {
  lang: string; continuous: boolean; interimResults: boolean;
  onresult: ((event: { resultIndex: number; results: ArrayLike<{ isFinal: boolean; 0: { transcript: string } }> }) => void) | null;
  onerror: ((event: { error: string }) => void) | null;
  onend: (() => void) | null;
  start: () => void; stop: () => void; abort: () => void;
};
type RecognitionConstructor = new () => Recognition;

function recognitionClass(): RecognitionConstructor | undefined {
  if (typeof window === "undefined") return undefined;
  const w = window as unknown as { SpeechRecognition?: RecognitionConstructor; webkitSpeechRecognition?: RecognitionConstructor };
  return w.SpeechRecognition ?? w.webkitSpeechRecognition;
}

/**
 * Browser dictation. Text lands in the draft for the user to read and edit before
 * sending; nothing is sent from speech alone.
 */
export function useDictation(onText: (text: string) => void, language?: string) {
  const [listening, setListening] = useState(false);
  const [interim, setInterim] = useState("");
  const [error, setError] = useState("");
  const recognition = useRef<Recognition | null>(null);
  const supported = !!recognitionClass();
  const stop = useCallback(() => { recognition.current?.stop(); }, []);
  const start = useCallback(() => {
    const Class = recognitionClass();
    if (!Class) { setError("This browser can’t take dictation."); return; }
    window.speechSynthesis?.cancel();
    const instance = new Class();
    instance.lang = language || navigator.language || "en-US";
    instance.continuous = true;
    instance.interimResults = true;
    instance.onresult = (event) => {
      let pending = "";
      for (let index = event.resultIndex; index < event.results.length; index++) {
        const result = event.results[index];
        if (result.isFinal) onText(result[0].transcript.trim());
        else pending += result[0].transcript;
      }
      setInterim(pending);
    };
    instance.onerror = (event) => setError(event.error === "not-allowed" ? "Microphone access is off for this site." : event.error === "no-speech" ? "" : "Dictation stopped.");
    instance.onend = () => { setListening(false); setInterim(""); recognition.current = null; };
    recognition.current = instance;
    setError("");
    setListening(true);
    instance.start();
  }, [language, onText]);
  useEffect(() => () => recognition.current?.abort(), []);
  return { supported, listening, interim, error, start, stop };
}

/** Reads new Nori replies aloud while on. Starting dictation or pressing stop interrupts it; neither cancels a task. */
export function useSpeech() {
  const [on, setOn] = useState(false);
  const [speaking, setSpeaking] = useState(false);
  const supported = typeof window !== "undefined" && "speechSynthesis" in window;
  const say = useCallback((text: string) => {
    if (!supported || !on || !text.trim()) return;
    const utterance = new SpeechSynthesisUtterance(text.replace(/[#*_`>|[\]]/g, "").slice(0, 2000));
    utterance.onstart = () => setSpeaking(true);
    utterance.onend = () => setSpeaking(false);
    utterance.onerror = () => setSpeaking(false);
    window.speechSynthesis.cancel();
    window.speechSynthesis.speak(utterance);
  }, [on, supported]);
  const interrupt = useCallback(() => { if (supported) window.speechSynthesis.cancel(); setSpeaking(false); }, [supported]);
  useEffect(() => () => { if (supported) window.speechSynthesis.cancel(); }, [supported]);
  return { supported, on, setOn: (value: boolean) => { if (!value) interrupt(); setOn(value); }, speaking, say, interrupt };
}
