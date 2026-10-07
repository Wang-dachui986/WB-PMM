#!/bin/bash
# APP=/absolute/path/WB-PMM.app OUT=/absolute/path/release [VERSION=1.0.3] \
#   [INSTALLER_SIGN_IDENTITY='Developer ID Installer: ...'] bash scripts/package-pkg.sh
# Packages an already signed App without changing its contents or signature.
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin

root="$(cd "$(dirname "$0")/.." && pwd -P)"
: "${APP:?Set APP to the signed WB-PMM.app}"
: "${OUT:?Set OUT to the release directory}"
[[ "$APP" = /* && "$OUT" = /* && -d "$APP" && ! -L "$APP" && "$(basename "$APP")" = WB-PMM.app ]] || {
  echo 'APP must be an absolute, non-symlink WB-PMM.app; OUT must be absolute' >&2; exit 1;
}

info="$APP/Contents/Info.plist"
bundle_id="$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$info")"
app_version="$(/usr/libexec/PlistBuddy -c 'Print CFBundleShortVersionString' "$info")"
app_build="$(/usr/libexec/PlistBuddy -c 'Print CFBundleVersion' "$info")"
version="${VERSION:-$app_version}"
[[ "$bundle_id" = cn.workbuddy.desktop && "$version" = "$app_version" && "$version" =~ ^[0-9]+(\.[0-9]+){1,3}$ && -n "$app_build" ]] || {
  echo 'App identity or version does not match this package' >&2; exit 1;
}
[[ -z "${BUILD_NUMBER:-}" || "$BUILD_NUMBER" = "$app_build" ]] || {
  echo "BUILD_NUMBER does not match App build $app_build" >&2; exit 1;
}
for binary in Contents/MacOS/WorkBuddyDesktop Contents/Helpers/wbgui Contents/Helpers/wb2api; do
  [[ -x "$APP/$binary" ]] && /usr/bin/lipo "$APP/$binary" -verify_arch arm64 >/dev/null || {
    echo "Missing executable arm64 binary: $binary" >&2; exit 1;
  }
done
legal="$APP/Contents/Resources/Legal"
[[ -d "$legal" && -n "$(/usr/bin/find "$legal" -type f ! -name '._*' -print -quit)" ]] || {
  echo 'Signed App must already contain third-party Legal files' >&2; exit 1;
}
if [[ -n "${LEGAL_DIR:-}" ]]; then
  [[ "$LEGAL_DIR" = /* && -d "$LEGAL_DIR" ]] && /usr/bin/diff -rq "$LEGAL_DIR" "$legal" || {
    echo 'App Legal files do not match LEGAL_DIR' >&2; exit 1;
  }
fi
[[ -z "$(/usr/bin/find "$APP" \( -name '._*' -o -name '.DS_Store' \) -print -quit)" ]] || {
  echo 'Input App contains AppleDouble or Finder metadata; prepare and sign a clean App first' >&2; exit 1;
}
# Do not change a signed App here: fail if the build shipped owner-only resources.
/usr/bin/python3 - "$APP/Contents/Resources/WBPMMMenuTemplate.png" <<'PYMODE'
import os, stat, sys
mode = os.stat(sys.argv[1]).st_mode
if not stat.S_ISREG(mode) or mode & 0o444 != 0o444:
    raise SystemExit('Menu icon must be readable by all users before signing (0644)')
PYMODE
/usr/bin/codesign --verify --deep --strict "$APP"

/bin/mkdir -p "$OUT"
out="$(cd "$OUT" && pwd -P)"
name="WB-PMM-${version}-macos-arm64.pkg"
[[ ! -e "$out/$name" && ! -e "$out/$name.sha256" ]] || {
  echo "Release already exists: $out/$name" >&2; exit 1;
}
work="$(/usr/bin/mktemp -d "$out/.package-pkg.XXXXXX")"
cleanup() {
  status=$?
  trap - EXIT
  [[ ! -d "$work" ]] || /bin/rm -r "$work" || status=1
  exit "$status"
}
trap cleanup EXIT
/bin/mkdir "$work/stage" "$work/scripts" "$work/resources"
/usr/bin/ditto "$APP" "$work/stage/WB-PMM.app"
[[ -z "$(/usr/bin/find "$work/stage" \( -name '._*' -o -name '.DS_Store' \) -print -quit)" ]] || {
  echo 'App contains AppleDouble or Finder metadata' >&2; exit 1;
}
/usr/bin/codesign --verify --deep --strict "$work/stage/WB-PMM.app"
/usr/bin/diff -rq "$APP" "$work/stage/WB-PMM.app" >/dev/null || {
  echo 'Staged App differs from the signed input' >&2; exit 1;
}

/usr/bin/swiftc -parse-as-library -swift-version 6 -O -target arm64-apple-macosx14.0 \
  "$root/scripts/installer-helper.swift" -o "$work/scripts/installer-helper"
/usr/bin/lipo "$work/scripts/installer-helper" -verify_arch arm64 >/dev/null
/bin/cp "$root/scripts/installer/preinstall" "$root/scripts/installer/postinstall" "$work/scripts/"
/bin/chmod 755 "$work/scripts/preinstall" "$work/scripts/postinstall" "$work/scripts/installer-helper"
/bin/cp "$root/scripts/installer/Welcome.html" "$root/scripts/installer/Conclusion.html" "$work/resources/"

components="$work/components.plist"
/usr/bin/pkgbuild --analyze --root "$work/stage" "$components"
/usr/bin/python3 - "$components" <<'PY'
import plistlib, sys
path = sys.argv[1]
with open(path, 'rb') as file:
    items = plistlib.load(file)
if len(items) != 1 or items[0].get('RootRelativeBundlePath') != 'WB-PMM.app':
    raise SystemExit('pkgbuild did not find exactly the WB-PMM.app component')
items[0].update(BundleIsRelocatable=False, BundleOverwriteAction='upgrade',
                BundleHasStrictIdentifier=True, BundleIsVersionChecked=False)
with open(path, 'wb') as file:
    plistlib.dump(items, file)
PY

component="$work/component.pkg"
/usr/bin/pkgbuild --root "$work/stage" --component-plist "$components" \
  --install-location /Applications --identifier cn.workbuddy.desktop.pkg \
  --version "$version" --scripts "$work/scripts" "$component"
/usr/bin/python3 - "$work/requirements.plist" <<'PY'
import plistlib, sys
with open(sys.argv[1], 'wb') as file:
    plistlib.dump({'arch': ['arm64'], 'os': ['14.0']}, file)
PY
distribution="$work/Distribution.xml"
(cd "$work" && /usr/bin/productbuild --synthesize --product requirements.plist --package component.pkg Distribution.xml)
/usr/bin/python3 - "$distribution" "$version" <<'PY'
import sys
import xml.etree.ElementTree as ET
path, version = sys.argv[1:]
tree = ET.parse(path)
root = tree.getroot()
if root.tag != 'installer-gui-script':
    raise SystemExit('unexpected productbuild Distribution')
options = root.find('options')
if options is None:
    options = ET.SubElement(root, 'options')
options.set('customize', 'never')
options.set('hostArchitectures', 'arm64')
ET.SubElement(root, 'domains', {'enable_anywhere': 'false',
                                'enable_currentUserHome': 'false', 'enable_localSystem': 'true'})
ET.SubElement(root, 'title').text = 'WB-PMM ' + version
ET.SubElement(root, 'welcome', {'file': 'Welcome.html', 'mime-type': 'text/html'})
ET.SubElement(root, 'conclusion', {'file': 'Conclusion.html', 'mime-type': 'text/html'})
tree.write(path, encoding='utf-8', xml_declaration=True)
PY

if [[ -n "${INSTALLER_SIGN_IDENTITY:-}" ]]; then
  /usr/bin/productbuild --distribution "$distribution" --package-path "$work" \
    --resources "$work/resources" --sign "$INSTALLER_SIGN_IDENTITY" "$work/$name"
else
  /usr/bin/productbuild --distribution "$distribution" --package-path "$work" \
    --resources "$work/resources" "$work/$name"
fi
/usr/bin/xar -tf "$work/$name" | /usr/bin/grep '^Distribution$' >/dev/null
/bin/mv "$work/$name" "$out/$name"
(cd "$out" && /usr/bin/shasum -a 256 "$name") > "$out/$name.sha256"
echo "Created: $out/$name"
/bin/cat "$out/$name.sha256"
if [[ -z "${INSTALLER_SIGN_IDENTITY:-}" ]]; then
  echo 'PKG: unsigned (no Developer ID Installer identity configured). App signature was preserved.'
else
  /usr/sbin/pkgutil --check-signature "$out/$name"
fi
echo 'Install destination: /Applications/WB-PMM.app; App is not opened automatically.'
