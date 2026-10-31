import { Audio } from "@remotion/media";
import { AbsoluteFill, Composition, Freeze, Sequence, staticFile, useCurrentFrame } from "remotion";
import { Intro, Scene1And2 } from "./opening";
import { CardFlyOff, End, Stage } from "./scenes";
import timeline from "./timeline.json";

const { scenes } = timeline;

const StageRange: React.FC = () => {
  const frame = useCurrentFrame();
  return frame >= scenes.ask.from && frame < scenes.end.from ? <Stage /> : null;
};

const NoriFilm: React.FC = () => (
  <AbsoluteFill style={{ background: "#fff" }}>
    <Sequence name="Waiting chat app" durationInFrames={scenes.problem.from} layout="none"><Freeze frame={0}><Scene1And2 /></Freeze></Sequence>
    <Sequence name="Problem and Meet" from={scenes.problem.from} durationInFrames={scenes.ask.from - scenes.problem.from + 20} layout="none"><Scene1And2 /></Sequence>
    <Sequence name="Intro" durationInFrames={scenes.intro.frames}><Intro /></Sequence>
    <Sequence name="End" from={scenes.end.from} durationInFrames={scenes.end.frames}><End /></Sequence>
    <StageRange />
    <CardFlyOff />
    <Audio src={staticFile("audio/score.wav")} />
  </AbsoluteFill>
);

export const MyComposition = () => (
  <Composition id="NoriFilm" component={NoriFilm} durationInFrames={timeline.total} fps={timeline.fps} width={1920} height={1080} />
);
