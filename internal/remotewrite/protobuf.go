package remotewrite

import (
	"encoding/binary"
	"math"
)

// encodeWriteRequest hand-encodes a prometheus.WriteRequest protobuf:
//
//	WriteRequest { repeated TimeSeries timeseries = 1; }
//	TimeSeries   { repeated Label labels = 1; repeated Sample samples = 2; }
//	Label        { string name = 1; string value = 2; }
//	Sample       { double value = 1; int64 timestamp = 2; }
func encodeWriteRequest(batch []Series) []byte {
	var out []byte
	for _, s := range batch {
		var ts []byte
		for _, l := range s.Labels {
			var lb []byte
			lb = appendBytesField(lb, 1, []byte(l.Name))
			lb = appendBytesField(lb, 2, []byte(l.Value))
			ts = appendBytesField(ts, 1, lb)
		}
		for _, p := range s.Points {
			var sb []byte
			sb = binary.AppendUvarint(sb, 1<<3|1) // field 1, wire type fixed64
			sb = binary.LittleEndian.AppendUint64(sb, math.Float64bits(p.Value))
			sb = binary.AppendUvarint(sb, 2<<3|0) // field 2, wire type varint
			sb = binary.AppendUvarint(sb, uint64(p.TsMs))
			ts = appendBytesField(ts, 2, sb)
		}
		out = appendBytesField(out, 1, ts)
	}
	return out
}

// appendBytesField appends a length-delimited (wire type 2) field.
func appendBytesField(b []byte, field uint64, v []byte) []byte {
	b = binary.AppendUvarint(b, field<<3|2)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}
