#!/usr/bin/env python3
"""Build the Magi brand assets from the three source SVG files."""

from html import escape
from pathlib import Path
import shutil
import re
import subprocess
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parent
NAVY = "#142D4E"
YELLOW = "#FFD34E"
IVORY = "#F7F5EF"
WHITE = "#FFFFFF"
NS = "{http://www.w3.org/2000/svg}"


def artwork(name):
    """Read source artwork without its outer SVG element or labels."""
    source = (ROOT / name).read_text()
    root = ET.fromstring(source)
    # Keep the source markup so generated files remain easy to read.
    start = source.index("\n", source.index("<desc")) + 1
    assert root.tag == NS + "svg"
    return source[start:source.rindex("</svg>")].strip()


def svg(width, height, title, body):
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" '
        f'height="{height}" viewBox="0 0 {width} {height}" '
        'role="img" aria-labelledby="title">\n'
        f'  <title id="title">{escape(title)}</title>\n{body}\n</svg>\n'
    )


def write(name, width, height, title, body):
    (ROOT / name).write_text(svg(width, height, title, body))


def placed(body, x, y, scale=1):
    # Each copy needs its own IDs when a preview contains several logos.
    suffix = f"{x}-{y}-{scale}".replace(".", "_")
    for identifier in re.findall(r'\bid="([^"]+)"', body):
        scoped = f"{identifier}-{suffix}"
        body = body.replace(f'id="{identifier}"', f'id="{scoped}"')
        body = body.replace(f'url(#{identifier})', f'url(#{scoped})')
    return f'<g transform="translate({x} {y}) scale({scale})">{body}</g>'


def label(text, x, y, size=18, color=NAVY):
    return (
        f'<text x="{x}" y="{y}" fill="{color}" '
        f'font-family="DejaVu Sans, sans-serif" font-size="{size}">'
        f'{escape(text)}</text>'
    )


def render(source, target, width, height=None):
    subprocess.run(
        ["rsvg-convert", "-w", str(width), "-h", str(height or width),
         "-o", str(ROOT / target), str(ROOT / source)],
        check=True,
    )


def main():
    for tool in ("rsvg-convert", "magick"):
        if not shutil.which(tool):
            raise SystemExit(f"Install {tool} before you build the assets.")
    (ROOT / "png").mkdir(exist_ok=True)
    mark = artwork("logo.svg")
    word = artwork("wordmark.svg")
    small = artwork("favicon.svg")
    reverse = mark.replace(NAVY, WHITE)
    # A transparent star cutout keeps each monochrome logo to one ink.
    star = list(ET.fromstring((ROOT / "logo.svg").read_text()))[-1].attrib["d"]
    mask = (
        '<defs><mask id="star-cutout" maskUnits="userSpaceOnUse" '
        'x="0" y="0" width="512" height="512" style="mask-type:luminance">'
        '<rect width="512" height="512" fill="white"/>'
        f'<path d="{star}" fill="black"/></mask></defs>'
    )
    mono = mask + '<g mask="url(#star-cutout)">' + mark.replace(YELLOW, NAVY) + '</g>'
    mono_white = mono.replace(NAVY, WHITE)
    write("logo-mono.svg", 512, 512, "Magi, one color", mono)
    write("logo-mono-reversed.svg", 512, 512, "Magi, one color on dark backgrounds", mono_white)
    write("logo-reversed.svg", 512, 512, "Magi on dark backgrounds", reverse)
    lockup = placed(mark, 0, 4, 0.625) + placed(word, 346, 66, 1.75)
    write("logo-horizontal.svg", 920, 360, "Magi logo and name", lockup)
    write("logo-horizontal-reversed.svg", 920, 360, "Magi logo and name on dark backgrounds", lockup.replace(NAVY, WHITE))
    write("app-icon.svg", 512, 512, "Magi app icon", f'<path fill="{IVORY}" d="M0 0H512V512H0Z"/>' + mark)
    # Android adaptive icon: the system crops the 108 dp layer to a shape.
    # The scale 0.75 keeps the mark in the circle that each shape shows.
    write("android-foreground.svg", 512, 512, "Magi Android icon foreground", placed(mark, 64, 64, 0.75))
    write("android-monochrome.svg", 512, 512, "Magi Android icon, one color", placed(mono, 64, 64, 0.75))
    social = (
        f'<path fill="{NAVY}" d="M0 0H1200V630H0Z"/>'
        + placed(lockup.replace(NAVY, WHITE), 140, 137)
    )
    write("social-card.svg", 1200, 630, "Magi", social)

    board = f'<rect width="1440" height="1120" fill="{IVORY}"/>'
    board += label("MAGI / BRAND ASSETS", 64, 60, 18)
    board += label("The many-armed guide", 64, 108, 36)
    board += label("Six arms. One guide. A star on the forehead.", 64, 145, 18)
    board += '<rect x="48" y="184" width="808" height="512" rx="24" fill="white"/>'
    board += placed(mark, 196, 184)
    board += f'<rect x="880" y="184" width="512" height="512" rx="24" fill="{NAVY}"/>'
    board += placed(reverse, 906, 210, 0.9)
    board += placed(lockup, 48, 720, 0.75)
    board += label("SMALL ICON", 816, 758, 16)
    board += placed(small, 816, 784, 1.5)
    board += placed(small, 942, 816, 1)
    board += placed(small, 1036, 848, 0.5)
    board += placed(small, 1106, 864, 0.25)
    for x, size in ((816, 96), (942, 64), (1036, 32), (1106, 16)):
        board += label(str(size), x, 910, 14)
    for x, color, name in ((64, NAVY, "NAVY / #142D4E"), (420, YELLOW, "YELLOW / #FFD34E"), (800, IVORY, "IVORY / #F7F5EF")):
        board += f'<rect x="{x}" y="1020" width="44" height="44" rx="8" fill="{color}" stroke="{NAVY}" stroke-width="1"/>'
        board += label(name, x + 60, 1048, 16)
    write("preview.svg", 1440, 1120, "Magi brand preview", board)

    for size in (64, 128, 256, 512, 1024):
        render("logo.svg", f"png/logo-{size}.png", size)
    for name in ("logo-mono", "logo-mono-reversed", "logo-reversed"):
        render(name + ".svg", f"png/{name}-512.png", 512)
    for name in ("logo-horizontal", "logo-horizontal-reversed"):
        render(name + ".svg", f"png/{name}.png", 1840, 720)
    for size in (180, 192, 512):
        render("app-icon.svg", f"png/app-icon-{size}.png", size)
    res = "../android/app/src/main/res"
    for density, size in (("mdpi", 108), ("hdpi", 162), ("xhdpi", 216), ("xxhdpi", 324), ("xxxhdpi", 432)):
        (ROOT / res / f"mipmap-{density}").mkdir(parents=True, exist_ok=True)
        render("android-foreground.svg", f"{res}/mipmap-{density}/ic_launcher_foreground.png", size)
        render("android-monochrome.svg", f"{res}/mipmap-{density}/ic_launcher_monochrome.png", size)
    for size in (16, 32, 48):
        render("favicon.svg", f"png/favicon-{size}.png", size)
    render("social-card.svg", "png/social-card.png", 1200, 630)
    render("preview.svg", "preview.png", 1440, 1120)
    subprocess.run(
        ["magick", *(str(ROOT / f"png/favicon-{size}.png") for size in (16, 32, 48)),
         str(ROOT / "favicon.ico")], check=True,
    )
    print("Built SVG variants, PNG exports, favicon.ico, and preview.png.")


if __name__ == "__main__":
    main()
