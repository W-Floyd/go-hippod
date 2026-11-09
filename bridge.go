package hippo

// Converter turns the module's messages into NMEA the way the stock HAL does:
// a position sentence pair when the DR solution (0x30) arrives and the GSV set
// when the GNSS fix (0x31) arrives (gps_status, 0x7dea-0x8308: it builds
// $GPGGA/$GPRMC when the last message was 0x30/2 and $GPGSV when it was
// 0x31/2). Date and time come from the last UTC message (0x32), so nothing is
// emitted before one has been seen.
type Converter struct {
	// AltFromDR takes the altitude from RawDR when neither a 0x70/0x22 nor a
	// valid 0x31 altitude has been seen. A guess, and on the unit it was
	// measured to read about 170 m high, so it is off by default.
	AltFromDR bool

	// Extra also emits $GPGSA and $GPGST after each position set, which gpsd
	// uses for the fix mode, the DOPs, the used satellites and the errors.
	Extra bool

	// Speed, when fresh, is the speed of $GPRMC and $GPVTG in place of the
	// module's, the only sentences that carry one; never without a fix.
	Speed *CarSpeed

	// CourseStatus leaves the course of $GPRMC and $GPVTG empty while the DR
	// solution's heading status (0x30 Flags2 bit 2) is clear, so a reader
	// sees no track rather than a heading the module does not vouch for.
	// Which way the bit reads on the unit is not captured yet: off by
	// default, the DR bearing always goes out, as from the HAL.
	CourseStatus bool

	utc   *UTC
	raw   *RawDR
	fix   *GPSFix
	ext   *ExtGPSFix
	used  *ExtUsedSV
	chans [32]*Channel
}

// Feed takes one decoded message and returns the NMEA sentences it triggers.
func (c *Converter) Feed(m Message) []string {
	switch m := m.(type) {
	case *UTC:
		c.utc = m
	case *GPSFix:
		c.fix = m
		return c.gsv()
	case *Channel:
		c.chans[m.Index] = m
	case *ExtGPSFix:
		c.ext = m
	case *ExtUsedSV:
		c.used = m
	case *RawDR:
		c.raw = m
		return c.position()
	}
	return nil
}

// Quality is the GGA fix quality the HAL derives (0x7e3e-0x7e56): 6 (dead
// reckoning) when the DR solution flag is set, else 1 when the GNSS fix status
// is valid, else 0.
func (c *Converter) quality() int {
	switch {
	case c.raw != nil && c.raw.DRSolution():
		return 6
	case c.fix != nil && c.fix.Status == FixValid:
		return 1
	}
	return 0
}

func (c *Converter) mode() byte {
	switch c.quality() {
	case 6:
		return 'E'
	case 1:
		return 'A'
	}
	return 'N'
}

func (c *Converter) position() []string {
	if c.utc == nil || c.raw == nil {
		return nil
	}
	u, r := c.utc, c.raw
	q := c.quality()
	has := q != 0
	sats := int(u.Visible)
	if c.used != nil {
		sats = int(c.used.Count)
	}
	g := GGAData{
		Hour: int(u.Hour), Min: int(u.Min), Sec: int(u.Sec),
		HasPos: has, Lat: r.Lat, Lon: r.Lon, Quality: q, Sats: sats, HDOP: u.HDOP,
	}
	switch {
	case c.ext != nil:
		g.HasAlt, g.AltMSL, g.Sep, g.HasSep = true, c.ext.AltMSL, c.ext.AltHAE-c.ext.AltMSL, true
	case c.fix != nil && c.fix.AltValid:
		// Measured on the unit: the 0x31 altitude is the one that matches
		// the true height above sea level (metres, int16); the 0x30 and
		// 0x70/0x32 ones read about 170 m high. No separation is sent: the
		// module has none that agrees with it.
		g.HasAlt, g.AltMSL = true, float64(c.fix.Alt)
	case c.AltFromDR && bit(r.Flags2, 1):
		g.HasAlt, g.AltMSL = true, float64(r.Alt)
	}
	noCourse := c.CourseStatus && !bit(r.Flags2, 2)
	knots := r.Knots()
	if has {
		if k, ok := c.Speed.Knots(); ok {
			knots = k
		}
	}
	out := []string{
		GGA(g),
		RMC(RMCData{
			Hour: int(u.Hour), Min: int(u.Min), Sec: int(u.Sec),
			Day: int(u.Day), Month: int(u.Month), Year: int(u.Year),
			Valid: has, HasPos: has, Lat: r.Lat, Lon: r.Lon,
			Knots: knots, Course: r.Bearing, NoCourse: noCourse, Mode: c.mode(),
		}),
		vtg(r.Bearing, noCourse, knots, c.mode()),
	}
	if c.Extra {
		mode := 1
		switch {
		case !has:
		case g.HasAlt:
			mode = 3
		default:
			mode = 2
		}
		var ids []byte
		if has && c.used != nil {
			ids = c.used.IDs
		}
		out = append(out, GSA(GSAData{Mode: mode, IDs: ids, PDOP: u.PDOP, HDOP: u.HDOP, VDOP: u.VDOP})...)
		out = append(out, GST(GSTData{Hour: g.Hour, Min: g.Min, Sec: g.Sec, Pos: float64(r.PosAccuracy), Alt: float64(r.AltAccuracy)}))
	}
	return out
}

func (c *Converter) gsv() []string {
	if c.utc == nil {
		return nil
	}
	n := int(c.utc.Visible)
	if n > len(c.chans) {
		n = len(c.chans)
	}
	sats := make([]SatView, n)
	for i := range sats {
		if ch := c.chans[i]; ch != nil {
			sats[i] = SatView{PRN: int(ch.PRN), Elev: int(ch.Elevation), Az: int(ch.Azimuth), SNR: int(ch.SNR), Known: true}
		}
	}
	return GSV(sats, int(c.utc.Visible))
}
