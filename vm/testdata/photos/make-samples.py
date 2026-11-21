#!/usr/bin/env python3
# Rebuilds the sample photos of the smart photos tests (vm/e2e.d/96-smartphotos.sh)
# from the originals listed in LICENSE.md: scaled to 800 px, with a date taken
# and GPS coordinates written into their EXIF data (made up, for the tests).
# Needs Pillow. Run: python3 make-samples.py <folder of the originals>
import os, sys
from fractions import Fraction
from PIL import Image

src = sys.argv[1]
SAMPLES = {  # name: (date taken, latitude, longitude)
    "obama-portrait": ("2012:12:06 10:00:00", 38.8977, -77.0365),
    "obama-desk": ("2009:01:21 09:30:00", 38.8977, -77.0365),
    "obama-office": ("2009:01:21 11:00:00", 38.8977, -77.0365),
    "obama-biden": ("2015:06:15 15:00:00", 38.8977, -77.0365),
    "biden-portrait": ("2021:03:03 12:00:00", 39.7391, -75.5398),
    "biden-2013": ("2013:01:10 12:00:00", None, None),
    "dog": ("2019:05:04 17:20:00", 45.7640, 4.8357),
    "beach": ("2018:08:12 11:45:00", 13.5149, 144.8069),
    "car": ("2023:02:28 08:10:00", 48.8566, 2.3522),
}

def dms(v):
    v = abs(v)
    d = int(v); m = int((v - d) * 60); s = round(((v - d) * 60 - m) * 60 * 100)
    return (Fraction(d), Fraction(m), Fraction(s, 100))

for name, (date, lat, lon) in SAMPLES.items():
    img = Image.open(os.path.join(src, name + ".jpg")).convert("RGB")
    img.thumbnail((800, 800))
    exif = Image.Exif()
    exif[0x0110] = "Sample"
    exif.get_ifd(0x8769)[0x9003] = date  # DateTimeOriginal
    if lat is not None:
        gps = exif.get_ifd(0x8825)
        gps[1] = "N" if lat >= 0 else "S"
        gps[2] = dms(lat)
        gps[3] = "E" if lon >= 0 else "W"
        gps[4] = dms(lon)
    img.save(name + ".jpg", quality=82, exif=exif)
