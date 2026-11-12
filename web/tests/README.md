# Web rendering checks

For the update shortcut, run `make build`, then
`NUSAMATION_PLAYWRIGHT=/tmp/nusamation-stress/node_modules/playwright CHROME=/usr/bin/google-chrome node web/tests/browser-update.cjs`.
The isolated server fixture checks initial and live update status, navigation,
and desktop/mobile rendering. Screenshots go to `/tmp`.

Run the self-contained logic tests with `node --test web/tests/*.test.cjs`.

For the browser stress test, install Playwright outside the checkout and point it
at a locally installed Chromium/Chrome. The fixture is isolated; no production
rooms or agents are contacted. It loads 2,000 messages and simulates ten active
rooms, each emitting ten small deltas roughly every 33 ms for 15 seconds. The
visible room also receives a tool-card update per interval and a new tool each
second.

```sh
PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install --prefix /tmp/nusamation-stress playwright@1.56.1
NUSAMATION_PLAYWRIGHT=/tmp/nusamation-stress/node_modules/playwright CHROME=/usr/bin/google-chrome node web/tests/browser-stress.cjs
```

Set `STRESS_MS` to change the duration and `CPU_THROTTLE=4` to emulate a slower CPU. The output reports delivered and rendered
visible-room deltas, frame gaps, DOM size, initial load time, and emulated
two-finger pinch zoom. It is a browser
fixture, not an end-to-end ACP/backend load test; one synthetic delta is not a
measured model token. Responsive, real-device, and network-failure checks remain
separate.

For question pagination and scrolling, run the isolated interaction fixture:

```sh
NUSAMATION_PLAYWRIGHT=/tmp/nusamation-stress/node_modules/playwright CHROME=/usr/bin/google-chrome node web/tests/browser-questions.cjs
```

It checks answer retention, typed submission, required fields, multi-select
bounds, keyboard tabs, retry and pending behavior, and desktop/mobile/short-height
layout. Screenshots go to `/tmp/nusamation-questions` (override with
`QUESTION_SCREENSHOTS`). This does not exercise a real mobile software keyboard.

For Markdown/local files/media, run `make build` then:

```sh
NUSAMATION_PLAYWRIGHT=/tmp/nusamation-stress/node_modules/playwright CHROME=/usr/bin/google-chrome node web/tests/browser-resources.cjs
```

This starts the actual binary with a temporary data directory and disposable
login, creates artifacts and uploads through the HTTP API, and checks previews,
local/relative/file URLs, media loading, downloads, text encodings, errors, and
streaming player identity. It requires FFmpeg for tiny MP4/WAV fixtures. Screenshots
and server logs go to `/tmp/nusamation-resources` (`RESOURCE_SCREENSHOTS` overrides
it). `NUSAMATION_BINARY` can select another freshly built binary. The server and
its temporary data are removed after the test. No installed instance is changed.
