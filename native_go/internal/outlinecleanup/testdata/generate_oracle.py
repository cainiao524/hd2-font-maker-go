"""Development-only golden generator; Go tests need neither Python nor fontTools."""
import hashlib
import importlib.util
import json
import random
import struct
from pathlib import Path

from fontTools.pens.ttGlyphPen import TTGlyphPen

HERE = Path(__file__).resolve().parent
SOURCE = HERE.parents[3] / "outline_cleanup.py"
spec = importlib.util.spec_from_file_location("_cleanup_source_oracle", SOURCE)
oracle = importlib.util.module_from_spec(spec)
spec.loader.exec_module(oracle)


def command(verb, *values):
    return {"Verb": verb, "Points": [struct.unpack("f", struct.pack("f", v))[0] for v in values] + [0.0] * (6 - len(values))}


def path(contours):
    output = []
    for contour in contours:
        if not contour:
            continue
        output.append(command(0, *contour[0][0]))
        for segment in contour:
            output.append(command(len(segment) - 1, *(v for p in segment[1:] for v in p)))
        output.append(command(4))
    return output


def decode(commands):
    pen = oracle.OutlinePen()
    for cmd in commands:
        verb, coords = cmd["Verb"], cmd["Points"]
        points = [tuple(coords[i:i + 2]) for i in range(0, 6, 2)]
        if verb == 0:
            pen.moveTo(points[0])
        elif verb == 1:
            pen.lineTo(points[0])
        elif verb == 2:
            pen.qCurveTo(*points[:2])
        elif verb == 3:
            pen.curveTo(*points)
        else:
            pen.closePath()
    return pen.contours


cases = []


def add(name, commands):
    cleaned = [c for contour in decode(commands) if (c := oracle.clean_contour(contour))]
    cases.append({"name": name, "input": commands, "expected": path(cleaned)})


def contour_case(name, segments):
    add(name, path([segments]))


add("empty", [])
add("move-close", [command(0, 2.5, -3.5), command(4)])
add("implicit-closing-edge", [command(0, 0, 0), command(1, 20, 0), command(1, 20, 20), command(4)])
contour_case("zero-edges", [((0, 0), (0, 0)), ((0, 0), (0, 0), (0, 0))])
contour_case("pixel-corners", [((0, 0), (8, 0)), ((8, 0), (16, 0)), ((16, 0), (16, 8)), ((16, 8), (8, 8)), ((8, 8), (8, 16)), ((8, 16), (0, 16)), ((0, 16), (0, 0))])
contour_case("newly-exposed-convergence", [((0, 0), (20, 0)), ((20, 0), (21, 0), (21, 0)), ((21, 0), (20, 0)), ((20, 0), (40, 0), (45, 20)), ((45, 20), (45, 50)), ((45, 50), (0, 50)), ((0, 50), (0, 0))])
contour_case("needle-regression", [((745, 4), (747, -8)), ((747, -8), (743, 15)), ((743, 15), (700, 15)), ((700, 15), (745, 4))])
contour_case("wraparound-retrace", [((0, 0), (10, 0)), ((10, 0), (10, 10)), ((10, 10), (-10, 10)), ((-10, 10), (-10, 0)), ((-10, 0), (5, 0)), ((5, 0), (0, 0))])
contour_case("whole-retrace", [((0, 0), (10, 0)), ((10, 0), (0, 0))])
contour_case("straight-curves", [((0, 0), (5, 0), (10, 0)), ((10, 0), (10, 15), (10, -5), (10, 10)), ((10, 10), (0, 10)), ((0, 10), (0, 0))])
contour_case("regular-half-grid", [((0.5, -0.5), (2.5, 9.5), (10.5, 10.5)), ((10.5, 10.5), (15.5, 8.5), (12.5, 1.5), (10.5, -0.5)), ((10.5, -0.5), (0.5, -0.5))])
contour_case("singular-cubic-half-grid", [((0.5, -0.5), (0.5, -0.5), (20.5, 25.5), (40.5, 0.5)), ((40.5, 0.5), (40.5, -20.5)), ((40.5, -20.5), (0.5, -20.5)), ((0.5, -20.5), (0.5, -0.5))])
contour_case("singular-cubic-end", [((0, 0), (20, 40), (40, 0), (40, 0)), ((40, 0), (40, -20)), ((40, -20), (0, -20)), ((0, -20), (0, 0))])
contour_case("closed-curve-loop", [((0, 0), (10, 20), (-10, 20), (0, 0))])
contour_case("converging-quadratic", [((0, 0), (20, 30), (40, 0)), ((40, 0), (20, 30), (50, 30)), ((50, 30), (0, 30)), ((0, 30), (0, 0))])
for width in (0.5, 1, 1.0001, 1.5, 3):
    contour_case(f"needle-width-{width}", [((0, 0), (1000, 0)), ((1000, 0), (0, width)), ((0, width), (-20, 30)), ((-20, 30), (0, 0))])

outer = [((20, 0), (20, 700)), ((20, 700), (20, 700)), ((20, 700), (250, 700), (250, 700)), ((250, 700), (249, 700)), ((249, 700), (500, 700)), ((500, 700), (500, 0)), ((500, 0), (20, 0))]
inner = [((100, 100), (400, 100)), ((400, 100), (400, 600)), ((400, 600), (100, 600)), ((100, 600), (100, 100))]
add("separate-counter", path([outer, inner]))

rng = random.Random(20261018)
for index in range(192):
    start = (rng.randrange(-60, 61) / 2, rng.randrange(-60, 61) / 2)
    current = start
    segments = []
    for j in range(rng.randrange(3, 10)):
        count = rng.choice((2, 3, 4))
        points = [current] + [(rng.randrange(-80, 81) / 2, rng.randrange(-80, 81) / 2) for _ in range(count - 1)]
        if count == 4 and j % 3 == 0:
            points[1] = points[0]
        elif count == 4 and j % 3 == 1:
            points[-2] = points[-1]
        segments.append(tuple(points))
        current = points[-1]
    segments.append((current, start))
    contour_case(f"seeded-{index:03d}", segments)

# Capture each actual encoded-grid input in the existing implied-point regression.
pen = TTGlyphPen(None)
pen.moveTo((0, 0))
pen.lineTo((0, 100))
pen.qCurveTo((50, 100), (51, 100), (51, 99), (52, 99), (80, 80))
pen.lineTo((80, 0))
pen.closePath()
glyph = pen.glyph()
for attempt in range(8):
    encoded = oracle.OutlinePen()
    glyph.draw(encoded, None)
    add(f"encoded-half-grid-pass-{attempt}", path(encoded.contours))
    clean = [c for contour in encoded.contours if (c := oracle.clean_contour(contour))]
    if clean == encoded.contours:
        break
    pen = TTGlyphPen(None)
    oracle.draw_contours(clean, pen)
    glyph = pen.glyph()
else:
    raise AssertionError("Oracle encoded cleanup did not stabilize in eight passes")

data = {"source": "outline_cleanup.py (pure source, native override disabled)", "sha256": hashlib.sha256(SOURCE.read_bytes()).hexdigest(), "cases": cases}
(HERE / "oracle.json").write_text(json.dumps(data, separators=(",", ":")) + "\n", encoding="utf-8")
print(f"Generated {len(cases)} oracle paths; source sha256={data['sha256']}")
