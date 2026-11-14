# Magi brand assets

Status: The user accepted this design on 2026-11-11.

The logo is a seated guide with six arms. A yellow star is on the forehead.
The arms show the system's ability to do many tasks. The design has no face,
fingers, or clothing details.

A short neck connects the head to the raised shoulders. The top two pairs
of arms extend outward. The bottom pair bends inward. The palms are upright
below the chin, and the elbows extend outward. Transparent gaps separate the
hands and forearms from the torso.
The torso narrows at the waist. One transparent opening separates the crossed
legs.

## Files

| File | Use |
| --- | --- |
| `logo.svg` | Source logo. Use on a light background. |
| `wordmark.svg` | Source name. The letters are vector paths. No font is required. |
| `favicon.svg` | Source small icon. Use below 64 pixels. |
| `logo-horizontal.svg` | Logo with the Magi name. |
| `logo-reversed.svg` | White guide with a yellow star. Use on a dark background. |
| `logo-horizontal-reversed.svg` | White logo and name with a yellow star. |
| `logo-mono.svg` | One dark blue ink with a transparent star. |
| `logo-mono-reversed.svg` | One white ink with a transparent star. |
| `app-icon.svg` | Full guide on an ivory square. The platform can crop the corners. |
| `social-card.svg` | Image for a shared link, 1200 by 630 pixels. |
| `favicon.ico` | Small icon at 16, 32, and 48 pixels. |
| `png/` | Raster exports. Logo exports have transparent backgrounds. |
| `preview.png`, `preview.svg` | Asset preview. Do not use as a logo. |
| `colors.css` | Brand color variables. |

## Colors and spacing

- Navy: `#142D4E`.
- Yellow: `#FFD34E`.
- Ivory: `#F7F5EF`.
- White: `#FFFFFF`.

Keep the square logo's empty border. Do not stretch or rotate the logo.
Keep other content at least one head radius from the visible guide.
Use the full guide at 64 pixels or larger. Use the small icon below 64 pixels.
Check the star at the final display size. At 16 pixels, its points can merge.
Use the yellow as an accent. Use navy for text on light backgrounds.

## Build the exports

Edit `logo.svg`, `wordmark.svg`, or `favicon.svg`. Then run:

```sh
python3 brand/build.py
```

The build needs Python 3, `rsvg-convert`, and ImageMagick (`magick`).
It needs no Python packages or network connection.
The preview labels use DejaVu Sans, with a sans-serif fallback.
The logo and wordmark need no fonts.
The build replaces the generated assets in this directory.

To view the preview from a WezTerm shell, run:

```sh
wezterm imgcat --width 100 brand/preview.png
```

## Design context

The user selected the guide design on 2026-11-11. Figures with many arms also
occur in Hindu and Buddhist art. This logo does not depict a named deity.
See [The Met's Avalokiteshvara example](https://www.metmuseum.org/art/collection/search/38959)
for that visual context.

## Next Actions

The design review is complete. Connect the assets to the web and Android
clients when requested. Check the final display sizes during that work.
