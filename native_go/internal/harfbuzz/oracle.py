"""Test-only FontTools oracle; never imported or launched by product code."""
import base64
import copy
import io
import json
from pathlib import Path
import sys

sys.dont_write_bytecode = True
from fontTools.cffLib import FDArrayIndex, FDSelect, FontDict
from fontTools.designspaceLib import DesignSpaceDocument, AxisDescriptor, SourceDescriptor
from fontTools.feaLib.builder import addOpenTypeFeaturesFromString
from fontTools.fontBuilder import FontBuilder
from fontTools.pens.basePen import BasePen
from fontTools.pens.t2CharStringPen import T2CharStringPen
from fontTools.pens.ttGlyphPen import TTGlyphPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont, TTCollection
from fontTools.ttLib.tables._g_l_y_f import Glyph, GlyphComponent, UNSCALED_COMPONENT_OFFSET
from fontTools.ttLib.tables.TupleVariation import TupleVariation
from fontTools.varLib.instancer import instantiateVariableFont
from fontTools.varLib import build as build_variable

HERE = Path(__file__).resolve().parent
FIXTURES = HERE.parent / "testfixtures"
EXTRA = HERE / "testdata"


def generate():
    EXTRA.mkdir(exist_ok=True)
    font = TTFont(FIXTURES / "variable.ttf", recalcTimestamp=False)
    font.ensureDecompiled()
    FontBuilder(font=font).setupFvar([
        ("wght", 100, 400, 900, "Weight"), ("wdth", 75, 100, 125, "Width")], [])
    for name in font.getGlyphOrder():
        coords = font["glyf"][name].getCoordinates(font["glyf"])[0]
        deltas = [(40 if x > 20 else 0, 20 if y > 20 else 0) for x, y in coords]
        deltas += [(0, 0), (80, 0), (0, 0), (0, 0)]
        font["gvar"].variations[name].append(TupleVariation({"wdth": (0, 1, 1)}, deltas))
    font.save(EXTRA / "two-axis.ttf")
    for name, values in (("two-axis-default.ttf", {"wght": 400, "wdth": 100}),
                         ("two-axis-weight.ttf", {"wght": 900, "wdth": 100}),
                         ("two-axis-both.ttf", {"wght": 900, "wdth": 125})):
        instantiateVariableFont(font, values, inplace=False).save(EXTRA / name)

    font = TTFont(FIXTURES / "base.ttf", recalcTimestamp=False)
    original_order = list(font.getGlyphOrder())
    pen = TTGlyphPen(None)
    pen.moveTo((20, 0))
    pen.qCurveTo((60, 400), (300, 500), (400, 0))
    pen.closePath()
    font["glyf"]["curved"] = pen.glyph()
    pen = TTGlyphPen(font.getGlyphSet())
    pen.addComponent("u0041", (0.75, 0.25, -0.125, 1, 37, 61))
    font["glyf"]["transformed"] = pen.glyph()
    pen = TTGlyphPen({"transformed": None, "curved": None})
    pen.addComponent("transformed", (1, 0, 0, 1, 12, -17))
    pen.addComponent("curved", (0.5, 0, 0, 0.5, 400, 30))
    font["glyf"]["nested"] = pen.glyph()
    joined = Glyph()
    joined.numberOfContours = -1
    first = GlyphComponent()
    first.glyphName, first.x, first.y, first.flags = "u0041", 0, 0, UNSCALED_COMPONENT_OFFSET
    second = GlyphComponent()
    second.glyphName, second.firstPt, second.secondPt, second.flags = "u0056", 2, 0, 0
    joined.components = [first, second]
    font["glyf"]["pointMatched"] = joined
    names = ["curved", "transformed", "nested", "pointMatched"]
    font.setGlyphOrder(original_order + names)
    for name in names:
        font["glyf"][name].recalcBounds(font["glyf"])
        font["hmtx"].metrics[name] = (1000, font["glyf"][name].xMin)
    for table in font["cmap"].tables:
        if table.isUnicode():
            table.cmap.update({0xE100+i: name for i, name in enumerate(names)})
    font.save(EXTRA / "composites.ttf")

    names = [".notdef", "cid00001", "cid00002", "cid00003"]
    builder = FontBuilder(1000, isTTF=False)
    builder.setupGlyphOrder(names)
    builder.setupCharacterMap({66: names[1], 87: names[2], 0xE001: names[3]})
    charstrings = {}
    for i, name in enumerate(names):
        pen = T2CharStringPen(600, None)
        pen.moveTo((20, 0))
        pen.curveTo((20, 300+i*10), (400, 700), (500, 0))
        pen.closePath()
        charstrings[name] = pen.getCharString()
    builder.setupCFF("SyntheticCID", {"FullName": "Synthetic CID", "FamilyName": "Synthetic CID", "Weight": "Regular"}, charstrings, {})
    builder.setupHorizontalMetrics({name: (600, 20) for name in names})
    builder.setupHorizontalHeader(ascent=800, descent=-200)
    builder.setupNameTable({"familyName": "Synthetic CID", "styleName": "Regular"})
    builder.setupOS2(sTypoAscender=800, sTypoDescender=-200, usWinAscent=800, usWinDescent=200)
    builder.setupPost()
    font = builder.font
    top = font["CFF "].cff.topDictIndex[0]
    top.ROS = ("Adobe", "Identity", 0)
    top.CIDCount = len(names)
    top.FDArray = FDArrayIndex()
    fd = FontDict()
    fd.Private = top.Private
    top.FDArray.append(fd)
    top.FDSelect = FDSelect(format=3)
    top.FDSelect.gidArray = [0] * len(names)
    del top.Private
    addOpenTypeFeaturesFromString(font, """
        languagesystem DFLT dflt;
        languagesystem latn dflt;
        languagesystem cyrl dflt;
        feature liga { sub cid00001 cid00002 by cid00003; } liga;
        feature zz01 { sub cid00001 by cid00002; } zz01;
        feature kern { pos cid00001 cid00002 -120; } kern;
        table GDEF { GlyphClassDef [cid00001 cid00002], [cid00003], [], []; } GDEF;
    """)
    font.save(EXTRA / "cid.otf")
    collection = TTCollection()
    collection.fonts = [TTFont(FIXTURES / "base.ttf"), TTFont(FIXTURES / "donor.ttf")]
    collection.save(EXTRA / "collection.ttc")
    design = DesignSpaceDocument()
    axis = AxisDescriptor()
    axis.name, axis.tag, axis.minimum, axis.default, axis.maximum = "Weight", "wght", 400, 400, 900
    design.addAxis(axis)
    for weight, factor in ((400, 1), (900, 1.25)):
        master = TTFont(FIXTURES / "base.otf", recalcTimestamp=False)
        master.ensureDecompiled()
        top = master["CFF "].cff.topDictIndex[0]
        glyph_set = master.getGlyphSet()
        for name in master.getGlyphOrder():
            pen = T2CharStringPen(600 * factor, glyph_set)
            glyph_set[name].draw(TransformPen(pen, (factor, 0, 0, 1, 20 * (1 - factor), 0)))
            top.CharStrings[name] = pen.getCharString(private=top.Private, globalSubrs=top.GlobalSubrs)
            master["hmtx"].metrics[name] = (round(600 * factor), 20)
        source = SourceDescriptor()
        source.name, source.location, source.font = str(weight), {"Weight": weight}, master
        design.addSource(source)
    font, _, _ = build_variable(design)
    font.save(EXTRA / "variable-cff2.otf")
    instantiateVariableFont(font, {"wght": 900}, inplace=False, downgradeCFF2=True).save(EXTRA / "variable-cff2-900.otf")
    print("Generated synthetic multi-axis, quadratic, composite, point-matched, CID CFF, TTC and variable CFF2 fixtures.")


class PathPen(BasePen):
    def __init__(self, glyph_set):
        super().__init__(glyph_set)
        self.commands = []

    def add(self, verb, *points):
        self.commands.append({"Verb": verb, "Points": [n for p in points for n in p] + [0] * (6 - 2 * len(points))})

    def _moveTo(self, pt): self.add(0, pt)
    def _lineTo(self, pt): self.add(1, pt)
    def _qCurveToOne(self, p1, p2): self.add(2, p1, p2)
    def _curveToOne(self, p1, p2, p3): self.add(3, p1, p2, p3)
    def _closePath(self): self.add(4)


def normalized(commands):
    # HarfBuzz explicitly emits a closing line; pens may leave it implicit.
    result, start = [], None
    for command in commands or []:
        if command["Verb"] == 0: start = command["Points"][:2]
        if command["Verb"] == 4 and result and result[-1]["Verb"] == 1 and result[-1]["Points"][:2] == start:
            result.pop()
        result.append(command)
    return result


def compare_paths(got, want):
    got, want = normalized(got), normalized(want)
    assert len(got) == len(want), (got, want)
    for a, b in zip(got, want):
        assert a["Verb"] == b["Verb"], (a, b)
        assert all(abs(x-y) <= 0.002 for x, y in zip(a["Points"], b["Points"])), (a, b)


def verify():
    payload = json.load(sys.stdin)
    report = {"fonttools": __import__("fontTools").version, "checks": []}
    for case in payload:
        font = TTFont(io.BytesIO(base64.b64decode(case["Data"])), recalcTimestamp=False)
        font.ensureDecompiled()
        name = case["Name"]
        if name.startswith("offset"):
            assert font["maxp"].numGlyphs == 7
            cmap = {cp: font.getGlyphID(g) for cp, g in font.getBestCmap().items()}
            assert cmap[66] == 4 and cmap[87] == 5, cmap
            ligs, pairs = [], []
            for lookup in font["GSUB"].table.LookupList.Lookup:
                for sub in lookup.SubTable:
                    for first, values in getattr(sub, "ligatures", {}).items():
                        for lig in values:
                            ligs.append(([font.getGlyphID(first)] + [font.getGlyphID(g) for g in lig.Component], font.getGlyphID(lig.LigGlyph)))
            for lookup in font["GPOS"].table.LookupList.Lookup:
                for sub in lookup.SubTable:
                    if hasattr(sub, "PairSet"):
                        for first, pair_set in zip(sub.Coverage.glyphs, sub.PairSet):
                            for pair in pair_set.PairValueRecord:
                                pairs.append(([font.getGlyphID(first), font.getGlyphID(pair.SecondGlyph)], pair.Value1.XAdvance))
            classes = {font.getGlyphID(g): value for g, value in font["GDEF"].table.GlyphClassDef.classDefs.items()}
            assert ([4, 5], 6) in ligs, ligs
            assert ([4, 5], -120) in pairs, pairs
            assert classes == {4: 1, 5: 1, 6: 2}, classes
            report["checks"].append({"case": name, "cmap": cmap, "GSUB": ligs, "GPOS": pairs, "GDEF": classes})
        if case.get("Expected"):
            reference = TTFont(io.BytesIO(base64.b64decode(case["Expected"])), recalcTimestamp=False)
            assert "fvar" not in font and "gvar" not in font
            metrics = [font["hmtx"][g] for g in font.getGlyphOrder()]
            expected_metrics = [reference["hmtx"][g] for g in reference.getGlyphOrder()]
            assert metrics == expected_metrics, (name, metrics, expected_metrics)
        glyph_set = font.getGlyphSet()
        for gid, got in enumerate(case.get("Paths") or []):
            pen = PathPen(glyph_set)
            glyph = font.getGlyphName(gid)
            if "glyf" in font and font["glyf"][glyph].isComposite():
                # Flatten from raw point attachment semantics. FontTools'
                # component pen path cannot draw firstPt/secondPt components.
                flat = copy.deepcopy(font["glyf"][glyph])
                coords, ends, flags = flat.getCoordinates(font["glyf"])
                flat.numberOfContours = len(ends)
                flat.coordinates, flat.endPtsOfContours, flat.flags = coords, ends, flags
                flat.draw(pen, font["glyf"])
            else:
                glyph_set[glyph].draw(pen)
            compare_paths(got, pen.commands)
            if case.get("Expected"):
                reference_set = reference.getGlyphSet()
                expected_pen = PathPen(reference_set)
                reference_set[reference.getGlyphName(gid)].draw(expected_pen)
                compare_paths(got, expected_pen.commands)
        if name == "cid-subset":
            for tag in ("GSUB", "GPOS"):
                table = font[tag].table
                assert {r.ScriptTag for r in table.ScriptList.ScriptRecord} == {"DFLT", "latn", "cyrl"}
            assert {r.FeatureTag for r in font["GSUB"].table.FeatureList.FeatureRecord} == {"liga", "zz01"}
        report["checks"].append({"case": name, "glyphs": font["maxp"].numGlyphs, "paths": len(case.get("Paths") or []), "passed": True})
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    {"generate": generate, "verify": verify}[sys.argv[1]]()
