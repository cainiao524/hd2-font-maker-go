"""Development-only synthetic fixtures and original fonts.register_font oracle."""
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import sys

sys.dont_write_bytecode = True
os.environ["HD2_FONT_PYTHON_ONLY"] = "1"
ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, os.environ.get("HD2_V13_REFERENCE_DIR", str(ROOT)))
from fontTools.ttLib import TTFont
from fontTools.pens.recordingPen import RecordingPen
from fonts import Face, register_font

HERE = Path(__file__).resolve().parent
FIXTURES = HERE.parent / "testfixtures"
IDENTITY_IDS = (1, 2, 4, 6, 16, 17, 21, 22)


def generate():
    out = HERE / "testdata"
    out.mkdir(exist_ok=True)
    for label, source in (("ttf", FIXTURES / "base.ttf"),
                          ("cff", FIXTURES / "base.otf"),
                          ("cid", HERE.parent / "harfbuzz" / "testdata" / "cid.otf")):
        font = TTFont(source, recalcTimestamp=False)
        for name_id in IDENTITY_IDS:
            font["name"].removeNames(nameID=name_id)
            font["name"].setName("Template-%s-English-%d" % (label, name_id), name_id, 3, 1, 0x409)
            font["name"].setName("\u6e2c\u8a66\u5b57\u9ad4-%d" % name_id, name_id, 3, 1, 0x404)
            font["name"].setName("\u30c6\u30b9\u30c8-%d" % name_id, name_id, 0, 4, 0)
            font["name"].setName("Mod\u00e8le-%d" % name_id, name_id, 1, 0, 0)
        font["name"].setName("Template unique should not survive", 3, 3, 1, 0x409)
        font["OS/2"].usWeightClass = 725
        font["OS/2"].usWidthClass = 3
        font["OS/2"].fsSelection = 0x223
        font["head"].macStyle = 3
        if "CFF " in font:
            cff = font["CFF "].cff
            cff.fontNames = ["Reference" + label.upper()]
            top = cff.topDictIndex[0]
            top.FamilyName = "Regular" if label == "cff" else "CID Template Family"
            top.FullName = "Template Full " + label.upper()
            top.Weight = "Bold"
            top.ItalicAngle = -12.5
        font.save(out / ("template-" + label + (".ttf" if label == "ttf" else ".otf")))
    print("Generated multilingual TTF, name-keyed CFF, and CID CFF templates.")


def snapshot(raw):
    font = TTFont(io.BytesIO(raw), recalcTimestamp=False, recalcBBoxes=False)
    result = {
        "names": sorted((n.platformID, n.platEncID, n.langID, n.nameID, n.string.hex()) for n in font["name"].names),
        "weight": font["OS/2"].usWeightClass,
        "width": font["OS/2"].usWidthClass,
        "selection": font["OS/2"].fsSelection,
        "macStyle": font["head"].macStyle,
    }
    if "CFF " in font:
        cff = font["CFF "].cff
        top = cff.topDictIndex[0]
        result["cff"] = {key: getattr(top, key, None) for key in ("FamilyName", "FullName", "Weight", "ItalicAngle", "FontMatrix", "FontBBox")}
        result["cff"]["fontNames"] = cff.fontNames
        result["charstrings"] = [top.CharStrings[name].bytecode.hex() for name in font.getGlyphOrder()]
        result["widths"] = [top.Private.defaultWidthX, top.Private.nominalWidthX]
        paths = []
        gs = font.getGlyphSet()
        for name in font.getGlyphOrder():
            pen = RecordingPen()
            gs[name].draw(pen)
            paths.append(pen.value)
        result["paths"] = paths
    return result


def verify():
    cases = json.load(sys.stdin)
    report = []
    for case in cases:
        source, template, native = (base64.b64decode(case[key]) for key in ("Source", "Template", "Native"))
        face = Face("Synthetic template", "other", template, frozenset(), hashlib.sha256(template).hexdigest())
        expected = register_font(source, face)
        got, want = snapshot(native), snapshot(expected)
        assert got == want, (case["Name"], {key: (got.get(key), want.get(key)) for key in got.keys() | want.keys() if got.get(key) != want.get(key)})
        before = snapshot(source)
        if "cff" in before:
            for key in ("charstrings", "widths", "paths"):
                assert got[key] == before[key], (case["Name"], "changed", key)
            for key in ("FontMatrix", "FontBBox"):
                assert got["cff"][key] == before["cff"][key]
        report.append({"case": case["Name"], "name_records": len(got["names"]), "charstrings": len(got.get("charstrings", [])), "passed": True})
    print(json.dumps({"oracle": "fonts.register_font / FontTools", "cases": report}, indent=2))


if __name__ == "__main__":
    {"generate": generate, "verify": verify}[sys.argv[1]]()
