# agi test fixtures

`exif_sample.jpg` is a ~1 KB JPEG that carries EXIF metadata, used by
`agi.image_test.go` to exercise the imagelib EXIF readers
(`decodeExifAsJSON`, `readerHasExif`).

It is one of the sample images shipped with the
[`github.com/rwcarlsen/goexif`](https://github.com/rwcarlsen/goexif) library
(`exif/samples/f7-exif.jpg`), which is distributed under the BSD 2-Clause
License (Copyright (c) 2012, Robert Carlsen & Contributors). The same library
is the EXIF decoder used at runtime, so the fixture is a faithful, license-
compatible representation of real EXIF input.
