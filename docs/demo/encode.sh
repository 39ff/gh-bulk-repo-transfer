#!/bin/bash
# encode.sh FRAMES_DIR OUT.gif
set -e
ffmpeg -loglevel error -y -f concat -safe 0 -i "$1/list.txt" \
  -vf "split[a][b];[a]palettegen=max_colors=128:stats_mode=full[p];[b][p]paletteuse=dither=none:diff_mode=rectangle" \
  -fps_mode vfr "$2"
