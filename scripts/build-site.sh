#!/usr/bin/env bash
# Assemble the yc microsite into _site/ for GitHub Pages.
#
# The page lives in site/index.html, but its screenshots come from
# docs/assets/screenshots/, which the README and the docs also reference.
# Copying rather than duplicating keeps one source of truth for them.
#
# Regenerate the SVG screenshots with:
#   YC_WRITE_SCREENSHOTS=1 go test ./internal/app -run TestWriteDocsScreenshots
#
# The site is deliberately self-contained: no CDN, no web fonts, no analytics,
# no frameworks. This script enforces that - it fails the build if the output
# fetches anything over the network. Outbound <a href> links are fine; a
# stylesheet, script, image, or font pulled from another host is not.
#
# assets/yc-banner.png is the Open Graph card. It is a checked-in raster of
# docs/assets/yc-banner.svg, because social scrapers do not render SVG. Rebuild
# it after changing the banner with:
#   rsvg-convert -w 1160 docs/assets/yc-banner.svg -o /tmp/banner.png
#   magick /tmp/banner.png -background '#0b0708' -gravity center \
#          -extent 1200x630 -strip site/assets/yc-banner.png
#
# Usage: scripts/build-site.sh [output-dir]   (default: _site)
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${1:-$repo_root/_site}"

if [[ ! -d "$repo_root/site" ]]; then
	echo "error: $repo_root/site not found" >&2
	exit 1
fi
if [[ ! -f "$repo_root/site/index.html" ]]; then
	echo "error: $repo_root/site/index.html not found" >&2
	exit 1
fi

# ---------------------------------------------------------------- assemble
rm -rf "$out_dir"
mkdir -p "$out_dir/assets"

cp -R "$repo_root/site/." "$out_dir/"
cp -R "$repo_root/docs/assets/." "$out_dir/assets/"

# The mirror marker below is a build artefact of site/, not something Pages
# should serve.
rm -f "$out_dir/assets/.gitignore"

# Pages serves this tree directly; .nojekyll stops GitHub from running Jekyll
# over it, which would otherwise drop any path beginning with an underscore.
touch "$out_dir/.nojekyll"

# Mirror the shared screenshots back into site/ so site/index.html also renders
# correctly when opened straight off the filesystem. The mirror is ignored by
# git (see site/assets/.gitignore) - docs/assets stays the only source.
if [[ -z "${YC_SITE_NO_MIRROR:-}" ]]; then
	mkdir -p "$repo_root/site/assets/screenshots"
	cp -R "$repo_root/docs/assets/screenshots/." "$repo_root/site/assets/screenshots/"
fi

# ------------------------------------------------------- validate: no CDN
# Anything the browser fetches on its own must be same-origin. These patterns
# cover every fetching form the page could plausibly grow: markup resource
# attributes, <link> hrefs, CSS url() and @import, and JS network calls.
fail=0
report() {
	fail=1
	echo "error: $1" >&2
	printf '%s\n' "$2" | sed 's/^/    /' >&2
}

scan() {
	# scan <description> <extended-regex>
	local desc="$1" re="$2" hits
	hits="$(grep -rInE "$re" "$out_dir" --include='*.html' --include='*.css' --include='*.js' --include='*.svg' || true)"
	if [[ -n "$hits" ]]; then
		report "$desc" "$hits"
	fi
}

scan "external resource attribute (src/srcset/poster/data-src) in the built site" \
	'(src|srcset|poster|data-src)[[:space:]]*=[[:space:]]*["'"'"']?https?:'
scan "external <link> in the built site" \
	'<link[^>]+href[[:space:]]*=[[:space:]]*["'"'"']?https?:'
scan "external CSS url() in the built site" \
	'url\([[:space:]]*["'"'"']?https?:'
scan "@import in the built site" \
	'@import'
scan "network call in the built site (fetch/XHR/WebSocket/importScripts to a remote host)" \
	'(fetch|importScripts)\([[:space:]]*["'"'"']https?:|new[[:space:]]+(XMLHttpRequest|WebSocket|EventSource)\([[:space:]]*["'"'"']?(https?|wss?):'
scan "@font-face in the built site (the page is system-font only)" \
	'@font-face'

# ------------------------------------------- validate: internal assets exist
# Every same-origin src=/href= that points at a file must resolve inside the
# output tree, or Pages serves a 404 nobody notices until someone else looks.
missing="$(
	grep -oE '(src|href)="[^"#?][^"]*"' "$out_dir/index.html" |
		sed -E 's/^(src|href)="//; s/"$//' |
		grep -vE '^(https?:|mailto:|data:|//)' |
		sort -u |
		while IFS= read -r ref; do
			[[ -e "$out_dir/${ref#/}" ]] || echo "$ref"
		done
)"
if [[ -n "$missing" ]]; then
	report "referenced asset missing from $out_dir" "$missing"
fi

# The Open Graph image is an absolute URL (relative ones break every scraper),
# so the check above cannot see it. Verify the file it names is really shipped.
og="$(grep -oE '<meta property="og:image" content="[^"]+"' "$out_dir/index.html" |
	sed -E 's/.*content="//; s/"$//' || true)"
if [[ -n "$og" ]]; then
	og_path="${og##*/yc/}"
	if [[ "$og_path" != "$og" && ! -e "$out_dir/$og_path" ]]; then
		report "og:image points at a file that is not in the build" "$og -> $out_dir/$og_path"
	fi
fi

if [[ "$fail" -ne 0 ]]; then
	echo "site build failed validation" >&2
	exit 1
fi

echo "built site -> $out_dir"
find "$out_dir" -type f | sed "s|$out_dir/|  |" | sort
