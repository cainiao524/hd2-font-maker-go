"""Development-only CFF reader and independent conversion oracle over stdin.

Receives only synthetic tables and paths from the Go test. No font files are read.
"""
import base64
import io
import json
import math
import struct
import sys

import fontTools
from fontTools.cffLib import CFFFontSet
from fontTools.misc.psCharStrings import T2CharString
from fontTools.pens.recordingPen import RecordingPen
from fontTools.pens.reverseContourPen import ReverseContourPen
from fontTools.pens.t2CharStringPen import T2CharStringPen


def reference(glyph, private):
    recording = RecordingPen()
    opened = False
    for command in glyph["Path"] or []:
        verb = command["Verb"]
        p = [struct.unpack("f", struct.pack("f", v))[0] for v in command["Points"]]
        if verb == 0:
            if opened:
                recording.closePath()
            recording.moveTo(tuple(p[:2]))
            opened = True
        elif verb == 1:
            recording.lineTo(tuple(p[:2]))
        elif verb == 2:
            recording.qCurveTo(tuple(p[:2]), tuple(p[2:4]))
        elif verb == 3:
            recording.curveTo(tuple(p[:2]), tuple(p[2:4]), tuple(p[4:6]))
        elif verb == 4:
            recording.closePath()
            opened = False
        else:
            raise AssertionError(f"unknown fixture verb {verb}")
    if opened:
        recording.closePath()
    pen = T2CharStringPen(glyph["Advance"], None, roundTolerance=0.5)
    recording.replay(ReverseContourPen(pen) if glyph["Reverse"] else pen)
    result = pen.getCharString(private=private, globalSubrs=[], optimize=False)
    result.compile()
    decoded = T2CharString(bytecode=result.bytecode, private=private, globalSubrs=[])
    out = RecordingPen()
    decoded.draw(out)
    return decoded.width, out.value


def verify(table):
    cff = CFFFontSet()
    cff.decompile(io.BytesIO(base64.b64decode(table["Data"])), None, isCFF2=False)
    assert cff.fontNames == [table["Name"]]
    top = cff[table["Name"]]
    count = len(table["Glyphs"])
    assert top.charset == [".notdef"] + [f"gid{i}" for i in range(1, count)]
    assert len(top.CharStrings.charStrings) == count
    assert top.FontBBox == table["BBox"]
    expected_matrix = [1 / table["UPM"], 0, 0, 1 / table["UPM"], 0, 0]
    assert all(math.isclose(a, b, rel_tol=1e-14, abs_tol=1e-18) for a, b in zip(top.FontMatrix, expected_matrix))
    assert top.CharstringType == 2
    assert top.Private.defaultWidthX == 0
    assert top.Private.nominalWidthX == 0
    assert len(cff.GlobalSubrs) == 0
    assert not hasattr(top.Private, "Subrs")
    for name, glyph in zip(top.charset, table["Glyphs"]):
        actual = top.CharStrings[name]
        outline = RecordingPen()
        actual.draw(outline)
        width, expected = reference(glyph, top.Private)
        assert actual.width == width == glyph["Advance"], (glyph["Label"], actual.width, width)
        assert outline.value == expected, (glyph["Label"], outline.value, expected)
    return count


request = json.load(sys.stdin)
count = sum(verify(table) for table in request["Tables"])
print(json.dumps({"fontTools": fontTools.version, "tables": len(request["Tables"]), "glyphs": count, "outlines_and_widths": "exact"}))
