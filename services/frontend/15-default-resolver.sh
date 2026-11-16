#!/bin/sh
# Auto-detect container DNS resolver (e.g., 127.0.0.11 in Docker, kube-dns in GKE)
# unless NGINX_RESOLVER is explicitly set.
if [ -z "${NGINX_RESOLVER:-}" ]; then
  NGINX_RESOLVER=$(awk '/^nameserver[[:space:]]+/{print $2; exit}' /etc/resolv.conf)
  export NGINX_RESOLVER="${NGINX_RESOLVER:-127.0.0.11}"
fi
