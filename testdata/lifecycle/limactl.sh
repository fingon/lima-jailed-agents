#!/bin/sh
set -eu
if [ "$1" = list ]; then
    if [ "${LJA_TEST_LIST_FAIL:-0}" = 1 ]; then
        exit 21
    fi
    cat "$LJA_TEST_LIST"
    exit 0
fi
printf '%s\n' "$@" >> "$LJA_TEST_LOG"
if [ "${LJA_TEST_OPERATION_FAIL:-0}" = 1 ]; then
    exit 23
fi
