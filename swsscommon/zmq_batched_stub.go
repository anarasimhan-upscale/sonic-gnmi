package swsscommon

// Temporary stubs so the package compiles when the SWIG wrap has not been
// regenerated with zmqProducerBatched{Set,Del} %inline helpers. RECORDS tests
// do not exercise these paths.

func ZmqProducerBatchedSet(arg1 ZmqProducerStateTable, arg2 VectorString, arg3 FieldValuePairsList) {
	panic("ZmqProducerBatchedSet: regenerate swsscommon SWIG wrap")
}

func ZmqProducerBatchedDel(arg1 ZmqProducerStateTable, arg2 VectorString) {
	panic("ZmqProducerBatchedDel: regenerate swsscommon SWIG wrap")
}
