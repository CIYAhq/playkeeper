#!/usr/bin/env bash
# Prints the notes for a Playkeeper GitHub release: what changed (the
# release's CHANGELOG.md section, from the signed manifest) and how to
# install or update. 0.x releases are labelled early.
# Usage: scripts/release-notes.sh VERSION OWNER/REPO INSTALL_URL ASSET_DIR
set -euo pipefail

version=$1
repo=$2
install_url=$3
dir=$4
tag=v$version
tar_sha=$(awk 'NR == 1 {print $1}' "$dir/playkeeper-linux-amd64.tar.gz.sha256")
get_sha=$(sha256sum "$dir/get.sh" | awk '{print $1}')
changes=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['notes'])" "$dir/playkeeper-release.json")

if [[ $version == 0.* ]]; then
  cat <<EOF
**Early release.** The owner has installed Playkeeper on a real VPS and joined with the official Minecraft client from another network. Not verified yet: a second person joining, surviving a reboot, and restoring on a separate machine. Keep your own copies of any backup you care about.

EOF
fi
cat <<EOF
## What changed

$changes

## Install or update

On an Ubuntu 24.04 LTS x86_64 server:

\`\`\`bash
curl -fsSL $install_url | sudo sh
\`\`\`

The same installer, straight from GitHub:

\`\`\`bash
curl -fsSL https://github.com/$repo/releases/latest/download/get.sh | sudo sh
\`\`\`

Both download \`playkeeper-linux-amd64.tar.gz\` from the latest release, stop unless it matches its \`.sha256\`, and start the installer, which shows every change and asks before making it. On a server that already runs Playkeeper, the same command upgrades it in place and keeps worlds, backups and settings. From 0.2.0 on, Settings in the dashboard shows new versions and installs them; it only installs releases whose \`playkeeper-release.json\` is signed with the release key built into your installed version. Requirements, the manual steps and uninstalling: [README](https://github.com/$repo/blob/$tag/README.md#install-on-your-vps).

| File | SHA-256 |
| --- | --- |
| \`playkeeper-linux-amd64.tar.gz\` | \`$tar_sha\` |
| \`get.sh\` | \`$get_sha\` |

Source code: [\`$tag\`](https://github.com/$repo/tree/$tag), under the GNU AGPL-3.0. Not an official Minecraft product. Not approved by or associated with Mojang or Microsoft.
EOF
