# Repository social preview

GitHub renders the social preview (Open Graph card) at **1280x640 PNG**. It is not
read from the repo tree - it is uploaded once at
`Settings -> General -> Social preview -> Upload an image`.

## What to upload

A 1280x640 PNG rendered from [`docs/assets/yc-banner.svg`](../docs/assets/yc-banner.svg),
padded onto the yc canvas color so the card keeps its margins on both light and dark
GitHub themes.

## How to generate it

The banner is 900x220. Render it at 2.4x (2160x528) and center it on a 1280x640
canvas scaled to fit with breathing room:

```sh
rsvg-convert -w 1120 docs/assets/yc-banner.svg -o /tmp/yc-banner@1120.png
magick /tmp/yc-banner@1120.png \
  -background '#0b0708' -gravity center -extent 1280x640 \
  /tmp/yc-social-preview.png
```

`rsvg-convert` comes from `librsvg`; `magick` from ImageMagick 7. With ImageMagick 6,
use `convert` in place of `magick`. If neither is installed, any SVG-capable renderer
works - the only requirements are the 1280x640 output size and the `#0b0708` matte,
which is the `yc` theme preset's `Background` (`internal/theme/presets.go`).

Do not commit the generated PNG. It is a one-time upload, and regenerating it from the
SVG is a single command.

## Related assets

| File | Size | Use |
| --- | --- | --- |
| `docs/assets/yc-logo.svg` | 256x256 | square mark, README/docs |
| `docs/assets/yc-banner.svg` | 900x220 | README header, site hero |
| `docs/assets/favicon.svg` | 32x32 (64 viewBox) | browser tab icon |

`site/assets/` carries byte-identical copies for the GitHub Pages site.
