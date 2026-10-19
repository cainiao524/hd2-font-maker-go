"""Test-only synthetic sparse-GID font and independent FontTools validation."""
import base64
import io
import json
from pathlib import Path
import sys

sys.dont_write_bytecode = True
from fontTools.fontBuilder import FontBuilder
from fontTools.feaLib.builder import addOpenTypeFeaturesFromString
from fontTools.pens.ttGlyphPen import TTGlyphPen
from fontTools.ttLib import TTFont


def generate():
    names = [".notdef"] + ["g%d" % i for i in range(1, 20)]
    fb = FontBuilder(1000, isTTF=True)
    fb.setupGlyphOrder(names)
    fb.setupCharacterMap({64+i: name for i, name in enumerate(names) if i})
    glyphs = {}
    for i, name in enumerate(names):
        pen = TTGlyphPen(None)
        pen.moveTo((20, 0))
        pen.lineTo((200+i, 500))
        pen.lineTo((400+i, 0))
        pen.closePath()
        glyphs[name] = pen.glyph()
    fb.setupGlyf(glyphs)
    fb.setupHorizontalMetrics({name: (600+i, 20) for i, name in enumerate(names)})
    fb.setupHorizontalHeader(ascent=800, descent=-200)
    fb.setupVerticalMetrics({name: (900+i, 30+i) for i, name in enumerate(names)})
    fb.setupVerticalHeader(ascent=800, descent=-200)
    fb.setupNameTable({"familyName": "Synthetic Sparse", "styleName": "Regular"})
    fb.setupOS2(sTypoAscender=800, sTypoDescender=-200, usWinAscent=800, usWinDescent=200)
    fb.setupPost()
    addOpenTypeFeaturesFromString(fb.font, """
        languagesystem DFLT dflt;
        feature liga { sub g1 g2 by g3; } liga;
        feature kern { pos g1 g2 -80; } kern;
        table GDEF { GlyphClassDef [g1 g2], [g3], [], []; } GDEF;
    """)
    fb.save(Path(__file__).resolve().parent / "testdata" / "sparse-20.ttf")
    print("Generated a synthetic 20-glyph TTF with hmtx, vmtx, post names and layout.")


def verify():
    reports = []
    for case in json.load(sys.stdin):
        source = TTFont(io.BytesIO(base64.b64decode(case["Source"])))
        output = TTFont(io.BytesIO(base64.b64decode(case["Output"])))
        output.ensureDecompiled()
        mapping = {int(old): new for old, new in case["Mapping"].items()}
        assert output["maxp"].numGlyphs == max(mapping.values()) + 1
        for old, new in mapping.items():
            a, b = source.getGlyphName(old), output.getGlyphName(new)
            assert source["hmtx"][a] == output["hmtx"][b]
            assert source["vmtx"][a] == output["vmtx"][b]
            assert a == b
        for cp, old_name in source.getBestCmap().items():
            assert output.getGlyphID(output.getBestCmap()[cp]) == mapping[source.getGlyphID(old_name)]
        ligature = output["GSUB"].table.LookupList.Lookup[0].SubTable[0].ligatures["g1"][0]
        assert output.getGlyphID(ligature.LigGlyph) == mapping[3]
        pair = output["GPOS"].table.LookupList.Lookup[0].SubTable[0].PairSet[0].PairValueRecord[0]
        assert output.getGlyphID(pair.SecondGlyph) == mapping[2] and pair.Value1.XAdvance == -80
        assert output["GDEF"].table.GlyphClassDef.classDefs["g3"] == 2
        reports.append({"offset": mapping[1], "glyph_slots": output["maxp"].numGlyphs, "passed": True})
    print(json.dumps(reports))


if __name__ == "__main__":
    {"generate": generate, "verify": verify}[sys.argv[1]]()
