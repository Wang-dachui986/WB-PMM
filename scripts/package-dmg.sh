#!/usr/bin/env bash
# APP=/absolute/path/WB-PMM.app OUT=/absolute/path/release \
#   [LEGAL_DIR=/absolute/path/legal] [VERSION=0.1.0] bash scripts/package-dmg.sh
# Packages an existing build; only the temporary copy is signed.
set -euo pipefail

: "${APP:?Set APP to the validated WB-PMM.app}"
: "${OUT:?Set OUT to the release directory}"
[[ "$APP" = /* && "$OUT" = /* ]] || { echo 'APP and OUT must be absolute paths' >&2; exit 1; }
[[ -d "$APP" && "$(basename "$APP")" = WB-PMM.app ]] || { echo "Invalid app: $APP" >&2; exit 1; }

info="$APP/Contents/Info.plist"
app_version="$(/usr/libexec/PlistBuddy -c 'Print CFBundleShortVersionString' "$info")"
version="${VERSION:-$app_version}"
[[ "$version" = "$app_version" ]] || { echo "VERSION $version does not match app $app_version" >&2; exit 1; }

for binary in Contents/MacOS/WorkBuddyDesktop Contents/Helpers/wbgui Contents/Helpers/wb2api; do
  [[ -x "$APP/$binary" ]] && lipo "$APP/$binary" -verify_arch arm64 >/dev/null || {
    echo "Missing executable arm64 binary: $APP/$binary" >&2; exit 1;
  }
done
for resource in WBPMM.icns WBPMMMenuTemplate.png; do
  [[ -f "$APP/Contents/Resources/$resource" ]] || { echo "Missing resource: $resource" >&2; exit 1; }
done
bundle_id="$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$info")"

if [[ -n "${LEGAL_DIR:-}" ]]; then
  [[ "$LEGAL_DIR" = /* && -d "$LEGAL_DIR" ]] || { echo "Invalid LEGAL_DIR: $LEGAL_DIR" >&2; exit 1; }
  [[ -n "$(find "$LEGAL_DIR" -type f -print -quit)" ]] || { echo "LEGAL_DIR is empty: $LEGAL_DIR" >&2; exit 1; }
fi

mkdir -p "$OUT"
out="$(cd "$OUT" && pwd -P)"
name="WB-PMM-${version}-macos-arm64.dmg"
[[ ! -e "$out/$name" && ! -e "$out/$name.sha256" ]] || {
  echo "Release already exists: $out/$name" >&2; exit 1;
}

work="$(mktemp -d "$out/.package-dmg.XXXXXX")"
mounted=0
cleanup() {
  if [[ "$mounted" = 1 ]] && ! hdiutil detach -quiet "$work/mount"; then
    echo "Could not unmount $work/mount; temporary files were retained" >&2
    return
  fi
  rm -r "$work"
}
trap cleanup EXIT
mkdir "$work/stage" "$work/mount"
staged_app="$work/stage/WB-PMM.app"
ditto "$APP" "$staged_app"
if [[ -n "${LEGAL_DIR:-}" ]]; then
  mkdir "$staged_app/Contents/Resources/Legal"
  ditto "$LEGAL_DIR" "$staged_app/Contents/Resources/Legal"
fi
ln -s /Applications "$work/stage/Applications"

stray="$(find "$work/stage" \( -name '._*' -o -name '.DS_Store' \) -print -quit)"
[[ -z "$stray" ]] || { echo "Forbidden package file: $stray" >&2; exit 1; }
for helper in wbgui wb2api; do
  codesign --force --sign - "$staged_app/Contents/Helpers/$helper"
done
sign_args=(--force --sign - --identifier "$bundle_id")
if codesign -dv "$staged_app" >/dev/null 2>&1; then
  sign_args+=(--preserve-metadata=entitlements)
fi
codesign "${sign_args[@]}" "$staged_app"
codesign --verify --deep --strict "$staged_app"
signature="$(codesign -dv "$staged_app" 2>&1)"
[[ "$signature" = *'Signature=adhoc'* ]] || { echo 'Expected ad-hoc signature' >&2; exit 1; }

image="$work/$name"
hdiutil create -quiet -srcfolder "$work/stage" -volname "WB-PMM $version" \
  -fs HFS+ -format UDZO -imagekey zlib-level=9 "$image"
hdiutil verify "$image"
hdiutil attach -quiet -nobrowse -readonly -mountpoint "$work/mount" "$image"
mounted=1
[[ -d "$work/mount/WB-PMM.app" && -L "$work/mount/Applications" ]] || {
  echo 'DMG is missing the app or Applications link' >&2; exit 1;
}
[[ "$(readlink "$work/mount/Applications")" = /Applications ]] || {
  echo 'Applications link has the wrong target' >&2; exit 1;
}
[[ "$(find "$work/mount" -mindepth 1 -maxdepth 1 -print | wc -l | tr -d ' ')" = 2 ]] || {
  echo 'Unexpected files at DMG root' >&2; exit 1;
}
codesign --verify --deep --strict "$work/mount/WB-PMM.app"
for binary in Contents/MacOS/WorkBuddyDesktop Contents/Helpers/wbgui Contents/Helpers/wb2api; do
  lipo "$work/mount/WB-PMM.app/$binary" -verify_arch arm64 >/dev/null
done
if [[ -n "${LEGAL_DIR:-}" ]]; then
  diff -rq "$LEGAL_DIR" "$work/mount/WB-PMM.app/Contents/Resources/Legal"
fi
[[ -z "$(find "$work/mount" \( -name '._*' -o -name '.DS_Store' \) -print -quit)" ]] || {
  echo 'DMG contains AppleDouble or Finder metadata' >&2; exit 1;
}
hdiutil detach -quiet "$work/mount"
mounted=0

mv "$image" "$out/$name"
(cd "$out" && shasum -a 256 "$name") > "$out/$name.sha256"
echo "Created: $out/$name"
cat "$out/$name.sha256"
echo 'Signature: ad-hoc; Developer ID and notarization are not included.'
