# CANChain

## Building the source

Building can requires both a Go (version 1.7 or later) and a C compiler.
You can install them using your favourite package manager.
Once the dependencies are installed, run

    make gcan

or, to build the full suite of utilities:

    make all

## Executables

The CANChain project comes with several wrappers/executables found in the `cmd` directory.

| Command    | Description |
|:----------:|-------------|
| **`gcan`** | Our main CANChain CLI client. |
| `bootnode` | Stripped down version of our CANChain client implementation that only takes part in the network node discovery protocol, but does not run any of the higher level application protocols. |
