#!/usr/bin/env bash
set -euo pipefail

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

require_cmd curl
require_cmd gh
require_cmd jq
require_cmd git

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

detect_package() {
  if [[ -f npm/package.json ]]; then
    jq -r '.name' npm/package.json
    return
  fi
  jq -r '.name' package.json
}

detect_repo() {
  local remote
  remote="$(git remote get-url origin 2>/dev/null || true)"
  if [[ -z "$remote" ]]; then
    echo "could not detect git remote origin" >&2
    exit 1
  fi

  echo "$remote" | sed -E 's#^git@github.com:##; s#^https://github.com/##; s#\.git$##'
}

package_name="${1:-$(detect_package)}"
repo_name="${2:-$(detect_repo)}"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

curl -fsSL "https://api.npmjs.org/downloads/point/last-day/${package_name}" >"$tmp_dir/npm-day.json"
curl -fsSL "https://api.npmjs.org/downloads/point/last-week/${package_name}" >"$tmp_dir/npm-week.json"
curl -fsSL "https://api.npmjs.org/downloads/point/last-month/${package_name}" >"$tmp_dir/npm-month.json"
curl -fsSL "https://api.npmjs.org/downloads/range/last-month/${package_name}" >"$tmp_dir/npm-range.json"
gh api "repos/${repo_name}/releases" >"$tmp_dir/releases.json"

local_version="$(jq -r '.version' npm/package.json)"

day_downloads="$(jq -r '.downloads' "$tmp_dir/npm-day.json")"
day_start="$(jq -r '.start' "$tmp_dir/npm-day.json")"
week_downloads="$(jq -r '.downloads' "$tmp_dir/npm-week.json")"
week_start="$(jq -r '.start' "$tmp_dir/npm-week.json")"
week_end="$(jq -r '.end' "$tmp_dir/npm-week.json")"
month_downloads="$(jq -r '.downloads' "$tmp_dir/npm-month.json")"
month_start="$(jq -r '.start' "$tmp_dir/npm-month.json")"
month_end="$(jq -r '.end' "$tmp_dir/npm-month.json")"
last_active_day="$(jq -r '[.downloads[] | select(.downloads > 0)][-1].day // "n/a"' "$tmp_dir/npm-range.json")"
last_active_count="$(jq -r '[.downloads[] | select(.downloads > 0)][-1].downloads // 0' "$tmp_dir/npm-range.json")"

latest_release_summary="$(
  jq -r --arg version "v${local_version}" '
    map(select(.tag_name == $version))[0] as $release
    | if $release == null then
        "missing"
      else
        {
          tag: $release.tag_name,
          published_at: $release.published_at,
          archive_downloads: ([ $release.assets[] | select(.name | test("(\\.tar\\.gz|\\.zip)$")) | .download_count ] | add // 0),
          assets: [ $release.assets[] | select(.name | test("(\\.tar\\.gz|\\.zip)$")) | {name, download_count} ]
        } | @json
      end
  ' "$tmp_dir/releases.json"
)"

platform_summary="$(
  jq -r '
    [
      .[].assets[]
      | select(.name | test("(\\.tar\\.gz|\\.zip)$"))
      | {
          platform: (
            if (.name | test("darwin-amd64")) then "macOS Intel"
            elif (.name | test("darwin-arm64")) then "macOS Apple Silicon"
            elif (.name | test("linux-amd64")) then "Linux x64"
            elif (.name | test("linux-arm64")) then "Linux ARM64"
            elif (.name | test("windows-amd64")) then "Windows x64"
            else .name
            end
          ),
          download_count
        }
    ]
    | group_by(.platform)
    | map({platform: .[0].platform, downloads: (map(.download_count) | add)})
    | sort_by(-.downloads, .platform)
    | @json
  ' "$tmp_dir/releases.json"
)"

release_summary="$(
  jq -r '
    [
      .[]
      | {
          tag: .tag_name,
          published_at,
          archive_downloads: ([ .assets[] | select(.name | test("(\\.tar\\.gz|\\.zip)$")) | .download_count ] | add // 0)
        }
    ]
    | sort_by(.published_at)
    | reverse
    | @json
  ' "$tmp_dir/releases.json"
)"

total_archive_downloads="$(jq -r '[.[].assets[] | select(.name | test("(\\.tar\\.gz|\\.zip)$")) | .download_count] | add // 0' "$tmp_dir/releases.json")"

echo "Package: ${package_name}"
echo "Repository: ${repo_name}"
echo "Local npm version: ${local_version}"
echo
echo "NPM downloads"
echo "  Last day (${day_start}): ${day_downloads}"
echo "  Last week (${week_start} to ${week_end}): ${week_downloads}"
echo "  Last month (${month_start} to ${month_end}): ${month_downloads}"
echo "  Last non-zero day: ${last_active_day} (${last_active_count})"
echo
echo "GitHub release archives"
echo "  Total archive downloads across all releases: ${total_archive_downloads}"

if [[ "$latest_release_summary" == "missing" ]]; then
  echo "  Latest local version release: not found on GitHub"
else
  latest_tag="$(jq -r '.tag' <<<"$latest_release_summary")"
  latest_published_at="$(jq -r '.published_at' <<<"$latest_release_summary")"
  latest_archive_downloads="$(jq -r '.archive_downloads' <<<"$latest_release_summary")"
  echo "  Current version release (${latest_tag}, published ${latest_published_at}): ${latest_archive_downloads}"
  jq -r '.assets[] | "    - \(.name): \(.download_count)"' <<<"$latest_release_summary"
fi

echo
echo "Archive downloads by platform"
jq -r '.[] | "  - \(.platform): \(.downloads)"' <<<"$platform_summary"

echo
echo "Archive downloads by release"
jq -r '.[] | "  - \(.tag) (\(.published_at)): \(.archive_downloads)"' <<<"$release_summary"
