import { Audio } from "@remotion/media";
import { AbsoluteFill, Composition, Sequence, staticFile, useCurrentFrame } from "remotion";
import { Scene1And2 } from "./opening";
import { CardFlyOff, End, Stage, Trust } from "./scenes";
import timeline from "./timeline.json";

const { scenes } = timeline;

const StageRange: React.FC = () => {
  const frame = useCurrentFrame();
  return frame >= scenes.ask.from && frame < scenes.trust.from ? <Stage /> : null;
};

const NoriFilm: React.FC = () => (
  <AbsoluteFill style={{ background: "#fff" }}>
    <Sequence name="Problem and Meet" durationInFrames={320} layout="none"><Scene1And2 /></Sequence>
    <Sequence name="Trust" from={scenes.trust.from} durationInFrames={scenes.trust.frames}><Trust /></Sequence>
    <Sequence name="End" from={scenes.end.from} durationInFrames={scenes.end.frames}><End /></Sequence>
    <StageRange />
    <CardFlyOff />
    <Audio src={staticFile("audio/score.wav")} />
  </AbsoluteFill>
);

export const MyComposition = () => (
  <Composition id="NoriFilm" component={NoriFilm} durationInFrames={timeline.total} fps={timeline.fps} width={1920} height={1080} />
);
