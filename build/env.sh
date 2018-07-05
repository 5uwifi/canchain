#!/bin/sh

set -e

if [ ! -f "build/env.sh" ]; then
    echo "$0 must be run from the root of the repository."
    exit 2
fi

# Create fake Go workspace if it doesn't exist yet.
workspace="$PWD/build/_workspace"
root="$PWD"
candir="$workspace/src/github.com/5uwifi"
if [ ! -L "$candir/canchain" ]; then
    mkdir -p "$candir"
    cd "$candir"
    ln -s ../../../../../. canchain
    cd "$root"
fi

# Set up the environment to use the workspace.
GOPATH="$workspace"
export GOPATH

# Run the command inside the workspace.
cd "$candir/canchain"
PWD="$candir/canchain"

# Launch the arguments with the configured environment.
exec "$@"
