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

// A minor, F, C, G: bright enough to feel like a product film, not a thriller.
const chords = [[57, 60, 64], [53, 57, 60], [48, 52, 55], [55, 59, 62]];
const roots = [33, 29, 36, 31];
const lead = [76, 72, 69, 72, 77, 72, 69, 65, 79, 76, 72, 67, 79, 74, 71, 67];

/** One bar of the full band, from film frame `from`, up to (not including) frame `to`. */
function band(from, to, level = 1) {
  for (let f = from; f < to; f += 60) {
    const b = Math.floor(f / 60);
    const t = F(f);
    const chord = chords[b % 4];
    const root = roots[b % 4];
    const has = (o) => f + o * 15 < to;
    for (let i = 0; i < 4; i++) {
      if (!has(i)) continue;
      kick(t + i * beat, level);
      hat(t + (i + 0.5) * beat, level);
      hat(t + (i + 0.25) * beat, 0.35 * level, -0.3);
      hat(t + (i + 0.75) * beat, 0.35 * level, -0.3);
    }
    if (has(1)) clap(t + beat, level);
    if (has(3)) clap(t + beat * 3, level);
    [0, 0.75, 1.5, 2, 2.75, 3.5].forEach((o) => { if (has(o)) bass(t + o * beat, root, 0.2, level); });
    [0.5, 1.5, 2.5, 3.5].forEach((o) => { if (has(o)) stab(t + o * beat, chord, 0.28, 0.85 * level); });
    for (let i = 0; i < 8; i++) if (has(i / 2)) pluck(t + (i * beat) / 2, lead[(b * 8 + i) % 16] + (b % 8 >= 6 ? 12 : 0), 0.55 * level, i % 2 ? 0.35 : -0.35);
  }
}
/** A low pulse on A, tightening: `from` to `to`, one note every `step` frames. */
function pulse(from, to, step, g0, g1) {
  for (let f = from; f < to; f += step) bass(F(f), 33, step / 40, g0 + (g1 - g0) * ((f - from) / (to - from)));
}
/** One sustained, soft note with slow ends. */
function held(from, to, n, g = 1) {
  const f0 = midi(n);
  add(F(from), F(to - from), (s, p) => (Math.sin(2 * Math.PI * f0 * s) + 0.3 * Math.sin(4 * Math.PI * f0 * s) + 0.12 * Math.sin(6.01 * Math.PI * f0 * s)) * Math.min(1, p * 8) * Math.min(1, (1 - p) * 3), 0.16 * g);
}
function rise(from, to, g = 1) {
  add(F(from), F(to - from), (s, p) => Math.sin(2 * Math.PI * (midi(57) + midi(69) * p * 0.5) * s) * p * p * Math.min(1, (1 - p) * 20), 0.1 * g);
}
function click(f, g = 1) { add(F(f), 0.025, (s) => noise() * Math.exp(-s * 240), 0.34 * g); }
function squish(f, g = 1) {
  let lp = 0;
  add(F(f), 0.22, (s, p) => { lp += 0.12 * (noise() - lp); return (lp * 1.6 + Math.sin(2 * Math.PI * (220 - 340 * p) * s) * 0.35) * Math.sin(Math.PI * p); }, 0.6 * g);
}
const pat = (f) => add(F(f), 0.07, (s) => Math.sin(2 * Math.PI * 170 * s) * Math.exp(-s * 44), 0.42);
const buzz = (f) => add(F(f), 0.17, (s) => Math.sin(2 * Math.PI * 92 * s) * (Math.sin(2 * Math.PI * 28 * s) > -0.4 ? 1 : 0.1), 0.5);
function keys(from, to, step = 2.5, g = 0.8) { for (let f = from; f < to; f += step) tick(F(f), g + Math.abs(noise()) * 0.5); }

const S = T.scenes;

// Scene 1: the cold open's tense pulse, tightening with each chore.
pulse(0, 60, 15, 0.45, 0.6);
pulse(60, 120, 15, 0.6, 0.75);
pulse(120, 210, 7.5, 0.75, 0.95);
pulse(210, 285, 3.75, 0.95, 1.15);
[2, 60, 120, 210].forEach((f) => pop(F(f), 1.2));
[0, 60, 210].forEach((f) => click(f));
[64, 69, 74].forEach((f) => pop(F(f), 0.7));
keys(120, 170, 1.5, 0.7);
[218, 226, 234, 242, 250, 258].forEach((f) => { click(f, 0.8); pop(F(f), 0.5); });

// Scene 2: the music cuts dead on 285. Squish, pats, then the drop on 375.
squish(324);
[330, 345, 360].forEach(pat);
whoosh(F(375), 0.3, 0.8);
impact(F(375), 1.2);
band(375, 480);
pop(F(381)); pop(F(393), 0.7);

// Scene 3: the band falls to a pulse while she asks, and returns with the reply.
whoosh(F(S.ask.from), 0.25, 0.8);
band(480, 525, 0.7);
keys(483, 523, 1.4);
pop(F(525), 1.1);
pulse(525, 780, 15, 0.5, 0.7);
pop(F(570), 1.2); pop(F(690), 1.2);
band(780, 1440);
pop(F(780), 1.1, -1);
pop(F(795), 1.1);

// Scene 4: a pop on every reply and card, and her tap.
[885, 1020, 1275].forEach((f) => pop(F(f), 1, -1));
pop(F(1032), 0.9);
click(1200); pop(F(1200), 1.2);
[1215, 1230, 1245, 1260, 1275].forEach((f, i) => pluck(F(f), 76 + i * 2, 0.8));

// Scene 5: one held note and a single soft tap at 1530.
stab(F(1440), [53, 57, 60], 1.4, 0.8);
held(1440, 1590, 69, 1.2);
add(F(1530), 0.09, (s) => Math.sin(2 * Math.PI * 660 * s) * Math.exp(-s * 34), 0.2);

// Scene 6: near silence and a rising tone with the mark's three pops, the buzz, a light pulse, then the hit.
rise(1590, 1680, 0.8);
[1625, 1648, 1671].forEach((f) => pluck(F(f), 84, 0.5));
buzz(1683); buzz(1691);
click(1755);
pulse(1770, 2145, 15, 0.4, 0.6);
for (let f = 1770; f < 2145; f += 30) kick(F(f), 0.32);
pop(F(1770), 0.7, -1); pop(F(1950), 0.7, -1); pop(F(1995), 0.8);
click(2070); pop(F(2070), 0.9);
pop(F(2115), 1.2);
impact(F(2145), 0.75);
pop(F(2148), 0.9);
pulse(2220, 2310, 30, 0.35, 0.45);
click(2220); whoosh(F(2234), 0.2, 0.4);
impact(F(2310), 0.7);
click(2310);
pop(F(2313), 1);
band(2310, 3075);
whoosh(F(2354), 0.2, 0.35);
keys(2385, 2443, 1.6);
pop(F(2445), 0.8);

// Scene 7: fly in, the app on the beat, Sam's click, fly out.
whoosh(F(2520), 0.9, 1.3);
impact(F(2520), 0.9);
pop(F(2523), 0.9);
[2610, 2625, 2640, 2655, 2670, 2685].forEach((f) => pop(F(f), 1.1));
pop(F(2613), 0.6);
click(2775, 1.1); pop(F(2775), 0.8);
whoosh(F(2835), 0.9, 1.2);
pop(F(2835), 1.1);

// Scene 8: a whoosh and three pops, one every two beats.
whoosh(F(2910), 0.28);
[0, 30, 60].forEach((o) => pop(F(2910 + o), 1.3));

// Scene 9: the band drops out, one chord rings, a second, then bells.
whoosh(F(3075), 0.32, 0.9);
kick(F(3075));
stab(F(3075), [57, 60, 64, 69], 2.4, 1.5);
bass(F(3075), 33, 2.2);
kick(F(3120), 0.7);
stab(F(3120), [53, 57, 60, 65], 2.4, 1.3);
bass(F(3120), 29, 2.2, 0.8);
stab(F(3195), [48, 55, 60, 64], 3, 1.5);
bass(F(3195), 36, 2.8);
kick(F(3195), 0.8);
[84, 88, 91, 96].forEach((n, i) => pluck(F(3195) + i * 0.12, n, 1.1, (i - 1.5) * 0.3));

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
