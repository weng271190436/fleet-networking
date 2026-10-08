#!/bin/sh

set -eu

printf '%s\n' "${MEMBER_NAME:?MEMBER_NAME is required}" >/www/index.html
exec httpd -f -p 8080 -h /www
