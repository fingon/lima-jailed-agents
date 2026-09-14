#!/bin/sh
set -eu
export LJA_TEST_GPG_PROCESS=1
exec "$LJA_TEST_BINARY" -test.run=^TestGPGProcess$ -- "${0##*/}" "$@"
