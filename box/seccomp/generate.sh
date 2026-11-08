#!/usr/bin/env bash
#
# Regenerate ../seccomp.json, the devbox's seccomp profile: Docker's default profile with
# one change, no IPPROTO_MPTCP sockets (CVE-2026-98432, see docs/security.md).
#
# - Every rule for socket() (the default only filters on the address family) and
#   socketcall() (the i386 multiplexer, whose arguments seccomp cannot inspect) is removed.
# - socket() is allowed again for any protocol except IPPROTO_MPTCP (262).
# - io_uring, which can also create sockets, is already denied by Docker's default.
#
# A seccomp filter cannot be removed from inside, and every process in the devbox inherits
# it, nested containers included. So no MPTCP listener can exist anywhere in the devbox,
# whatever root inside it does with sysctls or nftables.
#
#   seccomp/generate.sh [<moby/profiles git ref>]   # then redeploy (bin/deploy-devbox.sh)
set -euo pipefail

REF="${1:-4b8214fc4971e06172938a1f1554ccff6647ee80}" # moby/profiles main, 2026-10-12
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

curl -fsSL "https://raw.githubusercontent.com/moby/profiles/$REF/seccomp/default.json" |
  jq --indent 2 '
    .syscalls |= map(
        if (.names == ["socket"]) then empty else .names -= ["socket", "socketcall"] end
        | select((.names | length) > 0))
    | .syscalls += [{
        "names": ["socket"],
        "action": "SCMP_ACT_ALLOW",
        "args": [{ "index": 2, "value": 262, "op": "SCMP_CMP_NE" }],
        "comment": "devbox: every socket except IPPROTO_MPTCP (262), CVE-2026-98432"
      }]' >"$DIR/../seccomp.json"

echo "wrote $DIR/../seccomp.json from moby/profiles@$REF"
