// Synthesizes the score and sound effects, on the frame cues in SCRIPT.md.
//   node capture/score.mjs   writes public/audio/score.wav
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const T = JSON.parse(readFileSync(resolve(root, "src/timeline.json"), "utf8"));
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

// One track from the drop (450) to the ending (2550), in 60-frame bars. Three sections, same key and tempo:
// the full band, a lighter arrangement for the morning, then the full band again with a new top line.
const START = 450;
const MORNING = 14; // bar at 1290, just before the desktop becomes the phone
const RETURN = 25; // bar at 1950, on the send
const BARS = 35; // bar at 2550, the ending
const chordsA = [[57, 60, 64], [53, 57, 60], [48, 52, 55], [55, 59, 62]]; // Am F C G
const rootsA = [33, 29, 36, 31];
const chordsB = [[53, 57, 60], [48, 52, 55], [55, 59, 62], [57, 60, 64]]; // F C G Am
const rootsB = [29, 36, 31, 33];
const leadA = [76, 72, 69, 72, 77, 72, 69, 65, 79, 76, 72, 67, 79, 74, 71, 67];
const leadC = [81, 76, 72, 76, 81, 77, 72, 77, 79, 76, 72, 76, 79, 74, 71, 74];

function pad(t, notes, dur, g = 1) {
  notes.forEach((n, i) => {
    const f = midi(n);
    add(t, dur, (s, p) => (Math.sin(2 * Math.PI * f * s) + 0.25 * Math.sin(4 * Math.PI * f * s)) * Math.min(1, s * 6) * Math.min(1, (1 - p) * 6), 0.05 * g, (i - 1) * 0.35);
  });
}

function fullBar(t, chord, root, lead, b, level = 1) {
  for (let i = 0; i < 4; i++) {
    kick(t + i * beat, level);
    hat(t + (i + 0.5) * beat, level);
    hat(t + (i + 0.25) * beat, 0.35 * level, -0.3);
    hat(t + (i + 0.75) * beat, 0.35 * level, -0.3);
  }
  clap(t + beat, level);
  clap(t + beat * 3, level);
  [0, 0.75, 1.5, 2, 2.75, 3.5].forEach((o) => bass(t + o * beat, root, 0.2, level));
  [0.5, 1.5, 2.5, 3.5].forEach((o) => stab(t + o * beat, chord, 0.28, 0.85 * level));
  for (let i = 0; i < 8; i++) pluck(t + (i * beat) / 2, lead[(b * 8 + i) % 16] + (b % 8 >= 6 ? 12 : 0), 0.55 * level, i % 2 ? 0.35 : -0.35);
}

function lightBar(t, chord, root, b) {
  const night = b === 17 || b === 18; // the phone is dark and the clock runs
  if (!night) {
    kick(t, 0.5);
    kick(t + beat * 2, 0.5);
    for (let i = 0; i < 4; i++) hat(t + (i + 0.5) * beat, 0.45);
  }
  bass(t, root, beat * 1.8, 0.7);
  bass(t + beat * 2, root, beat * 1.8, 0.7);
  pad(t, chord.map((n) => n + 12), bar * 1.02, 1.1);
  const arp = [...chord, chord[1] + 12].map((n) => n + 12);
  for (let i = 0; i < 8; i++) pluck(t + (i * beat) / 2, arp[i % 4], 0.5, i % 2 ? 0.3 : -0.3);
}

for (let b = 0; b < BARS; b++) {
  const t = F(START + b * 60);
  if (b < MORNING) fullBar(t, chordsA[b % 4], rootsA[b % 4], b % 8 >= 4 ? leadC : leadA, b);
  else if (b < RETURN) lightBar(t, chordsB[(b - MORNING) % 4], rootsB[(b - MORNING) % 4], b);
  else fullBar(t, chordsA[(b - RETURN) % 4], rootsA[(b - RETURN) % 4], leadC, b);
}

/** A soft note from whatever chord is playing at frame `f`, so effects never fight the band. */
const chordTone = (f, index, up = 24) => {
  const b = Math.floor((f - START) / 60);
  const chord = b < MORNING ? chordsA[b % 4] : b < RETURN ? chordsB[(b - MORNING) % 4] : chordsA[(b - RETURN) % 4];
  return chord[index % 3] + up;
};
const soft = (f, g = 0.6) => pop(F(f), g);
function click(f, g = 1) { add(F(f), 0.025, (s) => noise() * Math.exp(-s * 240), 0.34 * g); }
function slam(f, g = 1) {
  add(F(f), 0.24, (s) => Math.sin(2 * Math.PI * (70 + 80 * Math.exp(-s * 30)) * s) * Math.exp(-s * 16), 0.75 * g);
  let lp = 0;
  add(F(f), 0.1, (s) => { lp += 0.25 * (noise() - lp); return lp * Math.exp(-s * 40); }, 0.5 * g);
}
const pat = (f) => add(F(f), 0.07, (s) => Math.sin(2 * Math.PI * 170 * s) * Math.exp(-s * 44), 0.42);
const buzz = (f) => add(F(f), 0.14, (s) => Math.sin(2 * Math.PI * 110 * s) * (Math.sin(2 * Math.PI * 28 * s) > -0.4 ? 1 : 0.1), 0.22);
function keys(from, to, step = 2.5, g = 0.8) { for (let f = from; f < to; f += step) tick(F(f), g + Math.abs(noise()) * 0.5); }
/** A low pulse on A, tightening: `from` to `to`, one note every `step` frames. */
function pulse(from, to, step, g0, g1) {
  for (let f = from; f < to; f += step) bass(F(f), 33, step / 40, g0 + (g1 - g0) * ((f - from) / (to - from)));
}

// The opening card: a quiet pulse begins under the title.
pulse(15, 75, 30, 0.3, 0.42);
pop(F(10), 0.9);
whoosh(F(66), 0.25, 0.5);

// The chores: the pulse tightens with each one.
pulse(75, 135, 15, 0.45, 0.6);
pulse(135, 195, 15, 0.6, 0.75);
pulse(195, 285, 7.5, 0.75, 0.95);
pulse(285, 360, 3.75, 0.95, 1.15);
[77, 135, 195, 285].forEach((f) => pop(F(f), 1.2));
[75, 135, 285].forEach((f) => click(f));
[139, 144, 149].forEach((f) => pop(F(f), 0.7));
keys(195, 245, 1.5, 0.7);
[293, 301, 309, 317, 325, 333].forEach((f) => { click(f, 0.8); pop(F(f), 0.5); });

// The pulse cuts dead on 360. The walls slam on three beats, two pats, then the drop on 450.
[375, 390, 405].forEach((f) => slam(f));
[413, 435].forEach(pat);
whoosh(F(450), 0.3, 0.8);
impact(F(450), 1.2);
pop(F(456)); pop(F(468), 0.7);

// The chat: keys, sends and replies, all kept under the band.
whoosh(F(540), 0.25, 0.6);
keys(543, 583, 1.4);
keys(627, 657, 1.2);
keys(663, 688, 1.2);
[585, 660, 690, 765].forEach((f) => soft(f, 0.8));
[600, 750, 825, 975, 990, 1140].forEach((f) => pop(F(f), 0.7, -1));

// Her tap, then one note per check mark from the chord underneath.
click(1065);
pluck(F(1065), chordTone(1065, 0), 0.6);
[1080, 1095, 1110, 1125, 1140].forEach((f, i) => pluck(F(f), chordTone(f, i), 0.45));

// Phone and morning: a soft tap on "Same thread", the mark's three pops, the buzz.
pluck(F(1395), chordTone(1395, 0), 0.7);
[1490, 1513, 1536].forEach((f, i) => pluck(F(f), chordTone(f, i), 0.5));
buzz(1548); buzz(1556);
click(1620);
soft(1650, 0.7);
pluck(F(1770), chordTone(1770, 0), 0.9); soft(1770, 0.8);
click(1860); whoosh(F(1872), 0.2, 0.3);

// The send brings the full band back.
click(1950);
impact(F(1950), 0.45);
soft(1953, 0.8);
whoosh(F(2037), 0.2, 0.3);
keys(2055, 2113, 1.6);
soft(2115, 0.7);

// Her computer: fly in, the app on the beat, Sam's click, fly out.
whoosh(F(2190), 0.9, 1);
impact(F(2190), 0.6);
soft(2193, 0.7);
[2265, 2280, 2295, 2310, 2325, 2340].forEach((f) => soft(f, 0.8));
click(2430); soft(2430, 0.6);
whoosh(F(2490), 0.9, 0.9);
soft(2490, 0.8);

// The ending: the band drops out, one chord rings, a second, then bells.
whoosh(F(2550), 0.32, 0.9);
kick(F(2550));
stab(F(2550), [57, 60, 64, 69], 2.4, 1.5);
bass(F(2550), 33, 2.2);
kick(F(2595), 0.7);
stab(F(2595), [53, 57, 60, 65], 2.4, 1.3);
bass(F(2595), 29, 2.2, 0.8);
stab(F(2670), [48, 55, 60, 64], 2.6, 1.5);
bass(F(2670), 36, 2.4);
kick(F(2670), 0.8);
[84, 88, 91, 96].forEach((n, i) => pluck(F(2670) + i * 0.12, n, 1.1, (i - 1.5) * 0.3));

// Sidechain-style pump and soft limit.
let peak = 0;
const fadeStart = (seconds - 1.5) * SR;
for (let i = 0; i < L.length; i++) {
  const fade = i > fadeStart ? Math.max(0, 1 - (i - fadeStart) / (1.5 * SR)) : 1;
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
