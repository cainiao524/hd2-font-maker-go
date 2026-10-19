"""Optional test-only FontTools oracle; stdout is fonttools_golden.json.

All tables and outlines below are synthetic. No installed fonts are read.
"""
import io
import json
import struct

import fontTools
import pathops
from fontTools.feaLib.builder import addOpenTypeFeaturesFromString
from fontTools.fontBuilder import FontBuilder
from fontTools.pens.ttGlyphPen import TTGlyphPen
from fontTools.ttLib.sfnt import SFNTWriter


def sfnt(version, tables):
    output = io.BytesIO()
    writer = SFNTWriter(output, len(tables), sfntVersion=struct.pack(">I", version))
    for tag, data in sorted(tables.items()):
        writer[tag] = data
    writer.close()
    return output.getvalue()


head = bytearray(range(54))
head[:4] = struct.pack(">I", 0x10000)
head[8:12] = bytes.fromhex("12345678")
head[12:16] = bytes.fromhex("5f0f3cf5")
tables = {
    "head": bytes(head),
    "GSUB": bytes.fromhex("00010000000a000c000e000000000000"),
    "GPOS": bytes.fromhex("00010000000a000c000e000000000000"),
    "name": b"synthetic\x00\xff",
    "cvt ": bytes.fromhex("abcd1234ef"),
    "TEST": bytes.fromhex("112233"),
    "zero": b"",
}


def glyph_case(name, commands):
    path = pathops.Path()
    pen = TTGlyphPen(None)
    for command in commands:
        verb, *p = command
        if verb == 0:
            path.moveTo(*p)
        elif verb == 1:
            path.lineTo(*p)
        elif verb == 2:
            path.quadTo(*p)
        else:
            path.close()
    path.draw(pen)
    glyph = pen.glyph()
    encoded = glyph.compile(None)
    decoded = pathops.Path()
    glyph.draw(decoded.getPen(), None)
    verbs = {pathops.PathVerb.MOVE: 0, pathops.PathVerb.LINE: 1,
             pathops.PathVerb.QUAD: 2, pathops.PathVerb.CUBIC: 3, pathops.PathVerb.CLOSE: 4}
    return {
        "name": name,
        "commands": commands,
        "bounds": [glyph.xMin, glyph.yMin, glyph.xMax, glyph.yMax] if encoded else [0]*4,
        "coordinates": list(glyph.coordinates),
        "on_curve": [bool(flag & 1) for flag in glyph.flags],
        "ends": glyph.endPtsOfContours,
        "fonttools_bytes": encoded.hex(),
        "decoded_commands": [[verbs[verb], *[v for point in points for v in point]]
                             for verb, points in decoded],
    }


def integration_font():
    order = [".notdef", "A", "B", "lig"]
    builder = FontBuilder(1000, isTTF=True)
    builder.setupGlyphOrder(order)
    builder.setupCharacterMap({65: "A", 66: "B"})
    glyphs = {}
    for name, width in zip(order, (0, 100, 120, 200)):
        pen = TTGlyphPen(None)
        if width:
            pen.moveTo((0, 0))
            pen.lineTo((width, 0))
            pen.lineTo((width, 100))
            pen.lineTo((0, 100))
            pen.closePath()
        glyphs[name] = pen.glyph()
    builder.setupGlyf(glyphs)
    builder.setupHorizontalMetrics({name: (500, 0) for name in order})
    builder.setupHorizontalHeader(ascent=800, descent=-200)
    builder.setupNameTable({"familyName": "Synthetic", "styleName": "Regular",
                            "uniqueFontIdentifier": "Synthetic-Regular",
                            "fullName": "Synthetic Regular", "psName": "Synthetic-Regular"})
    builder.setupOS2(sTypoAscender=800, sTypoDescender=-200, usWinAscent=800, usWinDescent=200)
    builder.setupPost()
    builder.font["head"].created = builder.font["head"].modified = 2082844800
    builder.font.recalcTimestamp = False
    addOpenTypeFeaturesFromString(builder.font, """
        languagesystem DFLT dflt;
        feature liga { sub A B by lig; } liga;
        feature kern { pos A B -25; } kern;
    """)
    output = io.BytesIO()
    builder.font.save(output)
    return output.getvalue().hex()


result = {
    "fonttools_version": fontTools.__version__,
    "pathops_version": pathops.__version__,
    "integration_font": integration_font(),
    "fonts": [
        {"version": version, "tables": {tag: data.hex() for tag, data in tables.items()},
         "expected": sfnt(version, tables).hex()}
        for version in (0x10000, 0x4F54544F, 0x74727565)
    ],
    "glyphs": [
        glyph_case("empty", []),
        glyph_case("move_only", [[0, 10, 20], [4]]),
        glyph_case("rectangle", [[0, 0, 0], [1, 100, 0], [1, 100, 100], [1, 0, 100], [1, 0, 0], [4]]),
        glyph_case("ot_round", [[0, -1.5, -0.5], [1, 1.5, 2.5], [1, 0.49, -2.5], [4]]),
        glyph_case("quadratic", [[0, 0, 0], [2, 50, 100, 100, 0], [1, 0, 0], [4]]),
        glyph_case("implied_integer", [[0, 0, 0], [2, 10, 20, 20, 20], [2, 30, 20, 40, 0], [4]]),
        glyph_case("implied_half", [[0, 0, 0], [2, 10, 21, 20.5, 20.5], [2, 31, 20, 40, 0], [4]]),
        glyph_case("implied_after_round", [[0, 0, 0], [2, 10.2, 20.1, 20.2, 20.3], [2, 29.9, 19.8, 40, 0], [4]]),
        glyph_case("near_half_midpoint", [[0, 0, 0], [2, 10, 21, 20.500002, 20.500002], [2, 31, 20, 40, 0], [4]]),
        glyph_case("outside_midpoint_tolerance", [[0, 0, 0], [2, 10, 21, 20.5003, 20.5], [2, 31, 20, 40, 0], [4]]),
        glyph_case("mixed_wrap_midpoint", [[0, 5.5, 5], [2, 11, 0, 16, 5], [2, 20, 10, 10, 10], [2, 0, 10, 5.5, 5], [4]]),
        glyph_case("near_close_rounding", [[0, 0.4999, 0.4999], [1, 100, 0], [1, 100, 100], [1, 0.50001, 0.50001], [4]]),
        glyph_case("large_implied_midpoint", [[0, 20000, 20000], [2, 20001, 20003, 20002, 20003.5], [2, 20003, 20004, 20004, 20000], [4]]),
        glyph_case("all_off_curve", [[0, 5, 5], [2, 10, 0, 15, 5], [2, 20, 10, 10, 10], [2, 0, 10, 5, 5], [4]]),
        glyph_case("all_off_curve_half", [[0, 5.5, 5], [2, 11, 0, 15.5, 5], [2, 20, 10, 10, 10], [2, 0, 10, 5.5, 5], [4]]),
        glyph_case("two_contours", [[0, 500, 400], [1, 700, 400], [1, 600, 600], [4], [0, -300, -400], [1, -500, -400], [1, -400, -600], [4]]),
        glyph_case("signed_deltas", [[0, -32768, 0], [1, -1, 256], [1, 32766, -256], [4]]),
        glyph_case("non_implied", [[0, 0, 0], [2, 10, 20, 21, 20], [2, 30, 20, 40, 0], [4]]),
    ],
}
print(json.dumps(result, indent=2, ensure_ascii=True))
