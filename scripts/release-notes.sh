#!/usr/bin/env bash
# Prints the notes for a Playkeeper GitHub release. 0.x releases are labelled
# early.
# Usage: scripts/release-notes.sh VERSION OWNER/REPO INSTALL_URL ASSET_DIR
set -euo pipefail

version=$1
repo=$2
install_url=$3
dir=$4
tag=v$version
tar_sha=$(awk 'NR == 1 {print $1}' "$dir/playkeeper-linux-amd64.tar.gz.sha256")
get_sha=$(sha256sum "$dir/get.sh" | awk '{print $1}')

if [[ $version == 0.* ]]; then
  cat <<EOF
**Early release.** Playkeeper $version works in rehearsals on fresh Ubuntu 24.04 machines with automated test players, but it has not yet been run on a real provider VPS with the official Minecraft client. Keep your own copies of any backup you care about.

EOF
fi
cat <<EOF
## Install

On an Ubuntu 24.04 LTS x86_64 server:

\`\`\`bash
curl -fsSL $install_url | sudo sh
\`\`\`

The same installer, straight from GitHub:

\`\`\`bash
curl -fsSL https://github.com/$repo/releases/latest/download/get.sh | sudo sh
\`\`\`

Both download \`playkeeper-linux-amd64.tar.gz\` from the latest release, stop unless it matches its \`.sha256\`, and start the installer, which shows every change and asks before making it. Requirements, the manual steps and uninstalling: [README](https://github.com/$repo/blob/$tag/README.md#install-on-your-vps).

| File | SHA-256 |
| --- | --- |
| \`playkeeper-linux-amd64.tar.gz\` | \`$tar_sha\` |
| \`get.sh\` | \`$get_sha\` |

Source code: [\`$tag\`](https://github.com/$repo/tree/$tag), under the GNU AGPL-3.0. Not an official Minecraft product. Not approved by or associated with Mojang or Microsoft.
EOF
