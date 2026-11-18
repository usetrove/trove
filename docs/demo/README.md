# Trove demo video

A short product video built with [Remotion](https://www.remotion.dev). Everything on screen is data in `src/script.ts`,
and the text the terminal shows is Trove's real output: the report renderer's golden files, the `trove autopilot`
consent and status text, the desktop notice from `internal/autopilot/notify.go`, and `trove fix`. Keep it that way:
when a command's wording changes in Go, change it here too.

## Scenes

| Scene | File | What it shows |
|---|---|---|
| Hook | `src/scenes/HookScene.tsx` | The headline, word by word |
| Install | `src/scenes/TerminalScene.tsx` (`partInstall`) | `brew install`, then `trove` and the report's headline |
| Report | `src/scenes/ReportScene.tsx` | The report's numbers as a graphic: three tiles and the leaks as bars |
| Consent | `src/scenes/TerminalScene.tsx` (`partConsent`) | `trove autopilot on` with the consent prompt; it starts in shadow |
| Time skip | `src/scenes/TimeSkipScene.tsx` | A counter up to the ten shadow sessions the v1 policy needs |
| Status | `src/scenes/TerminalScene.tsx` (`partStatus`) | The notification, then `trove autopilot` with one optimizer on |
| Lifecycle | `src/scenes/LifecycleScene.tsx` | shadow, candidate, on as cards; the optimizer moves along; the guardrail back |
| Status line | `src/scenes/StatusScene.tsx` | The status line inside an agent, with the `autopilot 1 on` suffix |
| Title | `src/scenes/TitleScene.tsx` | Logo, install command, repository |

`TroveDemo` is the landscape cut (1920x1080) and `TroveDemoVertical` the portrait one (1080x1920). Both use the
same scenes; `src/stage.ts` holds the geometry for each orientation. Every scene also has its own composition
(`Report`, `ReportVertical`, ...) for previewing one at a time.

## Music

`public/music/sub-clair-calm.mp3` is the first 61 seconds of "Calm" by Sub Clair, supplied by the project owner
as `sub_clair-calm-585426.mp3`. The file carries no embedded tags, so confirm its source and license terms,
and add whatever credit they require to `public/music/LICENSE.txt`, before publishing the video. The level and
fades are in `MUSIC` in `src/TroveDemo.tsx`.

The bed should be a lazy lo-fi groove around 70 to 90 bpm, stay even for the whole minute, start within a
second or two (the demo fades it in anyway), and carry little top end so it never fights the captions. To swap
in another track, drop the file under `public/music/`, change `MUSIC.file`, and update the credit above. Match
the level to the track's loudness: "Calm" measures -10.5 LUFS over the cut and plays at 0.3.

## Commands

```console
npm i --loglevel=error          # install
npm run dev                     # preview in Remotion Studio
npm run lint                    # eslint and tsc
npx remotion render TroveDemo out/trove-demo.mp4
npx remotion render TroveDemoVertical out/trove-demo-vertical.mp4
npx remotion still Report --frame=170 out/still.png      # one frame of one scene
```

## Conventions

- The brand accent is gold (`colors.gold` on dark, `colors.inkGold` on paper): caption accent words, chips for
  things Trove does, the hook's second line. Green, amber and red are status colours only (on, estimated, over)
  and are never used for branding; an "estimated" chip is neutral grey.
- Captions put accent-coloured words between asterisks: `Say *yes once*.`
- A `clear` line in the script wipes the terminal, so each command's output starts at the top.
- A highlight's label is placed in pixels from the mono advance (`MONO_ADVANCE` in `src/theme.ts`), beside the
  box when there is room and on its top border otherwise. It never sits on the text.
- Numbers on screen are the golden fixtures' numbers, labelled measured or estimated as the renderer labels them.
  The Report scene draws them from `REPORT` in `src/script.ts`; the Lifecycle scene's reasons are the ones
  `trove autopilot` prints.
- Pace: `CPS` in `src/script.ts` is the typing speed and `EVERY` the gap between output lines. Scene lengths are
  the `*_FRAMES` constants.
