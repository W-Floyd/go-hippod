package hippo

// Class bytes of the frames the host sends, from the HAL's writers.
const (
	ClassSet   = 0x01
	ClassQuery = 0x02
	ClassReset = 0x03
)

// Message ids, from the HAL's dispatcher in hippo_parse (0x3c42-0x3c8e).
const (
	IDVersion   = 0x11
	IDLog       = 0x14
	IDDRControl = 0x21 // sub 1 DR engine rate, sub 2 constellation
	IDNMEACtl   = 0x22 // sent by the host only; the HAL has no reply handler
	IDProtocol  = 0x23
	IDRawDR     = 0x30
	IDGPSFix    = 0x31
	IDUTC       = 0x32
	IDChannel   = 0x33
	IDDPP       = 0x36
	IDExt       = 0x70
)

// StockFrame is one frame the stock HAL is known to send.
type StockFrame struct {
	Name string
	// Body is the frame body, without delimiters and checksum.
	Body []byte
	// Query marks a frame that only asks; everything else changes the module.
	Query bool
	// Safe marks the frames -query sends: a plain request for a value the
	// HAL reads at start-up.
	Safe bool
	// Src is where the bytes come from in gps.tcc803x.so.
	Src string
}

// Frame is the frame as it goes on the wire.
func (s StockFrame) Frame() []byte { return Encode(s.Body) }

// Stock is every frame the HAL sends, in one table. Nothing else is ever
// composed. The queries' meaning is the HAL's own log text; what the module
// does on the set frames beyond "change the protocol mask" is not known.
var Stock = []StockFrame{
	{"reset", []byte{ClassReset, 0x01}, false, false, "hipporeset, 0x698c-0x69ba"},
	{"query-version", []byte{ClassQuery, IDVersion, 0x01}, true, true, "0x7a8e-0x7ac2, log \"query fmversion\""},
	{"query-dr-rate", []byte{ClassQuery, IDDRControl, 0x01}, true, true, "0x7af4-0x7b2a"},
	{"query-constellation", []byte{ClassQuery, IDDRControl, 0x02}, true, true, "0x7ba0-0x7bd4, log \"query constellation\""},
	{"query-protocol", []byte{ClassQuery, IDProtocol, 0x02}, true, true, "0x7c38-0x7c6c, log \"query Output Protocol Control\""},
	{"poll-70-50", []byte{ClassQuery, IDExt, 0x50}, true, false, "query_hippo_msg, 0x6e16-0x6e48, called at 0x7ad8"},
	{"poll-70-51", []byte{ClassQuery, IDExt, 0x51}, true, false, "query_hippo_msg, called at 0x7ae0"},
	{"set-nmea-control", []byte{ClassSet, IDNMEACtl, 0x02, 0x00, 0x09, 0x01, 0x00, 0x00}, false, false, "0x5854-0x58ac, log \"set NMEA Output Control Parameters\"; payload constant, fields unnamed"},
	{"set-protocol-3", SetProtocolMask(3), false, false, "0x58f0-0x5954, log \"set Output Protocol Control\""},
}

// StockByName finds a StockFrame.
func StockByName(name string) (StockFrame, bool) {
	for _, s := range Stock {
		if s.Name == name {
			return s, true
		}
	}
	return StockFrame{}, false
}

// Query is the body of a query for message id with sub-type sub.
func Query(id, sub byte) []byte { return []byte{ClassQuery, id, sub} }

// SetProtocolMask is the body that sets the module's output protocol mask
// (0x58f0-0x5954). The 02 05 00 around the mask are constants in the HAL; what
// each mask bit selects is not known (the HAL only ever sets 3).
func SetProtocolMask(mask byte) []byte {
	return []byte{ClassSet, IDProtocol, 0x02, 0x05, 0x00, mask}
}

// ReplayStock is the two set frames the HAL sends, in its order, when the
// module reports an output protocol mask other than 3 on the product models
// "wp_opio" and "opio" (0x57e6-0x5954): the NMEA control frame, then mask 3.
func ReplayStock() []StockFrame {
	a, _ := StockByName("set-nmea-control")
	b, _ := StockByName("set-protocol-3")
	return []StockFrame{a, b}
}

// SafeQueries are the frames -query sends.
func SafeQueries() []StockFrame {
	var out []StockFrame
	for _, s := range Stock {
		if s.Safe {
			out = append(out, s)
		}
	}
	return out
}
