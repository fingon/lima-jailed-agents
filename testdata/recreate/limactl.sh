#!/bin/sh
set -eu
export LJA_TEST_VM_PROCESS=1
exec "$LJA_TEST_BINARY" -test.run=^TestVMProcess$ -- "$@"
