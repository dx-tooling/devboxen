#!/usr/bin/env bash
#
# Regenerate ../seccomp.json, the devbox's seccomp profile: Docker's default profile with
# one change, no IPPROTO_MPTCP sockets (CVE-2026-98432, see docs/security.md).
#
# - Docker's default allows socket() for a list of address families, one rule each. Every
#   one of these rules gets a second condition: the protocol is not IPPROTO_MPTCP (262). The
#   families stay Docker's, so AF_VSOCK (40), for example, stays denied: vsock is not
#   network-namespaced, and in a box on a virtual machine it would reach the hypervisor.
# - socketcall() (the i386 multiplexer, whose arguments seccomp cannot inspect) is removed.
# - io_uring, which can also create sockets, is already denied by Docker's default.
#
# A seccomp filter cannot be removed from inside, and every process in the devbox inherits
# it, nested containers included. So no MPTCP listener can exist anywhere in the devbox,
# whatever root inside it does with sysctls or nftables.
#
#   seccomp/generate.sh [<moby/profiles git ref>]   # then: devboxen deploy --rebuild-boxes
set -euo pipefail

REF="${1:-4b8214fc4971e06172938a1f1554ccff6647ee80}" # moby/profiles main, 2026-10-12
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

curl -fsSL "https://raw.githubusercontent.com/moby/profiles/$REF/seccomp/default.json" |
  jq --indent 2 '
    .syscalls |= map(
        if (.names == ["socket"]) then
          .args += [{ "index": 2, "value": 262, "op": "SCMP_CMP_NE" }]
          | .comment = "devbox: the address family as in the Docker default, but no IPPROTO_MPTCP (262), CVE-2026-98432"
        else .names -= ["socketcall"] end
        | select((.names | length) > 0))
    | if ([.syscalls[] | select(.names == ["socket"])] | length) < 3
      then error("no per-family socket() rules found: the Docker default changed, check this script")
      else . end' >"$DIR/../seccomp.json"

echo "wrote $DIR/../seccomp.json from moby/profiles@$REF"
