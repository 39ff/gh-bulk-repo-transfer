# Demo GIFs

`docs/demo-interactive.gif` and `docs/demo-cli.gif` are recordings of the real
`ghbt` binary talking to a **fake GitHub API with sample data** (`server.py`),
so no real repositories are involved.

How they are made:

1. `record.py` starts `server.py`, runs `ghbt` in a pseudo-terminal, presses
   keys when the expected text is on screen, and saves the output with timings.
2. `render.js` replays that output in [xterm.js](https://xtermjs.org/) inside
   headless Chromium (Playwright) and screenshots every change.
3. `encode.sh` turns the screenshots into a GIF with ffmpeg.

Requirements: Go, Python 3 with `pyte`, Node.js, ffmpeg, the JetBrains Mono font.

```bash
go build -o /tmp/ghbt ./cmd/ghbt
```

```bash
cd docs/demo && npm install && npx playwright install chromium && python3 -m pip install pyte
```

```bash
python3 record.py /tmp/ghbt interactive interactive.json && node render.js interactive.json frames-i "ghbt — interactive mode" 2 && ./encode.sh frames-i ../demo-interactive.gif
```

```bash
python3 record.py /tmp/ghbt cli cli.json && node render.js cli.json frames-c "ghbt — command line" 2 && ./encode.sh frames-c ../demo-cli.gif
```
