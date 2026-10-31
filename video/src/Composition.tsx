import { Audio } from "@remotion/media";
import { AbsoluteFill, Composition, Sequence, staticFile } from "remotion";
import { ColdOpen, Demo, End, Meet, Phone, Stat, Trust } from "./scenes";
import timeline from "./timeline.json";

const { scenes } = timeline;

const NoriFilm: React.FC = () => (
  <AbsoluteFill style={{ background: "#fff" }}>
    <Sequence name="Cold open" from={scenes.coldOpen.from} durationInFrames={scenes.coldOpen.frames}><ColdOpen /></Sequence>
    <Sequence name="70%" from={scenes.stat.from} durationInFrames={scenes.stat.frames}><Stat /></Sequence>
    <Sequence name="Meet Nori" from={scenes.meet.from} durationInFrames={scenes.meet.frames}><Meet /></Sequence>
    <Sequence name="Real product" from={scenes.demo.from} durationInFrames={scenes.demo.frames}><Demo /></Sequence>
    <Sequence name="Phone" from={scenes.phone.from} durationInFrames={scenes.phone.frames}><Phone /></Sequence>
    <Sequence name="Trust" from={scenes.trust.from} durationInFrames={scenes.trust.frames}><Trust /></Sequence>
    <Sequence name="End" from={scenes.end.from} durationInFrames={scenes.end.frames}><End /></Sequence>
    <Audio src={staticFile("audio/score.wav")} />
  </AbsoluteFill>
);

export const MyComposition = () => (
  <Composition id="NoriFilm" component={NoriFilm} durationInFrames={timeline.total} fps={timeline.fps} width={1920} height={1080} />
);
