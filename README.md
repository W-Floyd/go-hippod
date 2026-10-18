# Warning!

This is LLM generated.
That being said, I still think others might find it interesting, so here it is.

Caveat emptor.

# go-hippod

Reads the GNSS module of the Kia EV6 head unit (Mobis DAudio Plus Gen5 Wide,
Telechips TCC8031; the module is most likely a Trimble, running its "hippo"
protocol) and writes NMEA, so that `gpsd` can use it on mainline Linux.

## Why

The stock Android HAL (`gps.tcc803x.so`) does not read NMEA from the module. It
parses a checksummed binary protocol on the module's UART and formats its own
`$GPGGA`/`$GPRMC`/`$GPGSV`. gpsd has no decoder for that protocol (its Trimble
drivers are TSIP and TAIP), so something has to do the HAL's job.

## What

A library first (`package hippo`) with a thin command, `cmd/hippod`, around an
exported `Main`.

* `hippo.Encode`, `hippo.Decoder`: the frame codec (`81 body csum 82`,
  checksum `-(0x81+sum+0x82)`, `0x80` escapes), and a streaming decoder that
  resynchronises on garbage.
* `hippo.Stock`: every frame the HAL sends, in one table. Nothing else is ever
  composed.
* `hippo.Parse`: decodes the module's messages; anything it does not know comes
  back raw.
* `hippo.Converter`: turns them into NMEA the way the HAL does.
* `hippo.Run`, `hippo.OpenTTY`, `hippo.OpenPTY`: the I/O.

## Command

    hippod -dev /dev/ttyAMA3 [-baud 115200] [-out PATH|-|file:PATH] [-dump] [-query] [-replay-stock]

By default it only reads. `-out PATH` makes a pty (Linux) and a symlink to its
slave end at PATH, which gpsd opens like a serial port; `-out -` is stdout and
`file:PATH` appends to a file. `-dump` prints every frame as hex with its decoded
fields (unknown ids as hex too): the first capture on the unit is what to check
the tables below against. `-query` sends the four plain queries (version, output
protocol mask, constellation, DR rate) once, prints what comes back for `-wait`
and exits. The only frames that change the module are the two of
`-replay-stock` (the NMEA control frame and "protocol mask 3", what the HAL
sends on the product models `wp_opio` and `opio` when the module reports another
mask), and they are sent only if that flag is given.

The daemon owns the UART, so nothing else can read it. `-tap /run/hippod.sock`
serves every frame, as `-dump` prints it and with the time, on a unix socket
(mode 0660); `hippod -attach /run/hippod.sock` from another shell prints them live
until stopped, and any number can attach. A reader that cannot keep up loses lines
and never slows the daemon. `-tap` does not apply to `-query`.

## What is known

Every layout below was read from a disassembly of the HAL, never from a capture
or a specification. "HAL" addresses are in `gps.tcc803x.so` (ARM Thumb-2, 54568
bytes). Offsets are body indices: `b[0]` is the message id, `b[1]` the sub-type.
Confidence is for the layout, not for what the module will really send.

| message | what | traced from | confidence |
|---|---|---|---|
| frame, checksum, escapes | `81 .. csum 82`, csum seeded 3 | `calc_csum` 0x3790, `hippo_proc` 0x8bd4, `hippo_parse` 0x3b30 | high |
| host frames | the nine in `Stock` | writers at 0x5768, 0x58ac, 0x5954, 0x69ba, 0x6e48, 0x7ac2-0x7c6c | high (bytes), medium (names) |
| 0x11/1 version | major, minor, build, day, month, year | 0x3c92-0x3d84; the string reproduces the package's `6.12.7.2023621` | high |
| 0x14/1 log event | date/time, event, status codes | 0x3d8c-0x3e98 | high (layout), none (meaning of codes) |
| 0x14/2 | fatal error log | 0x3e9c | raw |
| 0x21/1 DR rate | `b[4]<<8\|b[5]` | 0x54ac | medium |
| 0x21/2 constellation | version `b[2]`, mask `b[3]<<8\|b[7]` | 0x5528 | medium, bit meanings unknown |
| 0x23/2 protocol | mask `b[4]` | 0x57a0 | medium, bit meanings unknown (the HAL wants 3) |
| 0x30/2 raw DR | flags, TOW, lat/lon (int32 x 180/2^31), alt, bearing, speed, accuracies, gyro/tacho counts | 0x1710-0x23b8 | high for offsets and scales, medium for units (speed is cm/s because x0.01943844 gives knots) |
| 0x31/2 GNSS fix | fix status, validity flags, lat/lon, alt, heading, speed, vertical speed, frequency offset | 0x24a8-0x2fba | high offsets; `b[22:24]` and `b[24:30]` not decoded |
| 0x32/1 UTC | date/time, PDOP/HDOP/VDOP (raw/256), time source, visible SV count | 0x3f24-0x42a2 | high; `b[9]`, `b[16]`, `b[18]` unread by the HAL |
| 0x33/1 channel | channel, PRN, flags, SNR x0.2, azimuth x2, elevation | 0x42a6-0x4656 | high offsets; `b[8]` unnamed |
| 0x36 DPP | not decoded | 0x465a | the HAL stores a constant 0.0 and ignores it |
| 0x70/0x22 | `alt_hae`, `alt_msl` (float64 at `b[30]`, `b[38]`) | 0x4ff0-0x50d0 | medium; the HAL's GGA altitude |
| 0x70/0x24 | used-SV count and ids | 0x50d2-0x51ae | medium |
| 0x70/others | 0x13, 0x14, 0x30, 0x31, 0x33, 0x50, 0x51 | dispatcher 0x46c8 | names only; bodies raw |

NMEA follows the HAL's `gps_status` (0x7dea-0x8308): `$GPGGA` and `$GPRMC` when a
0x30 message arrives, `$GPGSV` (four satellites per sentence, six at most) when a
0x31 arrives. Date and time are from the last 0x32, so nothing is written before
one. GGA quality is 6 (DR) when the DR flag of 0x30 is set, else 1 when the 0x31
fix status is `position valid` (0x11), else 0 (no position is written then).

### Where it deviates from the HAL, or guesses

* Coordinates are rounded to a millionth of a minute with carry; the HAL
  truncates.
* The HAL's magnetic variation comes from a sensor value outside these messages;
  the RMC fields are left empty. `$GPVTG` is added.
* GGA satellites: the count of 0x70/0x24 if one was seen, else the visible count
  of 0x32 (the HAL prints the visible count).
* GGA altitude is empty unless a 0x70/0x22 arrived; `-alt-dr` takes the DR
  altitude instead, which is a guess (its unit is not stated in the HAL).
* Whether the module sends 0x70 messages without being polled (`02 70 50`,
  `02 70 51`, in `Stock` but not in `-query`), and whether it streams
  unsolicited at power-up at all, is not known.

## First capture

Run `hippod -dev /dev/ttyAMA3 -dump` and compare: frames start `81` and end `82`
with a valid checksum; message ids in the table; versions `6.12.7.x`; plausible
dates in 0x32; lat/lon of 0x30 and 0x31 near each other once there is a fix;
`dropped` and `garbage` counters near zero. Anything else is a finding: an
unknown id prints as hex.

## Licence

MIT: see [LICENSE](LICENSE).
