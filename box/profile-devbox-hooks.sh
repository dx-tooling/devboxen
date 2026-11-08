# devbox: one line per project start hook (.devbox/on-start) that failed, was skipped as
# untrusted or changed, or still runs. Login shells of dev only; see devbox-hooks.
if [ -n "${PS1:-}" ] && [ "$(id -u)" -ne 0 ] && command -v devbox-hooks >/dev/null 2>&1; then
  devbox-hooks notice || true
fi
