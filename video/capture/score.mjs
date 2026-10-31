// Synthesizes the score and sound effects, locked to src/timeline.json.
//   node capture/score.mjs   writes public/audio/score.wav
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const T = JSON.parse(readFileSync(resolve(root, "src/timeline.json"), "utf8"));
const markers = JSON.parse(readFileSync(resolve(root, "public/clips/markers.json"), "utf8")).desktop.markers;
const SR = 44100;
const seconds = T.total / T.fps;
const L = new Float32Array(Math.ceil(SR * (seconds + 0.5)));
const R = new Float32Array(L.length);
const beat = 60 / T.bpm;
const bar = beat * 4;
const F = (f) => f / T.fps;
const midi = (n) => 440 * 2 ** ((n - 69) / 12);

let seed = 7;
const noise = () => ((seed = (seed * 1664525 + 1013904223) >>> 0) / 2 ** 31) - 1;

function add(t0, dur, fn, gain = 1, pan = 0) {
  const start = Math.round(t0 * SR);
  const n = Math.round(dur * SR);
  const gl = gain * Math.cos(((pan + 1) * Math.PI) / 4);
  const gr = gain * Math.sin(((pan + 1) * Math.PI) / 4);
  for (let i = 0; i < n && start + i < L.length; i++) {
    if (start + i < 0) continue;
    const v = fn(i / SR, i / n);
    L[start + i] += v * gl;
    R[start + i] += v * gr;
  }
}

const kick = (t, g = 1) => add(t, 0.26, (s) => Math.sin(2 * Math.PI * (48 * s + 9 * (1 - Math.exp(-s * 34)))) * Math.exp(-s * 13), 0.95 * g);
const clap = (t, g = 1) => add(t, 0.16, (s) => noise() * (Math.exp(-s * 26) + 0.5 * Math.exp(-((s - 0.018) ** 2) * 9e4)), 0.3 * g);
const hat = (t, g = 1, pan = 0.2) => { let last = 0; add(t, 0.05, (s) => { const n = noise(); const v = n - last; last = n; return v * Math.exp(-s * 95); }, 0.13 * g, pan); };
const saw = (ph) => 2 * (ph - Math.floor(ph + 0.5));

function bass(t, n, dur, g = 1) {
  const f = midi(n);
  add(t, dur, (s, p) => (Math.sin(2 * Math.PI * f * s) * 0.8 + saw(f * s) * 0.2) * Math.min(1, s * 200) * Math.min(1, (1 - p) * 30), 0.36 * g);
}
function stab(t, notes, dur = 0.2, g = 1) {
  notes.forEach((n, i) => {
    const f = midi(n);
    let lp = 0;
    add(t, dur, (s) => {
      const cut = 0.05 + 0.3 * Math.exp(-s * 16);
      lp += cut * ((saw(f * s) + saw(f * 1.006 * s)) * 0.5 - lp);
      return lp * Math.exp(-s * 7);
    }, 0.1 * g, (i - 1) * 0.3);
  });
}
function pluck(t, n, g = 1, pan = 0) {
  const f = midi(n);
  add(t, 0.42, (s) => (Math.sin(2 * Math.PI * f * s) + 0.35 * Math.sin(4 * Math.PI * f * s) * Math.exp(-s * 24)) * Math.exp(-s * 9) * Math.min(1, s * 900), 0.15 * g, pan);
}
function whoosh(t, dur = 0.45, g = 1) {
  let lp = 0;
  add(t - dur, dur + 0.12, (s, p) => {
    const a = p < 0.8 ? (p / 0.8) ** 2.4 : (1 - p) / 0.2;
    lp += (0.02 + 0.45 * p) * (noise() - lp);
    return lp * a;
  }, 0.5 * g);
}
function pop(t, g = 1, up = 1) {
  add(t, 0.09, (s) => Math.sin(2 * Math.PI * (up > 0 ? 420 + 1500 * s * 6 : 980 - 2600 * s) * s) * Math.exp(-s * 46), 0.3 * g);
}
const tick = (t, g = 1, pan = -0.15) => add(t, 0.018, (s) => noise() * Math.exp(-s * 340), 0.11 * g, pan);
function impact(t, g = 1) {
  add(t, 1.1, (s) => Math.sin(2 * Math.PI * (38 + 40 * Math.exp(-s * 20)) * s) * Math.exp(-s * 4.2), 0.8 * g);
  let lp = 0;
  add(t, 0.5, (s) => { lp += 0.2 * (noise() - lp); return lp * Math.exp(-s * 9); }, 0.45 * g);
}

// A minor, F, C, G: bright enough to feel like a product film, not a thriller.
const chords = [[57, 60, 64], [53, 57, 60], [48, 52, 55], [55, 59, 62]];
const roots = [33, 29, 36, 31];
const lead = [76, 72, 69, 72, 77, 72, 69, 65, 79, 76, 72, 67, 79, 74, 71, 67];
const barsTotal = Math.ceil(T.total / (T.framesPerBeat * 4));

// Section index by bar: 0-1 cold open, 2-3 stat, 4+ the drop, ends on a held chord.
const endBar = T.scenes.end.from / 60;
for (let b = 0; b < barsTotal; b++) {
  const t = b * bar;
  const chord = chords[b % 4];
  const root = roots[b % 4];
  if (b < 2) {
    // Tense pulse under the window pile-up.
    for (let i = 0; i < 8; i++) bass(t + (i * beat) / 2, 33, 0.14, 0.45 + b * 0.25);
  } else if (b < 4) {
    for (let i = 0; i < 4; i++) { kick(t + i * beat, 0.75); hat(t + (i + 0.5) * beat, 0.7); }
    for (let i = 0; i < 8; i++) bass(t + (i * beat) / 2, root, 0.18, 0.7);
    stab(t + beat * 1.5, chord, 0.3, 0.7);
    stab(t + beat * 3, chord, 0.3, 0.7);
  } else if (b < endBar) {
    const light = b >= T.scenes.trust.from / 60 ? 1.1 : 1;
    for (let i = 0; i < 4; i++) {
      kick(t + i * beat);
      hat(t + (i + 0.5) * beat);
      hat(t + (i + 0.25) * beat, 0.35, -0.3);
      hat(t + (i + 0.75) * beat, 0.35, -0.3);
    }
    clap(t + beat);
    clap(t + beat * 3);
    [0, 0.75, 1.5, 2, 2.75, 3.5].forEach((o) => bass(t + o * beat, root, 0.2));
    [0.5, 1.5, 2.5, 3.5].forEach((o) => stab(t + o * beat, chord, 0.28, 0.85 * light));
    for (let i = 0; i < 8; i++) pluck(t + (i * beat) / 2, lead[(b * 8 + i) % 16] + (b % 8 >= 6 ? 12 : 0), 0.55, i % 2 ? 0.35 : -0.35);
  } else {
    // End card: the band drops out, one chord rings, the mark gets a bell.
    if (b === endBar) {
      kick(t);
      stab(t, [57, 60, 64, 69], 2.4, 1.5);
      bass(t, 33, 2.2);
      kick(t + beat * 2, 0.7);
      stab(t + beat * 2, [53, 57, 60, 65], 2.2, 1.3);
    } else if (b === endBar + 1) {
      stab(t, [48, 55, 60, 64], 3, 1.5);
      bass(t, 36, 2.8);
      kick(t, 0.8);
      [84, 88, 91, 96].forEach((n, i) => pluck(t + i * 0.12, n, 1.1, (i - 1.5) * 0.3));
    }
  }
}

// Effects locked to picture.
const S = T.scenes;
[0, 15, 30, 45, 52, 60, 67, 75].forEach((f) => pop(F(f), 1.2));
impact(F(90), 0.8);
whoosh(F(S.stat.from), 0.35);
tick(F(S.stat.from + 34), 3);
whoosh(F(S.meet.from + 9), 0.5, 1.2);
impact(F(S.meet.from + 9), 1.1);
whoosh(F(S.demo.from), 0.3);
whoosh(F(S.phone.from), 0.3);
pop(F(S.phone.from + 8));
whoosh(F(S.trust.from), 0.28);
[0, 15, 30, 45].forEach((o) => pop(F(S.trust.from + o), 1.3));
whoosh(F(S.end.from), 0.32, 0.9);

// Demo sounds: map moments in the filmed clip to the edit.
let segFrom = S.demo.from;
const toEdit = (src) => {
  let from = S.demo.from;
  for (const seg of T.demo) {
    const [a, b] = seg.src;
    if (src >= a && src < b) return F(from + ((src - a) / (b - a)) * seg.frames);
    from += seg.frames;
  }
  return null;
};
for (const seg of T.demo) {
  pop(F(segFrom + 3), 0.8);
  segFrom += seg.frames;
}
const typing = [[markers.type1, markers.sent1], [markers.type2, markers.sent2 - 1.1], [markers.type3, markers.reply3 - 1], [markers.type4, markers["two-tasks"] - 2.8]];
for (const [a, b] of typing) {
  for (let s = a; s < b; s += 0.075) { const t = toEdit(s); if (t !== null) tick(t, 0.7 + Math.abs(noise()) * 0.5); }
  const sent = toEdit(b);
  if (sent !== null) pop(sent, 1.1);
}
for (const s of [markers.reply1, markers.sent2, markers.reply3, markers.brief, markers.document]) { const t = toEdit(s - 0.02); if (t !== null) pop(t, 0.8, -1); }

// Sidechain-style pump and soft limit.
let peak = 0;
const fadeStart = (seconds - 2.2) * SR;
for (let i = 0; i < L.length; i++) {
  const fade = i > fadeStart ? Math.max(0, 1 - (i - fadeStart) / (2.2 * SR)) : 1;
  L[i] = Math.tanh(L[i] * 1.15) * fade;
  R[i] = Math.tanh(R[i] * 1.15) * fade;
  peak = Math.max(peak, Math.abs(L[i]), Math.abs(R[i]));
}
const gain = 0.92 / peak;
const pcm = Buffer.alloc(L.length * 4 + 44);
pcm.write("RIFF", 0); pcm.writeUInt32LE(36 + L.length * 4, 4); pcm.write("WAVEfmt ", 8); pcm.writeUInt32LE(16, 16);
pcm.writeUInt16LE(1, 20); pcm.writeUInt16LE(2, 22); pcm.writeUInt32LE(SR, 24); pcm.writeUInt32LE(SR * 4, 28); pcm.writeUInt16LE(4, 32); pcm.writeUInt16LE(16, 34);
pcm.write("data", 36); pcm.writeUInt32LE(L.length * 4, 40);
for (let i = 0; i < L.length; i++) {
  pcm.writeInt16LE(Math.round(L[i] * gain * 32767), 44 + i * 4);
  pcm.writeInt16LE(Math.round(R[i] * gain * 32767), 46 + i * 4);
}
mkdirSync(resolve(root, "public/audio"), { recursive: true });
writeFileSync(resolve(root, "public/audio/score.wav"), pcm);
console.log("score.wav", seconds, "s, peak", peak.toFixed(2));
