#!/usr/bin/env bash
# @author Kurok1 <im.kurokyhanc@gmail.com>
# @since 3.3.0

set -euo pipefail

desktop_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$desktop_root"

project_version="$(node -p "require('./package.json').version")"
product_name="$(node -p "require('./src-tauri/tauri.conf.json').productName")"
bundle_dir="src-tauri/target/aarch64-apple-darwin/release/bundle"
app_path="${bundle_dir}/macos/${product_name}.app"
plist_path="${app_path}/Contents/Info.plist"
app_version="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$plist_path")"
executable_name="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$plist_path")"

if [[ "$app_version" != "$project_version" ]]; then
  echo "desktop bundle version ${app_version} does not match ${project_version}" >&2
  exit 1
fi
architectures="$(lipo -archs "${app_path}/Contents/MacOS/${executable_name}")"
if [[ "$architectures" != 'arm64' ]]; then
  echo "desktop bundle must contain only arm64, found ${architectures}" >&2
  exit 1
fi
codesign --verify --deep --strict "$app_path"

shopt -s nullglob
dmg_files=("${bundle_dir}/dmg/"*.dmg)
if [[ ${#dmg_files[@]} -ne 1 ]]; then
  echo "expected one desktop DMG, found ${#dmg_files[@]}" >&2
  exit 1
fi
hdiutil verify "${dmg_files[0]}"

output_dir="${desktop_root}/release-artifacts"
artifact_name="agents-otel-monitor-desktop_v${project_version}_darwin-arm64"
mkdir -p "$output_dir"
cp "${dmg_files[0]}" "${output_dir}/${artifact_name}.dmg"
ditto -c -k --sequesterRsrc --keepParent "$app_path" "${output_dir}/${artifact_name}.app.zip"

cd "$output_dir"
shasum -a 256 "${artifact_name}.dmg" > "${artifact_name}.dmg.sha256"
shasum -a 256 "${artifact_name}.app.zip" > "${artifact_name}.app.zip.sha256"
shasum -a 256 -c "${artifact_name}.dmg.sha256" "${artifact_name}.app.zip.sha256"
