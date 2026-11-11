package hippo

import "math"

// The module's side, for an emulated module (cmd/hippo-fake): message bodies
// built as Parse reads them, so the layouts stay in one place.

func put16(b []byte, i int, v uint16) { b[i], b[i+1] = byte(v), byte(v>>8) }
func put32(b []byte, i int, v uint32) { put16(b, i, uint16(v)); put16(b, i+2, uint16(v>>16)) }

func deg(v float64) uint32 { return uint32(int32(math.Round(v / degPerLSB))) }

// Body is the 0x11/1 version reply.
func (v *Version) Body() []byte {
	b := make([]byte, 9)
	b[0], b[1], b[2], b[3], b[4], b[5], b[6] = IDVersion, 1, v.Major, v.Minor, v.Build, v.Day, v.Month
	put16(b, 7, v.Year)
	return b
}

// ProtocolBody is the 0x23/2 protocol reply: the mask at b[4].
func ProtocolBody(mask byte) []byte { return []byte{IDProtocol, 2, 0, 0, mask} }

// Body is the 0x31/2 GNSS fix, 0x28 bytes.
func (g *GPSFix) Body() []byte {
	b := make([]byte, 0x28)
	b[0], b[1] = IDGPSFix, 2
	put32(b, 2, g.TOW)
	b[6] = g.Status & 0x3f
	if g.Alt3D {
		b[6] |= 1 << 6
	}
	if g.DGPS {
		b[6] |= 1 << 7
	}
	for i, v := range []bool{g.PosValid, g.AltValid, g.HeadValid, g.SpeedValid} {
		if v {
			b[7] |= 1 << i
		}
	}
	b[7] |= (g.TimeSource & 3) << 4
	put32(b, 8, deg(g.Lat))
	put32(b, 12, deg(g.Lon))
	put16(b, 16, uint16(g.Alt))
	put16(b, 18, uint16(math.Round(math.Mod(g.Heading+360, 360)/bearingPerLSB)))
	put16(b, 20, uint16(math.Round(g.Speed*100)))
	put16(b, 22, uint16(g.Unk22))
	put16(b, 0x1e, g.VertSpeed)
	put16(b, 0x20, g.VertSpeedAcc)
	put32(b, 0x22, uint32(g.FreqOffset))
	put16(b, 0x26, g.FreqOffsetAcc)
	return b
}

// Body is the 0x32/1 UTC and DOPs, 0x14 bytes.
func (u *UTC) Body() []byte {
	b := make([]byte, 0x14)
	b[0], b[1] = IDUTC, 1
	put16(b, 2, u.Year)
	b[4], b[5], b[6], b[7], b[8] = u.Month, u.Day, u.Hour, u.Min, u.Sec
	put16(b, 10, uint16(math.Round(u.PDOP/dopPerLSB)))
	put16(b, 12, uint16(math.Round(u.HDOP/dopPerLSB)))
	put16(b, 14, uint16(math.Round(u.VDOP/dopPerLSB)))
	b[0x11] = (u.TimeSource & 3) << 4
	b[0x13] = u.Visible
	return b
}

// Body is the 0x30/2 dead-reckoning solution, 0x30 bytes: the position the
// HAL reports (its GGA and RMC come from it, not from the fix).
func (r *RawDR) Body() []byte {
	b := make([]byte, 0x30)
	b[0], b[1], b[2], b[3], b[4] = IDRawDR, 2, r.Flags2, r.Flags3, r.AgeIndex
	put32(b, 5, r.TOW)
	put32(b, 9, deg(r.Lat))
	put32(b, 13, deg(r.Lon))
	put16(b, 17, uint16(r.Alt))
	put16(b, 19, uint16(math.Round(math.Mod(r.Bearing+360, 360)/bearingPerLSB)))
	put16(b, 21, r.SpeedRaw)
	put16(b, 23, r.DeltaTime)
	put16(b, 25, uint16(r.DeltaDistance))
	put16(b, 27, uint16(r.DeltaBearing))
	put16(b, 29, r.PosAccuracy)
	put16(b, 31, r.AltAccuracy)
	put16(b, 33, uint16(math.Round(r.HeadingAcc/bearingPerLSB)))
	put16(b, 35, r.SpeedAccuracy)
	put16(b, 37, r.GPSDeltaDistAcc)
	put16(b, 39, r.GPSDeltaHeadAcc)
	b[41] = r.GyroSamples & 0x7f
	if r.DirSwitch {
		b[41] |= 0x80
	}
	put32(b, 42, r.GyroCount)
	put16(b, 46, r.TachoCount)
	return b
}
