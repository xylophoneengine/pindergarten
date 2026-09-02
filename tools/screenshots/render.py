#!/usr/bin/env python3
"""Render raw ANSI (.ans) terminal frames captured by
internal/tui/screenshot_test.go into PNG screenshots for the README.

Usage: render.py <ans-dir> <out-dir>

Parses a small, deliberate subset of SGR (Select Graphic Rendition)
sequences -- reset, bold, dim, underline, reverse, the basic/bright 16-color
codes, 256-color (38/48;5;n), truecolor (38/48;2;r;g;b), and the fg/bg
default resets (39/49) -- since that is everything lipgloss's TrueColor
profile ever emits. Every glyph in this project's TUI is single-width, so
each Unicode code point maps to exactly one terminal cell; there is no
wide/ambiguous-width handling to do beyond that 1:1 mapping.
"""
import os
import re
import subprocess
import sys

from PIL import Image, ImageDraw, ImageFont

FONT_SIZE = 16
PAD = 2

BG_DEFAULT = (30, 30, 46)  # #1e1e2e
FG_DEFAULT = (224, 224, 224)  # #e0e0e0

# Standard 16-color ANSI palette (xterm/VS Code defaults). lipgloss's
# TrueColor profile never actually emits these (everything in this project
# is an explicit truecolor or 256-color sequence), but a renderer that only
# handled the codes its own fixtures happen to produce would silently
# mis-render anything else -- so the basic/bright codes and the 256-color
# cube/grayscale ramp are implemented for real, not stubbed.
BASIC16 = [
    (0, 0, 0), (205, 49, 49), (13, 188, 121), (229, 229, 16),
    (36, 114, 200), (188, 63, 188), (17, 168, 205), (229, 229, 229),
    (102, 102, 102), (241, 76, 76), (35, 209, 139), (245, 245, 67),
    (59, 142, 234), (214, 112, 214), (41, 184, 219), (255, 255, 255),
]

_CUBE_LEVELS = [0, 95, 135, 175, 215, 255]


def xterm256(n):
    """Resolves an xterm 256-color index (38/48;5;n) to an (r, g, b) tuple."""
    if n < 16:
        return BASIC16[n]
    if n < 232:
        n -= 16
        r, g, b = n // 36, (n // 6) % 6, n % 6
        return (_CUBE_LEVELS[r], _CUBE_LEVELS[g], _CUBE_LEVELS[b])
    level = 8 + 10 * (n - 232)
    return (level, level, level)


class State:
    """Current SGR text-attribute state while parsing one line."""

    def __init__(self):
        self.fg = None
        self.bg = None
        self.bold = False
        self.dim = False
        self.underline = False
        self.reverse = False

    def apply(self, params):
        i = 0
        n = len(params)
        while i < n:
            code = params[i]
            if code == 0:
                self.__init__()
            elif code == 1:
                self.bold = True
            elif code == 2:
                self.dim = True
            elif code == 22:
                self.bold = self.dim = False
            elif code == 4:
                self.underline = True
            elif code == 24:
                self.underline = False
            elif code == 7:
                self.reverse = True
            elif code == 27:
                self.reverse = False
            elif code == 39:
                self.fg = None
            elif code == 49:
                self.bg = None
            elif 30 <= code <= 37:
                self.fg = BASIC16[code - 30]
            elif 90 <= code <= 97:
                self.fg = BASIC16[8 + code - 90]
            elif 40 <= code <= 47:
                self.bg = BASIC16[code - 40]
            elif 100 <= code <= 107:
                self.bg = BASIC16[8 + code - 100]
            elif code == 38 and i + 1 < n:
                if params[i + 1] == 5 and i + 2 < n:
                    self.fg = xterm256(params[i + 2])
                    i += 2
                elif params[i + 1] == 2 and i + 4 < n:
                    self.fg = (params[i + 2], params[i + 3], params[i + 4])
                    i += 4
            elif code == 48 and i + 1 < n:
                if params[i + 1] == 5 and i + 2 < n:
                    self.bg = xterm256(params[i + 2])
                    i += 2
                elif params[i + 1] == 2 and i + 4 < n:
                    self.bg = (params[i + 2], params[i + 3], params[i + 4])
                    i += 4
            i += 1

    def resolve(self):
        """Returns the effective (fg, bg, bold, underline) for a cell drawn
        under this state right now: default colors filled in, dim blended
        toward the background, reverse swapping fg/bg last (matching a real
        terminal's own attribute order)."""
        fg = self.fg if self.fg is not None else FG_DEFAULT
        bg = self.bg if self.bg is not None else BG_DEFAULT
        if self.dim:
            fg = tuple((c + bg_c) // 2 for c, bg_c in zip(fg, bg))
        if self.reverse:
            fg, bg = bg, fg
        return fg, bg, self.bold, self.underline


SGR_RE = re.compile(r"\x1b\[([0-9;]*)m")


def parse_line(line):
    """Splits one raw line into a list of (char, fg, bg, bold, underline)
    cells, in column order."""
    cells = []
    state = State()
    pos = 0
    length = len(line)
    while pos < length:
        m = SGR_RE.match(line, pos)
        if m:
            raw = m.group(1)
            params = [int(p) if p else 0 for p in raw.split(";")] if raw else [0]
            state.apply(params)
            pos = m.end()
            continue
        ch = line[pos]
        cells.append((ch,) + state.resolve())
        pos += 1
    return cells


def font_paths():
    """Locates DejaVu Sans Mono (regular and bold) via fontconfig, falling
    back to whatever fontconfig's own default match returns if DejaVu is
    somehow unavailable."""
    def match(pattern):
        out = subprocess.run(
            ["fc-match", "-f", "%{file}", pattern],
            capture_output=True, text=True, check=True,
        ).stdout
        return out.strip()

    regular = match("DejaVu Sans Mono")
    bold = match("DejaVu Sans Mono:bold")
    return regular, bold


def render_file(path, font, font_bold, out_path):
    with open(path, "r", encoding="utf-8") as f:
        raw_lines = f.read().split("\n")

    grid = [parse_line(line) for line in raw_lines]
    cols = max((len(row) for row in grid), default=0)
    rows = len(grid)

    # Cell size from the font's own metrics: a monospace font's advance
    # width is uniform, so any glyph's width is the cell width; ascent+
    # descent is the cell height (no extra line-gap -- this is a terminal
    # grid, not prose).
    ascent, descent = font.getmetrics()
    cell_w = round(font.getlength("M"))
    cell_h = ascent + descent

    img = Image.new("RGB", (cols * cell_w + 2 * PAD, rows * cell_h + 2 * PAD), BG_DEFAULT)
    draw = ImageDraw.Draw(img)

    for y, row in enumerate(grid):
        for x, (ch, fg, bg, bold, underline) in enumerate(row):
            cx = PAD + x * cell_w
            cy = PAD + y * cell_h
            if bg != BG_DEFAULT:
                draw.rectangle([cx, cy, cx + cell_w - 1, cy + cell_h - 1], fill=bg)
            if ch != " ":
                f = font_bold if bold else font
                draw.text((cx, cy), ch, font=f, fill=fg)
            if underline:
                uy = cy + ascent + 1
                draw.line([(cx, uy), (cx + cell_w - 1, uy)], fill=fg)

    img.save(out_path)
    return img.size


def main():
    if len(sys.argv) != 3:
        print("usage: render.py <ans-dir> <out-dir>", file=sys.stderr)
        return 1
    ans_dir, out_dir = sys.argv[1], sys.argv[2]
    os.makedirs(out_dir, exist_ok=True)

    regular_path, bold_path = font_paths()
    font = ImageFont.truetype(regular_path, FONT_SIZE)
    font_bold = ImageFont.truetype(bold_path, FONT_SIZE)

    names = sorted(n for n in os.listdir(ans_dir) if n.endswith(".ans"))
    if not names:
        print(f"no .ans files in {ans_dir}", file=sys.stderr)
        return 1

    for name in names:
        base = name[: -len(".ans")]
        size = render_file(
            os.path.join(ans_dir, name), font, font_bold,
            os.path.join(out_dir, base + ".png"),
        )
        print(f"{base}.png {size[0]}x{size[1]}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
