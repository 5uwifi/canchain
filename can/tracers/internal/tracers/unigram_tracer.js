{
    // hist is the map of opcodes to counters
    hist: {},
    // nops counts number of ops
    nops: 0,
    // step is invoked for every opcode that the VM executes.
    step: function(log, db) {
        var op = log.op.toString();
        if (this.hist[op]){
            this.hist[op]++;
        }
        else {
            this.hist[op] = 1;
        }
        this.nops++;
    },
    // fault is invoked when the actual execution of an opcode fails.
    fault: function(log, db) {},

    // result is invoked when all the opcodes have been iterated over and returns
    // the final result of the tracing.
    result: function(ctx) {
        if(this.nops > 0){
            return this.hist;
        }
    },
}
