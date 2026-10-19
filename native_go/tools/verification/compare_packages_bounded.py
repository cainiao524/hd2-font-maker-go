"""Independent 1.3 Python vs new-Go package oracle.

Only stdlib, fontTools and pathops are used for the semantic checks.  The Go
generator's own report is treated as metadata and for atlas associations; it is
never accepted as proof that glyphs, metrics or textures are equal.

Exit codes: 0 = requested semantics match (metadata differences are reported),
1 = semantic differences, 2 = incomplete/fatal audit.
"""
from __future__ import annotations

import argparse
from collections import Counter, defaultdict, OrderedDict
import copy
import gc
from datetime import datetime, timezone
import hashlib
import io
import json
import math
import os
from pathlib import Path
import shutil
import struct
import sys
import tempfile
import time
import zipfile

import fontTools
from fontTools.pens.areaPen import AreaPen
from fontTools.pens.boundsPen import BoundsPen
from fontTools.pens.recordingPen import DecomposingRecordingPen
from fontTools.pens.teePen import TeePen
from fontTools.ttLib import TTFont

try:
    import pathops
except ImportError:  # Reported as incomplete rather than silently accepted.
    pathops = None


SCHEMA = "hd2-python-vs-go-oracle-v1"
IMPLEMENTATION = "bounded-v2"
PREVIOUS_BOUNDED_SHA256 = "18f86b113d9e19b0a665f7779389b9b28fbdaf9a66a8a1aacb5843e9e477ec36"
BASE_COMPARATOR_SHA256 = "f17ee88a2fe1896790582f11343bd626b1da8ad1c6fca5980259f373630339ad"
HEADER = struct.Struct("<III20sQQ24s")
TYPE = struct.Struct("<IIQIIII")
ENTRY = struct.Struct("<7Q6I")
MAGIC = 0xF0000011
ENTRY_NAMES = ("NameID", "TypeID", "MainOffset", "StreamOffset", "GPUOffset",
               "Unknown0", "Unknown1", "MainSize", "StreamSize", "GPUSize",
               "MainAlignment", "GPUAlignment", "Index")
TYPE_NAMES = ("Unknown0", "Unknown1", "TypeID", "Count", "Unknown2", "Unknown3", "Unknown4")
IDENTITY_NAMES = {1, 2, 4, 6, 16, 17, 21, 22}
MAX_FIELDS = ("maxPoints", "maxContours", "maxCompositePoints", "maxCompositeContours",
              "maxComponentElements", "maxComponentDepth", "maxSizeOfInstructions")
KNOWN_BINDINGS = {
    0xe01b1c60bb279a7c: 0x1a87fa21dafbd90e, 0xfbb35bee675f2295: 0xf14daed0a7d38271,
    0x3da89f2c15b53d65: 0x86667f650dca00f9, 0x7c63fea1f706eec4: 0x3454c1d97f395663,
    0xb18e66ab16af6cd4: 0x5e4f7e7101016c99, 0xd5a757787d7dcb0f: 0x9c753f055238d29a,
    0xe007454455e2d2bb: 0x8d346dcdd08459d5, 0xfca7631255290a2c: 0x9ae590aec7c63b1c,
    0x7a40659072a8be3c: 0xa99cdca7ecee679e, 0xe6944ade8a140216: 0x529a0ff67a1c8a0b,
    0x28a718e0d452eff0: 0x8455f23e0a9bae87, 0x5e83c5f2a9b02110: 0xe732ccb97e7e201a,
    0xb56d2abac5d17df2: 0xd1ebb991c79f934b,
}


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def file_sha(path: Path) -> str:
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def hash_range(path: Path, offset: int, size: int) -> str:
    h = hashlib.sha256()
    with Path(path).open("rb") as stream:
        stream.seek(offset)
        remaining = size
        while remaining:
            block = stream.read(min(1024 * 1024, remaining))
            if not block:
                raise ValueError(f"short read in {path}")
            h.update(block)
            remaining -= len(block)
    return h.hexdigest()


def read_range(path: Path, offset: int, size: int) -> bytes:
    with Path(path).open("rb") as stream:
        stream.seek(offset)
        data = stream.read(size)
    if len(data) != size:
        raise ValueError(f"short resource read in {path}")
    return data


def resource_hash(text: str) -> int:
    raw = text.encode("utf-8")
    mask, mix = (1 << 64) - 1, 0xC6A4A7935BD1E995
    value = len(raw) * mix & mask
    end = len(raw) // 8 * 8
    for (word,) in struct.iter_unpack("<Q", raw[:end]):
        word = word * mix & mask
        word ^= word >> 47
        value = (value ^ (word * mix & mask)) * mix & mask
    if raw[end:]:
        value = (value ^ int.from_bytes(raw[end:], "little")) * mix & mask
    value ^= value >> 47
    value = value * mix & mask
    return value ^ (value >> 47)


KINDS = {resource_hash(kind): kind for kind in ("ttf", "otf", "runtime_font", "font", "texture")}


def unpack_header(data: bytes):
    if len(data) < HEADER.size:
        raise ValueError("truncated patch header")
    return HEADER.unpack_from(data)


def sfnt_size(data: bytes) -> int:
    if len(data) < 12 or data[:4] not in (b"\0\1\0\0", b"OTTO", b"true"):
        raise ValueError("invalid SFNT signature")
    count = struct.unpack_from(">H", data, 4)[0]
    end = 12 + 16 * count
    if end > len(data):
        raise ValueError("truncated SFNT directory")
    for i in range(count):
        off, size = struct.unpack_from(">II", data, 12 + 16 * i + 8)
        if off + size > len(data):
            raise ValueError("SFNT table outside resource")
        end = max(end, off + size)
    return end


def checksum_words(data: bytes, *, zero_head_adjustment_at: int | None = None) -> int:
    total = 0
    for i in range(0, len(data) - 3, 4):
        word = struct.unpack_from(">I", data, i)[0]
        if zero_head_adjustment_at is not None and i == zero_head_adjustment_at:
            word = 0
        total = (total + word) & 0xFFFFFFFF
    tail = data[len(data) // 4 * 4:]
    if tail:
        total = (total + int.from_bytes(tail + bytes(4 - len(tail)), "big")) & 0xFFFFFFFF
    return total


def sfnt_audit(data: bytes) -> dict:
    try:
        count = struct.unpack_from(">H", data, 4)[0]
        directory_end = 12 + 16 * count
        tables = {}
        errors = []
        head_adjust_offset = None
        for i in range(count):
            tag, checksum, off, size = struct.unpack_from(">4sIII", data, 12 + 16 * i)
            name = tag.decode("latin1")
            raw = data[off:off + size]
            table_checksum = checksum_words(raw, zero_head_adjustment_at=8 if tag == b"head" else None)
            if table_checksum != checksum:
                errors.append({"tag": name, "declared": checksum, "actual": table_checksum})
            tables[name] = {"sha256": sha(raw), "bytes": size, "offset": off}
            if tag == b"head":
                head_adjust_offset = off + 8
        whole = checksum_words(data, zero_head_adjustment_at=head_adjust_offset)
        return {"tables": tables, "table_checksum_errors": errors,
                "whole_checksum": whole, "whole_checksum_valid": whole == 0xB1B0AFBA,
                "directory_end": directory_end}
    except Exception as exc:
        return {"error": f"{type(exc).__name__}: {exc}", "tables": {},
                "table_checksum_errors": [], "whole_checksum": None, "whole_checksum_valid": False}


def normalise_point(point):
    if point is None:
        return None
    return tuple(round(float(value), 6) for value in point)


class ContourAreaPen(AreaPen):
    """Record every contour's signed area, independent of contour order/start."""

    def __init__(self, glyphset):
        super().__init__(glyphset)
        self.areas = []
        self.open_contours = 0

    def _moveTo(self, point):
        self.value = 0
        super()._moveTo(point)

    def _closePath(self):
        super()._closePath()
        self.areas.append(self.value)

    def _endPath(self):
        super()._endPath()
        self.open_contours += 1
        self.areas.append(self.value)


def recording_commands(font, name):
    glyph_set = cached_glyph_set(font)
    # A RecordingPen leaves addComponent(name, transform) operations intact.
    # Expand nested transforms before normalising coordinates. Preserve the
    # transform's actual winding (including reflections), never auto-reverse it.
    recording = DecomposingRecordingPen(glyph_set, skipMissingComponents=False,
                                       reverseFlipped=False)
    bounds = BoundsPen(glyph_set)
    area = ContourAreaPen(glyph_set)
    glyph_set[name].draw(TeePen(recording, bounds, area))
    commands = []
    for operator, points in recording.value:
        commands.append((operator, tuple(normalise_point(point) for point in points)))
    contour_areas = tuple(round(float(value), 6) for value in area.areas)
    directions = {
        "positive": sum(value > 0 for value in contour_areas),
        "negative": sum(value < 0 for value in contour_areas),
        "zero": sum(value == 0 for value in contour_areas),
        "open": area.open_contours,
    }
    return tuple(commands), bounds.bounds, float(area.value), contour_areas, directions


def cached_glyph_set(font):
    # The expensive CFF/glyf glyph-set object lives only as long as this pair.
    if not hasattr(font, "_oracle_glyph_set"):
        font._oracle_glyph_set = font.getGlyphSet()
    return font._oracle_glyph_set


class GeometryCache:
    def __init__(self):
        self.rows = OrderedDict()
        self.xor = OrderedDict()
        self.limit = 128
        self.peak_rows = 0
        self.peak_xor = 0
        self.fresh = 0
        self.reused = 0

    def glyph(self, font, font_sha, name):
        key = (font_sha, name)
        if key not in self.rows:
            if len(self.rows) >= self.limit:
                self.rows.popitem(last=False)
            self.rows[key] = recording_commands(font, name)
            self.fresh += 1
            self.peak_rows = max(self.peak_rows, len(self.rows))
        else:
            self.rows.move_to_end(key)
            self.reused += 1
        return self.rows[key]

    def xor_area(self, font_a, sha_a, name_a, font_b, sha_b, name_b):
        key = (sha_a, name_a, sha_b, name_b)
        if key in self.xor:
            self.xor.move_to_end(key)
            return self.xor[key]
        if pathops is None:
            if len(self.xor) >= self.limit:
                self.xor.popitem(last=False)
            self.xor[key] = None
            self.peak_xor = max(self.peak_xor, len(self.xor))
            return None
        p = pathops.Path()
        q = pathops.Path()
        glyph_set_a = cached_glyph_set(font_a)
        glyph_set_b = cached_glyph_set(font_b)
        glyph_set_a[name_a].draw(p.getPen(glyphSet=glyph_set_a))
        glyph_set_b[name_b].draw(q.getPen(glyphSet=glyph_set_b))
        try:
            value = float(pathops.op(p, q, pathops.PathOp.XOR).area)
        except Exception:
            value = None
        if len(self.xor) >= self.limit:
            self.xor.popitem(last=False)
        self.xor[key] = value
        self.peak_xor = max(self.peak_xor, len(self.xor))
        return value


def name_records(font):
    rows = []
    for record in font["name"].names:
        if record.nameID in IDENTITY_NAMES:
            try:
                text = record.toUnicode()
            except Exception:
                text = None
            rows.append((record.nameID, record.platformID, record.platEncID,
                         record.langID, record.string.hex(), text))
    return sorted(rows, key=lambda row: row[:4])


def cff_names(font):
    if "CFF " not in font:
        return None
    try:
        cff = font["CFF "].cff
        top = cff.topDictIndex[0]
        return {"fontNames": list(cff.fontNames),
                "FamilyName": getattr(top, "FamilyName", None),
                "FullName": getattr(top, "FullName", None)}
    except Exception as exc:
        return {"error": f"{type(exc).__name__}: {exc}"}


def font_snapshot(data: bytes) -> dict:
    font = TTFont(io.BytesIO(data), lazy=True, recalcBBoxes=False)
    try:
        order = font.getGlyphOrder()
        raw_metrics = font["hmtx"].metrics
        if isinstance(raw_metrics, dict):
            metrics = [raw_metrics[name] for name in order]
        else:
            metrics = list(raw_metrics)
        last_advance = metrics[-1][0] if metrics else 0
        advances = [metrics[i][0] if i < len(metrics) else last_advance for i in range(len(order))]
        bearings = [metrics[i][1] if i < len(metrics) else metrics[-1][1] for i in range(len(order))]
        cmap = {}
        cmap_conflicts = []
        variation_sequences = []
        for table in font["cmap"].tables:
            if table.format == 14:
                for selector, pairs in getattr(table, "uvsDict", {}).items():
                    variation_sequences.extend((int(cp), int(selector)) for cp, _ in pairs)
            elif table.isUnicode():
                for cp, name in table.cmap.items():
                    if cp in cmap and cmap[cp] != name:
                        cmap_conflicts.append([int(cp), cmap[cp], name])
                    cmap[int(cp)] = name
        os2 = font["OS/2"]
        head = font["head"]
        hhea = font["hhea"]
        maxp = font["maxp"]
        maxp_fields = {field: getattr(maxp, field, None) for field in MAX_FIELDS}
        maxp_fields["numGlyphs"] = getattr(maxp, "numGlyphs", None)
        maxp_fields["tableVersion"] = getattr(maxp, "tableVersion", None)
        return {
            "font": font,
            "data": data,
            "sha256": sha(data),
            "format": "ttf" if "glyf" in font else "cff" if "CFF " in font else "unknown",
            "glyph_order": order,
            "glyph_ids": {name: i for i, name in enumerate(order)},
            "glyph_count": len(order),
            "cmap": cmap,
            "cmap_conflicts": cmap_conflicts,
            "variation_sequences": sorted(set(variation_sequences)),
            "advances": advances,
            "bearings": bearings,
            "numberOfHMetrics": getattr(hhea, "numberOfHMetrics", None),
            "names": name_records(font),
            "cff_names": cff_names(font),
            "weight": getattr(os2, "usWeightClass", None),
            "width": getattr(os2, "usWidthClass", None),
            "fsSelection_mask_0x263": getattr(os2, "fsSelection", 0) & 0x263,
            "macStyle_mask_3": getattr(head, "macStyle", 0) & 3,
            "upm": getattr(head, "unitsPerEm", None),
            "head": {key: getattr(head, key, None) for key in
                     ("xMin", "yMin", "xMax", "yMax", "indexToLocFormat", "macStyle", "unitsPerEm")},
            "hhea": {key: getattr(hhea, key, None) for key in
                     ("numberOfHMetrics", "ascent", "descent", "lineGap", "advanceWidthMax",
                      "minLeftSideBearing", "minRightSideBearing", "xMaxExtent",
                      "caretSlopeRise", "caretSlopeRun", "caretOffset")},
            "os2": {key: getattr(os2, key, None) for key in
                    ("version", "sTypoAscender", "sTypoDescender", "sTypoLineGap",
                     "usWinAscent", "usWinDescent", "usWeightClass", "usWidthClass", "fsSelection")},
            "maxp": maxp_fields,
            "sfnt": sfnt_audit(data),
            "table_count": len(font.keys()),
        }
    except Exception:
        raise


def declared_ttf_bbox(font, name):
    if "glyf" not in font:
        return None, False, None
    try:
        glyph = font["glyf"][name]
        composite = bool(glyph.isComposite())
        if glyph.numberOfContours == 0:
            return None, composite, None
        declared = (glyph.xMin, glyph.yMin, glyph.xMax, glyph.yMax)
        coordinates, _, _ = glyph.getCoordinates(font["glyf"])
        actual = tuple(coordinates.calcIntBounds())
        return declared, composite, actual
    except Exception:
        return None, False, None


def diff_values(a, b, keys):
    result = {}
    for key in keys:
        av = a.get(key) if isinstance(a, dict) else getattr(a, key, None)
        bv = b.get(key) if isinstance(b, dict) else getattr(b, key, None)
        if av != bv:
            result[key] = {"python": av, "go": bv}
    return result


class Audit:
    def __init__(self, args):
        self.args = args
        self.issues = []
        self.report = {
            "schema": SCHEMA,
            "implementation": IMPLEMENTATION,
            "base_comparator_sha256": BASE_COMPARATOR_SHA256,
            "previous_bounded_sha256": PREVIOUS_BOUNDED_SHA256,
            "created_at_utc": datetime.now(timezone.utc).isoformat(),
            "fonttools_version": fontTools.__version__,
            "python_version": sys.version,
            "pathops_available": pathops is not None,
            "inputs": {},
            "scope": {},
            "summary": {},
            "packages": {},
            "members": {},
            "resources": {},
            "fonts": [],
            "atlases": [],
            "metadata": {},
            "issues": self.issues,
        }
        self.geometry = GeometryCache()
        self.snapshots = {}
        self.pair_results = {}
        self.started = time.perf_counter()
        out = getattr(args, "out", None)
        self.face_jsonl = Path(str(out) + ".faces.jsonl") if out is not None else None
        if self.face_jsonl is not None:
            self.face_jsonl.parent.mkdir(parents=True, exist_ok=True)
            mode = "w" if getattr(args, "force", False) else "x"
            with self.face_jsonl.open(mode, encoding="utf-8"):
                pass
            self.report["face_results_jsonl"] = str(self.face_jsonl.resolve())
        self.pair_stats = {"fresh_pairs": 0, "reused_faces": 0,
                           "glyph_records_peak": 0, "xor_records_peak": 0,
                           "open_fonts_peak": 0, "live_font_cache_after_pair": 0,
                           "fresh_codepoint_checks": 0, "reused_codepoint_checks": 0}

    def progress(self, event, **details):
        print(json.dumps({"event": event, "elapsed_seconds": round(time.perf_counter() - self.started, 3),
                          **details}, ensure_ascii=True), file=sys.stderr, flush=True)

    def save_face(self, **details):
        if self.face_jsonl is None:
            return
        record = {"implementation": IMPLEMENTATION,
                  "elapsed_seconds": round(time.perf_counter() - self.started, 3), **details}
        # Close each append so a reader can inspect every completed face even
        # while later comparisons run or the process is subsequently stopped.
        with self.face_jsonl.open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(record, ensure_ascii=False) + "\n")
            stream.flush()

    def issue(self, severity, category, **details):
        row = {"severity": severity, "category": category, **details}
        self.issues.append(row)
        return row

    def snapshot(self, data):
        key = sha(data)
        if key not in self.snapshots:
            self.snapshots[key] = font_snapshot(data)
            self.pair_stats["open_fonts_peak"] = max(self.pair_stats["open_fonts_peak"], len(self.snapshots))
        return self.snapshots[key]

    def release_pair(self):
        self.pair_stats["glyph_records_peak"] = max(self.pair_stats["glyph_records_peak"], self.geometry.peak_rows)
        self.pair_stats["xor_records_peak"] = max(self.pair_stats["xor_records_peak"], self.geometry.peak_xor)
        for snapshot in self.snapshots.values():
            font = snapshot["font"]
            if hasattr(font, "_oracle_glyph_set"):
                del font._oracle_glyph_set
            font.close()
            font.tables.clear()
            snapshot.clear()
        self.snapshots.clear()
        self.geometry = GeometryCache()
        gc.collect()
        self.pair_stats["live_font_cache_after_pair"] = len(self.snapshots)

    def compare_pair(self, label, data_a, data_b):
        key = (sha(data_a), sha(data_b))
        if key in self.pair_results:
            cached_row, cached_issues, first_label = self.pair_results[key]
            row = copy.deepcopy(cached_row)
            row["label"] = label
            row["pair_reuse"] = {"reused": True, "first_label": first_label,
                                 "python_sha256": key[0], "go_sha256": key[1]}
            for saved in cached_issues:
                issue = copy.deepcopy(saved)
                issue["font"] = label
                self.issues.append(issue)
            self.pair_stats["reused_faces"] += 1
            self.pair_stats["reused_codepoint_checks"] += row["geometry"]["codepoints_checked"]
            return row
        first_issue = len(self.issues)
        row = None
        try:
            snap_a, snap_b = self.snapshot(data_a), self.snapshot(data_b)
            row = compare_fonts(self, label, snap_a, snap_b)
        finally:
            # No TTFont, decoded glyph, path command or raw SFNT survives a pair.
            # Only the compact comparison result and its sampled findings persist.
            self.release_pair()
        if row["geometry"]["errors"] == 0:
            self.pair_results[key] = (copy.deepcopy(row), copy.deepcopy(self.issues[first_issue:]), label)
        row["pair_reuse"] = {"reused": False, "first_label": label,
                             "python_sha256": key[0], "go_sha256": key[1]}
        self.pair_stats["fresh_pairs"] += 1
        self.pair_stats["fresh_codepoint_checks"] += row["geometry"]["codepoints_checked"]
        return row


class Package:
    def __init__(self, zip_path: Path, work: Path):
        self.zip_path = zip_path.resolve()
        self.root = work / (zip_path.stem + "-" + hashlib.sha256(str(zip_path).encode()).hexdigest()[:8])
        self.root.mkdir(parents=True, exist_ok=True)
        self.members = {}
        self.report = {}
        self.resources = {}
        self.patches = {}
        with zipfile.ZipFile(self.zip_path) as archive:
            bad = archive.testzip()
            if bad is not None:
                raise ValueError(f"CRC failure in {zip_path}: {bad}")
            for info in archive.infolist():
                if info.is_dir():
                    continue
                target = (self.root / info.filename).resolve()
                if not target.is_relative_to(self.root.resolve()):
                    raise ValueError(f"unsafe ZIP member: {info.filename}")
                target.parent.mkdir(parents=True, exist_ok=True)
                with archive.open(info) as src, target.open("wb") as dst:
                    shutil.copyfileobj(src, dst, 1024 * 1024)
                self.members[info.filename] = {
                    "bytes": info.file_size, "compress_type": info.compress_type,
                    "compress_size": info.compress_size, "crc32": info.CRC,
                    "sha256": file_sha(target),
                }
        report_path = self.root / "font-build-report.json"
        if report_path.is_file():
            self.report = json.loads(report_path.read_text("utf-8-sig"))
        self._parse_patches()

    def _parse_patches(self):
        for main in sorted((self.root / "Fonts").glob("*.patch_0")):
            archive = main.name[:-len(".patch_0")]
            stream = self.root / "Fonts" / (archive + ".patch_0.stream")
            gpu = self.root / "Fonts" / (archive + ".patch_0.gpu_resources")
            with main.open("rb") as handle:
                header_data = handle.read(HEADER.size)
                header = unpack_header(header_data)
                if header[0] != MAGIC:
                    raise ValueError(f"bad patch magic: {main}")
                type_data = handle.read(TYPE.size * header[1])
                entry_data = handle.read(ENTRY.size * header[2])
            types = [TYPE.unpack_from(type_data, i * TYPE.size) for i in range(header[1])]
            entries = [ENTRY.unpack_from(entry_data, i * ENTRY.size) for i in range(header[2])]
            table_size = HEADER.size + TYPE.size * header[1] + ENTRY.size * header[2]
            sizes = [main.stat().st_size, stream.stat().st_size if stream.is_file() else 0,
                     gpu.stat().st_size if gpu.is_file() else 0]
            if header[4] != sizes[0] or header[5] != sizes[2]:
                raise ValueError(f"patch header sizes do not match companions: {archive}")
            for entry in entries:
                for channel, (offset, size) in enumerate(((entry[2], entry[7]), (entry[3], entry[8]), (entry[4], entry[9]))):
                    if channel == 0 and size and offset < table_size:
                        raise ValueError(f"resource overlaps index: {archive}")
                    if offset + size > sizes[channel]:
                        raise ValueError(f"resource outside companion: {archive}")
                kind = KINDS.get(entry[1], "unknown")
                key = (archive, entry[0], entry[1])
                self.resources[key] = {
                    "archive": archive, "entry": entry, "type": None, "kind": kind,
                    "main": main, "stream": stream, "gpu": gpu, "seq": entry[12],
                }
            type_map = {row[2]: row for row in types}
            for resource in self.resources.values():
                if resource["archive"] == archive:
                    resource["type"] = type_map.get(resource["entry"][1])
            self.patches[archive] = {"header": header, "types": types, "entries": entries,
                                     "table_size": table_size, "sizes": sizes,
                                     "main": main, "stream": stream, "gpu": gpu}

    def payload(self, resource, channel="main"):
        entry = resource["entry"]
        offset, size = {"main": (entry[2], entry[7]),
                        "stream": (entry[3], entry[8]),
                        "gpu": (entry[4], entry[9])}[channel]
        return read_range(resource[channel], offset, size)

    def ranges(self, resource, channel="main"):
        entry = resource["entry"]
        return {"main": (entry[2], entry[7]), "stream": (entry[3], entry[8]),
                "gpu": (entry[4], entry[9])}[channel]


def font_faces(package: Package, resource):
    data = package.payload(resource, "main")
    kind = resource["kind"]
    if kind in ("ttf", "otf"):
        if len(data) < 16 or struct.unpack_from("<I", data)[0] + 16 != len(data):
            raise ValueError("wrapped font length mismatch")
        size = sfnt_size(data[16:])
        return [{"slot": 0, "metrics": None, "data": data[16:16 + size],
                 "wrapper_size": struct.unpack_from("<I", data)[0], "raw_size": len(data)}]
    if kind == "runtime_font":
        if len(data) < 40:
            raise ValueError("runtime font header truncated")
        count, offset = struct.unpack_from("<II", data, 32)
        if not count or offset < 40 or offset + count * 16 > len(data):
            raise ValueError("runtime font directory invalid")
        result = []
        for i in range(count):
            metrics = struct.unpack_from("<3f", data, offset + i * 16)
            start = struct.unpack_from("<I", data, offset + i * 16 + 12)[0]
            if start < offset + count * 16:
                raise ValueError("runtime font start overlaps directory")
            size = sfnt_size(data[start:])
            result.append({"slot": i, "metrics": metrics, "data": data[start:start + size],
                           "wrapper_size": None, "raw_size": len(data)})
        return result
    raise ValueError(f"not a font resource: {kind}")


def font_descriptors(package: Package, resource):
    """Read directories only; a multi-slot runtime never materializes all fonts.

    Check every table extent and only allow zero alignment padding outside the
    SFNT extent. Pair hashes include the complete directory and all table bytes,
    excluding only this separately validated container/alignment padding.
    """
    path = resource["main"]
    base, length = package.ranges(resource)
    kind = resource["kind"]

    def read(offset, size):
        if offset < 0 or size < 0 or offset + size > length:
            raise ValueError("font container range exceeds declared MainSize")
        return read_range(path, base + offset, size)

    def zero_padding(start, end, max_count):
        if end < start or end - start > max_count or any(read(start, end - start)):
            raise ValueError("font extent/declared length mismatch or nonzero alignment padding")

    def extent(start, end):
        header = read(start, 12)
        if header[:4] not in (b"\0\1\0\0", b"OTTO", b"true"):
            raise ValueError("invalid SFNT signature")
        count = struct.unpack_from(">H", header, 4)[0]
        if count == 0 or start + 12 + 16 * count > end:
            raise ValueError("invalid SFNT directory length")
        directory = read(start + 12, 16 * count)
        final = 12 + 16 * count
        for tag, checksum, offset, size in struct.iter_unpack(">4sIII", directory):
            if offset < 12 + 16 * count or start + offset + size > end:
                raise ValueError("SFNT table exceeds its font slot")
            final = max(final, offset + size)
        return final

    if kind in ("ttf", "otf"):
        header = read(0, 16)
        declared = struct.unpack_from("<I", header)[0]
        if declared + 16 != length:
            raise ValueError("wrapped font length mismatch")
        size = extent(16, length)
        zero_padding(16 + size, length, 3)
        return [{"slot": 0, "metrics": None, "path": path, "offset": base + 16,
                 "size": size, "wrapper_size": declared, "raw_size": length,
                 "validated_length": True}]
    if kind != "runtime_font":
        raise ValueError("not a font container")
    header = read(0, 40)
    count, at = struct.unpack_from("<II", header, 32)
    if not count or at < 40 or at + count * 16 > length:
        raise ValueError("runtime font directory invalid")
    records = list(struct.iter_unpack("<3fI", read(at, count * 16)))
    starts = sorted(record[3] for record in records)
    if len(set(starts)) != count or starts[0] < at + count * 16:
        raise ValueError("runtime slot duplicate/overlap with directory")
    ends = dict(zip(starts, starts[1:] + [length]))
    result = []
    for slot, record in enumerate(records):
        start = record[3]
        if start % 16 or not all(math.isfinite(value) for value in record[:3]):
            raise ValueError("runtime font alignment or metrics invalid")
        size = extent(start, ends[start])
        zero_padding(start + size, ends[start], 15 if ends[start] < length else 3)
        result.append({"slot": slot, "metrics": record[:3], "path": path,
                       "offset": base + start, "size": size, "wrapper_size": None,
                       "raw_size": length, "validated_length": True})
    return result


def glyph_table(data: bytes):
    if len(data) < 88:
        raise ValueError("glyph table header truncated")
    count, offset, kerns, kern_offset, _ = struct.unpack_from("<5I", data, 68)
    if offset != 88 or kern_offset != 88 + 32 * count or len(data) != kern_offset + 12 * kerns:
        raise ValueError("glyph table layout mismatch")
    keys = list(struct.unpack_from(f"<{count}I", data, offset)) if count else []
    # Structure-of-arrays: all uint32 keys first, then packed seven-float rows.
    # 32 bytes per character = 4 key + 28 metrics, NOT a 32-byte metric stride.
    metrics = [tuple(struct.unpack_from("<7f", data, offset + 4 * count + i * 28)) for i in range(count)]
    header = {
        "size_height_base": tuple(struct.unpack_from("<3f", data, 8)),
        "inverse_dimensions": tuple(struct.unpack_from("<2f", data, 20)),
        "range": struct.unpack_from("<f", data, 36)[0],
        "default_metrics": tuple(struct.unpack_from("<7f", data, 40)),
        "count": count, "kern_count": kerns,
    }
    return {"header": header, "keys": keys, "metrics": metrics,
            "header_sha256": sha(data[:88]), "sha256": sha(data)}


def texture_info(package: Package, resource):
    main = package.payload(resource, "main")
    gpu_path = resource["gpu"]
    gpu_offset, gpu_size = package.ranges(resource, "gpu")
    if len(main) < 340 or main[192:196] != b"DDS ":
        raise ValueError("unsupported texture header")
    height, width = struct.unpack_from("<II", main, 204)
    mip_count = struct.unpack_from("<I", main, 220)[0]
    header_size = struct.unpack_from("<I", main, 320)[0]
    mips = []
    offset = 0
    for level in range(min(mip_count, 32)):
        w, h = max(1, width >> level), max(1, height >> level)
        size = w * h * 4
        digest = hash_range(gpu_path, gpu_offset + offset, size) if gpu_size >= offset + size else None
        mips.append({"level": level, "width": w, "height": h, "bytes": size, "sha256": digest})
        offset += size
    return {"width": width, "height": height, "mip_count": mip_count,
            "header_size": header_size, "gpu_bytes": gpu_size,
            "computed_gpu_bytes": offset, "mips": mips,
            "main_sha256": sha(main), "main_bytes": len(main)}


def compare_fonts(audit: Audit, label, fa, fb, *, depth_filter=None):
    font_a, font_b = fa["font"], fb["font"]
    row = {
        "label": label,
        "python": {"sha256": fa["sha256"], "format": fa["format"], "glyphs": fa["glyph_count"],
                   "cmap": len(fa["cmap"]), "upm": fa["upm"]},
        "go": {"sha256": fb["sha256"], "format": fb["format"], "glyphs": fb["glyph_count"],
               "cmap": len(fb["cmap"]), "upm": fb["upm"]},
    }
    semantic = []
    metadata = []
    if fa["format"] != fb["format"] or fa["upm"] != fb["upm"]:
        semantic.append({"kind": "font_format_or_upm", "python": [fa["format"], fa["upm"]],
                         "go": [fb["format"], fb["upm"]]})
    missing = sorted(set(fa["cmap"]) - set(fb["cmap"]))
    extra = sorted(set(fb["cmap"]) - set(fa["cmap"]))
    if missing or extra:
        semantic.append({"kind": "cmap_coverage", "missing_in_go": len(missing),
                         "extra_in_go": len(extra),
                         "missing_samples": [f"U+{cp:04X}" for cp in missing[:audit.args.max_samples]],
                         "extra_samples": [f"U+{cp:04X}" for cp in extra[:audit.args.max_samples]]})
    name_diff = []
    if fa["names"] != fb["names"]:
        a_rows = {row[:4]: row[4:] for row in fa["names"]}
        b_rows = {row[:4]: row[4:] for row in fb["names"]}
        for key in sorted(set(a_rows) | set(b_rows)):
            if a_rows.get(key) != b_rows.get(key):
                name_diff.append({"record": list(key), "python": a_rows.get(key), "go": b_rows.get(key)})
        semantic.append({"kind": "registration_names", "differences": len(name_diff),
                         "samples": name_diff[:audit.args.max_samples]})
    if fa["cff_names"] != fb["cff_names"]:
        metadata.append({"kind": "cff_names", "python": fa["cff_names"], "go": fb["cff_names"]})
    style_keys = ("weight", "width", "fsSelection_mask_0x263", "macStyle_mask_3")
    style_diff = {key: {"python": fa.get(key), "go": fb.get(key)} for key in style_keys if fa.get(key) != fb.get(key)}
    if style_diff:
        semantic.append({"kind": "style_metadata", **style_diff})

    common = sorted(set(fa["cmap"]) & set(fb["cmap"]))
    advance_mismatch = []
    bearing_mismatch = []
    for cp in common:
        name_a, name_b = fa["cmap"][cp], fb["cmap"][cp]
        gid_a, gid_b = fa["glyph_ids"][name_a], fb["glyph_ids"][name_b]
        adv_a, adv_b = fa["advances"][gid_a], fb["advances"][gid_b]
        if adv_a != adv_b:
            advance_mismatch.append({"cp": f"U+{cp:04X}", "python": adv_a, "go": adv_b, "delta": adv_b - adv_a})
        br_a, br_b = fa["bearings"][gid_a], fb["bearings"][gid_b]
        if br_a != br_b:
            bearing_mismatch.append({"cp": f"U+{cp:04X}", "python": br_a, "go": br_b})
    if advance_mismatch:
        semantic.append({"kind": "advance_width", "count": len(advance_mismatch),
                         "max_abs_delta": max(abs(item["delta"]) for item in advance_mismatch),
                         "samples": advance_mismatch[:audit.args.max_samples]})
    if bearing_mismatch:
        metadata.append({"kind": "left_side_bearing", "count": len(bearing_mismatch),
                         "samples": bearing_mismatch[:audit.args.max_samples]})

    line_diff = {}
    for group, keys in (
        ("hhea", ("ascent", "descent", "lineGap")),
        ("os2", ("sTypoAscender", "sTypoDescender", "sTypoLineGap",
                 "usWinAscent", "usWinDescent")),
    ):
        diff = diff_values(fa[group], fb[group], keys)
        if diff:
            line_diff[group] = diff
    if line_diff:
        semantic.append({"kind": "line_metrics", "differences": line_diff})
    head_bbox_diff = diff_values(fa["head"], fb["head"], ("xMin", "yMin", "xMax", "yMax"))
    if head_bbox_diff:
        metadata.append({"kind": "head_bounds", "differences": head_bbox_diff})
    hhea_other_diff = diff_values(fa["hhea"], fb["hhea"],
                                  ("advanceWidthMax", "minLeftSideBearing", "minRightSideBearing",
                                   "xMaxExtent", "caretSlopeRise", "caretSlopeRun", "caretOffset"))
    if hhea_other_diff:
        metadata.append({"kind": "hhea_metadata", "differences": hhea_other_diff})
    os2_other_diff = diff_values(fa["os2"], fb["os2"], ("version", "fsSelection"))
    if os2_other_diff:
        metadata.append({"kind": "os2_metadata", "differences": os2_other_diff})
    maxp_diff = diff_values(fa["maxp"], fb["maxp"], tuple(fa["maxp"]))
    if maxp_diff:
        metadata.append({"kind": "maxp_metadata", "differences": maxp_diff})

    geometry_exact = 0
    geometry_allowed = 0
    geometry_mismatch = []
    geometry_errors = []
    direction_mismatch = []
    contour_area_mismatch = []
    bbox_differences = []
    composite_bbox_differences = []
    unique_glyph_a = set()
    unique_glyph_b = set()
    for cp in common:
        name_a, name_b = fa["cmap"][cp], fb["cmap"][cp]
        unique_glyph_a.add(name_a)
        unique_glyph_b.add(name_b)
        try:
            commands_a, bounds_a, area_a, contours_a, directions_a = audit.geometry.glyph(font_a, fa["sha256"], name_a)
            commands_b, bounds_b, area_b, contours_b, directions_b = audit.geometry.glyph(font_b, fb["sha256"], name_b)
            # Direction is a separate requirement.  Filled-region XOR=0 alone
            # cannot excuse reversing an outer contour: the game's SDF path
            # cares about winding even when ordinary nonzero-fill previews do
            # not.  Contour order and cyclic start remain allowed; signed area
            # multisets and direction counts are order/start independent.
            if directions_a != directions_b:
                direction_mismatch.append({"cp": f"U+{cp:04X}", "python": directions_a,
                                           "go": directions_b})
            area_a_sorted = sorted(contours_a)
            area_b_sorted = sorted(contours_b)
            area_equal = (len(area_a_sorted) == len(area_b_sorted) and
                          all(abs(a - b) <= audit.args.contour_area_tolerance
                              for a, b in zip(area_a_sorted, area_b_sorted)))
            if not area_equal:
                contour_area_mismatch.append({"cp": f"U+{cp:04X}",
                                              "python": area_a_sorted,
                                              "go": area_b_sorted})
            if commands_a == commands_b:
                geometry_exact += 1
            else:
                delta = audit.geometry.xor_area(font_a, fa["sha256"], name_a,
                                                font_b, fb["sha256"], name_b)
                if delta is None:
                    geometry_errors.append({"cp": f"U+{cp:04X}", "reason": "pathops unavailable or failed"})
                elif (abs(delta) <= audit.args.area_tolerance and
                      directions_a == directions_b and area_equal):
                    geometry_allowed += 1
                else:
                    geometry_mismatch.append({"cp": f"U+{cp:04X}", "python_glyph": name_a,
                                              "go_glyph": name_b, "xor_area": delta,
                                              "filled_region_equal": abs(delta) <= audit.args.area_tolerance,
                                              "python_directions": directions_a,
                                              "go_directions": directions_b,
                                              "python_bounds": bounds_a, "go_bounds": bounds_b})
            if bounds_a != bounds_b and (bounds_a is not None or bounds_b is not None):
                bbox_differences.append({"cp": f"U+{cp:04X}", "python": bounds_a, "go": bounds_b})
            declared_a, composite_a, actual_a = declared_ttf_bbox(font_a, name_a)
            declared_b, composite_b, actual_b = declared_ttf_bbox(font_b, name_b)
            if composite_a or composite_b:
                if declared_a != declared_b or actual_a != actual_b:
                    composite_bbox_differences.append({"cp": f"U+{cp:04X}",
                        "python_declared": declared_a, "go_declared": declared_b,
                        "python_actual": actual_a, "go_actual": actual_b})
        except Exception as exc:
            geometry_errors.append({"cp": f"U+{cp:04X}", "error": f"{type(exc).__name__}: {exc}"})
    if geometry_mismatch:
        semantic.append({"kind": "glyph_geometry", "count": len(geometry_mismatch),
                         "samples": geometry_mismatch[:audit.args.max_samples]})
    if geometry_errors:
        semantic.append({"kind": "glyph_geometry_incomplete", "count": len(geometry_errors),
                         "samples": geometry_errors[:audit.args.max_samples]})
    if direction_mismatch:
        semantic.append({"kind": "glyph_contour_direction", "count": len(direction_mismatch),
                         "samples": direction_mismatch[:audit.args.max_samples]})
    if contour_area_mismatch:
        semantic.append({"kind": "glyph_contour_signed_area", "count": len(contour_area_mismatch),
                         "samples": contour_area_mismatch[:audit.args.max_samples]})
    if bbox_differences:
        metadata.append({"kind": "glyph_bounds", "count": len(bbox_differences),
                         "samples": bbox_differences[:audit.args.max_samples]})
    if composite_bbox_differences:
        metadata.append({"kind": "composite_bbox", "count": len(composite_bbox_differences),
                         "samples": composite_bbox_differences[:audit.args.max_samples]})

    table_a, table_b = fa["sfnt"]["tables"], fb["sfnt"]["tables"]
    tags_a, tags_b = set(table_a), set(table_b)
    table_diff = {tag: {"python": table_a.get(tag, {}).get("sha256"),
                        "go": table_b.get(tag, {}).get("sha256")}
                  for tag in sorted(tags_a & tags_b)
                  if table_a[tag].get("sha256") != table_b[tag].get("sha256")}
    if tags_a != tags_b:
        metadata.append({"kind": "table_tags", "missing_in_go": sorted(tags_a - tags_b),
                         "extra_in_go": sorted(tags_b - tags_a)})
    if table_diff:
        metadata.append({"kind": "table_bytes", "tags": sorted(table_diff),
                         "samples": {tag: table_diff[tag] for tag in list(sorted(table_diff))[:12]}})
    checksum_diff = {
        "python_whole_valid": fa["sfnt"]["whole_checksum_valid"],
        "go_whole_valid": fb["sfnt"]["whole_checksum_valid"],
        "python_table_errors": len(fa["sfnt"]["table_checksum_errors"]),
        "go_table_errors": len(fb["sfnt"]["table_checksum_errors"]),
    }
    if checksum_diff["python_whole_valid"] != checksum_diff["go_whole_valid"] or checksum_diff["python_table_errors"] != checksum_diff["go_table_errors"]:
        metadata.append({"kind": "sfnt_checksum", **checksum_diff})
    hmtx_layout = {
        "python_numberOfHMetrics": fa["numberOfHMetrics"],
        "go_numberOfHMetrics": fb["numberOfHMetrics"],
        "python_glyphs": fa["glyph_count"], "go_glyphs": fb["glyph_count"],
        "python_trailing_advance": fa["advances"][-1] if fa["advances"] else None,
        "go_trailing_advance": fb["advances"][-1] if fb["advances"] else None,
    }
    if fa["numberOfHMetrics"] != fb["numberOfHMetrics"]:
        metadata.append({"kind": "numberOfHMetrics", **hmtx_layout,
                         "note": "full long hmtx vs compressed trailing advances; advance values compared per codepoint"})
    if fa["glyph_count"] != fb["glyph_count"]:
        metadata.append({"kind": "glyph_count", "python": fa["glyph_count"], "go": fb["glyph_count"]})
    row.update({
        "cmap": {"missing_in_go": len(missing), "extra_in_go": len(extra),
                 "missing_samples": [f"U+{cp:04X}" for cp in missing[:audit.args.max_samples]],
                 "extra_samples": [f"U+{cp:04X}" for cp in extra[:audit.args.max_samples]]},
        "advances": {"checked": len(common), "mismatches": len(advance_mismatch),
                     "max_abs_delta": max((abs(item["delta"]) for item in advance_mismatch), default=0),
                     "samples": advance_mismatch[:audit.args.max_samples]},
        "geometry": {"codepoints_checked": len(common), "unique_python_glyphs": len(unique_glyph_a),
                     "unique_go_glyphs": len(unique_glyph_b), "exact_commands": geometry_exact,
                     "allowed_equivalents": geometry_allowed, "mismatches": len(geometry_mismatch),
                     "errors": len(geometry_errors),
                     "direction_mismatches": len(direction_mismatch),
                     "signed_area_mismatches": len(contour_area_mismatch),
                     "equivalence_policy": "GID and contour order/cyclic start may differ; direction and signed contour area must match. XOR=0 is evidence only and never excuses a direction reversal.",
                     "samples": geometry_mismatch[:audit.args.max_samples]},
        "bbox": {"path_bounds_differences": len(bbox_differences),
                 "composite_declared_or_actual_differences": len(composite_bbox_differences),
                 "samples": (composite_bbox_differences or bbox_differences)[:audit.args.max_samples]},
        "metadata": {"maxp": maxp_diff, "hmtx_layout": hmtx_layout,
                     "table_bytes_different_tags": sorted(table_diff), "checksums": checksum_diff,
                     "name3_note": "nameID 3 is implementation-unique and excluded from registration identity"},
        "semantic_differences": semantic, "metadata_differences": metadata,
        "geometry_cache": {"fresh": audit.geometry.fresh, "reused": audit.geometry.reused},
    })
    for item in semantic:
        audit.issue("semantic", "font_" + item["kind"], font=label, details=item)
    for item in metadata:
        audit.issue("metadata", "font_" + item["kind"], font=label, details=item)
    return row


def compare_glyph_tables(audit: Audit, label, ga, gb):
    row = {"label": label, "python": {"keys": len(ga["keys"]), "header": ga["header"],
                                        "sha256": ga["sha256"]},
           "go": {"keys": len(gb["keys"]), "header": gb["header"], "sha256": gb["sha256"]},
           "semantic_differences": [], "metadata_differences": []}
    if ga["keys"] != gb["keys"]:
        missing = sorted(set(ga["keys"]) - set(gb["keys"]))
        extra = sorted(set(gb["keys"]) - set(ga["keys"]))
        item = {"kind": "atlas_keys", "missing_in_go": len(missing), "extra_in_go": len(extra),
                "order_equal": ga["keys"] == gb["keys"],
                "missing_samples": missing[:audit.args.max_samples],
                "extra_samples": extra[:audit.args.max_samples]}
        row["semantic_differences"].append(item)
        audit.issue("semantic", "atlas_keys", table=label, details=item)
    if ga["header"] != gb["header"]:
        item = {"kind": "atlas_header", "python": ga["header"], "go": gb["header"]}
        row["semantic_differences"].append(item)
        audit.issue("semantic", "atlas_header", table=label, details=item)
    metric_diffs = []
    for index in range(min(len(ga["metrics"]), len(gb["metrics"]))):
        if ga["metrics"][index] != gb["metrics"][index]:
            key = ga["keys"][index] if index < len(ga["keys"]) else None
            metric_diffs.append({"index": index, "cp": key, "python": ga["metrics"][index], "go": gb["metrics"][index]})
    if len(ga["metrics"]) != len(gb["metrics"]) or metric_diffs:
        item = {"kind": "atlas_metrics", "count": len(metric_diffs),
                "lengths": [len(ga["metrics"]), len(gb["metrics"])],
                "samples": metric_diffs[:audit.args.max_samples]}
        row["semantic_differences"].append(item)
        audit.issue("semantic", "atlas_metrics", table=label, details=item)
    if ga["sha256"] != gb["sha256"] and not row["semantic_differences"]:
        item = {"kind": "atlas_payload_bytes", "python": ga["sha256"], "go": gb["sha256"]}
        row["metadata_differences"].append(item)
        audit.issue("metadata", "atlas_payload_bytes", table=label, details=item)
    return row


def compare_textures(audit: Audit, label, ta, tb):
    row = {"label": label, "python": ta, "go": tb, "semantic_differences": [], "metadata_differences": []}
    for key in ("width", "height", "mip_count", "header_size", "gpu_bytes", "computed_gpu_bytes"):
        if ta[key] != tb[key]:
            item = {"kind": "texture_header", "field": key, "python": ta[key], "go": tb[key]}
            row["semantic_differences"].append(item)
            audit.issue("semantic", "texture_header", texture=label, details=item)
    if ta["mip_count"] != tb["mip_count"] or len(ta["mips"]) != len(tb["mips"]):
        item = {"kind": "texture_mip_count", "python": ta["mip_count"], "go": tb["mip_count"]}
        row["semantic_differences"].append(item)
        audit.issue("semantic", "texture_mip_count", texture=label, details=item)
    mismatches = []
    for index in range(min(len(ta["mips"]), len(tb["mips"]))):
        a, b = ta["mips"][index], tb["mips"][index]
        if a != b:
            mismatches.append({"level": index, "python": a, "go": b})
    if mismatches:
        item = {"kind": "texture_mips", "count": len(mismatches),
                "samples": mismatches[:audit.args.max_samples]}
        row["semantic_differences"].append(item)
        audit.issue("semantic", "texture_mips", texture=label, details=item)
    if ta["main_sha256"] != tb["main_sha256"] and not row["semantic_differences"]:
        item = {"kind": "texture_header_bytes", "python": ta["main_sha256"], "go": tb["main_sha256"]}
        row["metadata_differences"].append(item)
        audit.issue("metadata", "texture_header_bytes", texture=label, details=item)
    return row


def find_resource(package: Package, archive, name_id, kind):
    for key, resource in package.resources.items():
        if key[0] == archive and key[1] == name_id and resource["kind"] == kind:
            return resource
    return None


def compare_packages(python_zip: Path, go_zip: Path, args):
    audit = Audit(args)
    # --work names a parent. Cleanup only removes the child created by this run.
    work = Path(tempfile.mkdtemp(prefix="hd2-python-go-bounded-", dir=args.work))
    work.mkdir(parents=True, exist_ok=True)
    audit.progress("package_begin", package="python")
    python_pkg = Package(python_zip, work)
    audit.progress("package_end", package="python", resources=len(python_pkg.resources))
    audit.progress("package_begin", package="go")
    go_pkg = Package(go_zip, work)
    audit.progress("package_end", package="go", resources=len(go_pkg.resources))
    audit.report["inputs"] = {
        "python": {"path": str(python_zip.resolve()), "sha256": file_sha(python_zip),
                   "bytes": python_zip.stat().st_size, "report_version": python_pkg.report.get("version")},
        "go": {"path": str(go_zip.resolve()), "sha256": file_sha(go_zip),
               "bytes": go_zip.stat().st_size, "report_version": go_pkg.report.get("version")},
    }
    audit.report["packages"] = {
        "python": {"members": len(python_pkg.members), "resources": len(python_pkg.resources),
                   "archives": sorted(python_pkg.patches)},
        "go": {"members": len(go_pkg.members), "resources": len(go_pkg.resources),
               "archives": sorted(go_pkg.patches)},
    }

    def selected(package):
        return {row["group"] for row in package.report.get("coverage", []) if row.get("selected")}

    p_sel, g_sel = selected(python_pkg), selected(go_pkg)
    required = {item.strip() for item in args.require_groups.split(",") if item.strip()}
    symbols = {"emoji", "special"}
    audit.report["scope"] = {
        "required_groups": sorted(required), "python_selected": sorted(p_sel), "go_selected": sorted(g_sel),
        "missing_required_in_python": sorted(required - p_sel),
        "missing_required_in_go": sorted(required - g_sel),
        "extra_in_python": sorted(p_sel - required - symbols),
        "extra_in_go": sorted(g_sel - required - symbols),
        "python_symbols": sorted(p_sel & symbols), "go_symbols": sorted(g_sel & symbols),
    }
    for side, selected_set in (("python", p_sel), ("go", g_sel)):
        missing = required - selected_set
        if missing:
            audit.issue("semantic", "scope_missing_required", package=side, groups=sorted(missing))
    if (p_sel & symbols) != (g_sel & symbols):
        audit.issue("semantic", "scope_symbol_mismatch", python=sorted(p_sel & symbols), go=sorted(g_sel & symbols))
    for side, selected_set in (("python", p_sel), ("go", g_sel)):
        extra = selected_set - required - symbols
        if extra:
            audit.issue("metadata", "scope_extra_groups", package=side, groups=sorted(extra))

    # Member scope and the package's own declared member hashes.
    fonts_a = {name for name in python_pkg.members if name.startswith("Fonts/")}
    fonts_b = {name for name in go_pkg.members if name.startswith("Fonts/")}
    audit.report["members"] = {
        "python_fonts": len(fonts_a), "go_fonts": len(fonts_b),
        "missing_in_go": sorted(fonts_a - fonts_b), "extra_in_go": sorted(fonts_b - fonts_a),
        "python_non_fonts": sorted(set(python_pkg.members) - fonts_a),
        "go_non_fonts": sorted(set(go_pkg.members) - fonts_b),
    }
    if fonts_a != fonts_b:
        audit.issue("semantic", "Fonts_member_scope", missing=sorted(fonts_a - fonts_b),
                    extra=sorted(fonts_b - fonts_a))
    for label, package in (("python", python_pkg), ("go", go_pkg)):
        declared = package.report.get("files", {})
        bad = []
        for name, row in declared.items():
            actual = package.members.get(name)
            if actual is None or actual["bytes"] != row.get("bytes") or actual["sha256"] != row.get("sha256"):
                bad.append({"name": name, "declared": row,
                            "actual": None if actual is None else {"bytes": actual["bytes"], "sha256": actual["sha256"]}})
        if bad:
            audit.issue("metadata", "declared_member_hash", package=label, count=len(bad), samples=bad[:args.max_samples])
    order_a = [name for name in python_pkg.members]
    order_b = [name for name in go_pkg.members]
    if order_a != order_b:
        audit.issue("metadata", "zip_member_order", python=order_a[:20], go=order_b[:20])
    compress_diff = {name: {"python": python_pkg.members[name]["compress_type"],
                            "go": go_pkg.members[name]["compress_type"]}
                     for name in fonts_a & fonts_b
                     if python_pkg.members[name]["compress_type"] != go_pkg.members[name]["compress_type"]}
    if compress_diff:
        audit.issue("metadata", "zip_compression_methods", count=len(compress_diff), samples=compress_diff)

    # Wrapper and resource scope.
    common = sorted(set(python_pkg.resources) & set(go_pkg.resources))
    missing = sorted(set(python_pkg.resources) - set(go_pkg.resources))
    extra = sorted(set(go_pkg.resources) - set(python_pkg.resources))
    audit.report["resources"] = {
        "python": len(python_pkg.resources), "go": len(go_pkg.resources),
        "missing_in_go": [list(key) for key in missing[:args.max_samples]],
        "extra_in_go": [list(key) for key in extra[:args.max_samples]],
        "missing_count": len(missing), "extra_count": len(extra), "common": len(common),
        "wrapper": {}, "fonts": {}, "atlas_fonts": {}, "textures": {},
    }
    if missing or extra:
        audit.issue("semantic", "resource_scope", missing_count=len(missing), extra_count=len(extra),
                    missing=[list(key) for key in missing[:args.max_samples]],
                    extra=[list(key) for key in extra[:args.max_samples]])
    wrapper_diffs = []
    face_directories = {}
    for key in common:
        a, b = python_pkg.resources[key], go_pkg.resources[key]
        if a["kind"] not in ("ttf", "otf", "runtime_font") or b["kind"] != a["kind"]:
            continue
        try:
            face_directories[key] = (font_descriptors(python_pkg, a), font_descriptors(go_pkg, b))
        except Exception as exc:
            audit.issue("semantic", "font_decode_resource", key=list(key), error=f"{type(exc).__name__}: {exc}")
    audit.progress("font_directories_validated", resources=len(face_directories))
    for key in common:
        a, b = python_pkg.resources[key], go_pkg.resources[key]
        if a["kind"] != b["kind"]:
            wrapper_diffs.append({"key": list(key), "kind": [a["kind"], b["kind"]]})
            continue
        ea, eb = a["entry"], b["entry"]
        fields = {
            "TypeID": (ea[1], eb[1]), "MainSize": (ea[7], eb[7]),
            "StreamSize": (ea[8], eb[8]), "GPUSize": (ea[9], eb[9]),
            "MainAlignment": (ea[10], eb[10]), "GPUAlignment": (ea[11], eb[11]),
            "Unknown0": (ea[5], eb[5]), "Unknown1": (ea[6], eb[6]),
        }
        if ea[7] != eb[7] and key in face_directories:
            audit.issue("metadata", "font_main_size_encoding", key=list(key),
                        python=ea[7], go=eb[7],
                        internal_declared_actual_lengths_verified=True,
                        note="Valid font containers may have different CFF/hmtx encodings; every font semantic check still runs.")
            del fields["MainSize"]
        offsets = {"MainOffset": (ea[2], eb[2]), "StreamOffset": (ea[3], eb[3]),
                   "GPUOffset": (ea[4], eb[4]), "Index": (ea[12], eb[12])}
        if any(x != y for x, y in fields.values()):
            wrapper_diffs.append({"key": list(key), "fields": {name: value for name, value in fields.items() if value[0] != value[1]}})
        if any(x != y for x, y in offsets.values()):
            audit.issue("metadata", "resource_offsets", key=list(key),
                        differences={name: value for name, value in offsets.items() if value[0] != value[1]})
        ta, tb = a.get("type"), b.get("type")
        if ta is not None and tb is not None:
            type_diff = {name: {"python": ta[i], "go": tb[i]} for i, name in enumerate(TYPE_NAMES)
                         if ta[i] != tb[i]}
            if type_diff:
                audit.issue("metadata", "type_record", key=list(key), differences=type_diff)
    if wrapper_diffs:
        audit.issue("semantic", "resource_wrapper", count=len(wrapper_diffs), samples=wrapper_diffs[:args.max_samples])

    # Payload hashes for resources that are compared semantically elsewhere.
    payload_hash_diff = []
    for key in common:
        a, b = python_pkg.resources[key], go_pkg.resources[key]
        if a["kind"] != b["kind"]:
            continue
        hashes = {}
        for channel in ("main", "stream", "gpu"):
            oa, sa = python_pkg.ranges(a, channel)
            ob, sb = go_pkg.ranges(b, channel)
            hashes[channel] = (hash_range(a[channel], oa, sa) if sa else None,
                               hash_range(b[channel], ob, sb) if sb else None)
        if hashes["main"][0] != hashes["main"][1] or hashes["stream"][0] != hashes["stream"][1] or hashes["gpu"][0] != hashes["gpu"][1]:
            payload_hash_diff.append({"key": list(key), "kind": a["kind"], "hashes": hashes})
    audit.report["metadata"]["payload_hash_differences"] = len(payload_hash_diff)

    # Independent font comparisons, using resource/slot identity.
    font_rows = []
    total_faces = sum(min(len(a), len(b)) for a, b in face_directories.values())
    face_number = 0
    for key, (faces_a, faces_b) in face_directories.items():
        if len(faces_a) != len(faces_b):
            audit.issue("semantic", "runtime_slot_count", key=list(key),
                        python=len(faces_a), go=len(faces_b))
        for face_a, face_b in zip(faces_a, faces_b):
            label = f"{key[0]}/{key[1]:016x}[{face_a['slot']}]"
            face_number += 1
            face_started = time.perf_counter()
            face_issue_start = len(audit.issues)
            row = None
            face_error = None
            audit.progress("face_begin", face=face_number, total_faces=total_faces, label=label)
            if face_a["metrics"] != face_b["metrics"]:
                audit.issue("semantic", "runtime_slot_metrics", font=label,
                            python=face_a["metrics"], go=face_b["metrics"])
            try:
                data_a = read_range(face_a["path"], face_a["offset"], face_a["size"])
                data_b = read_range(face_b["path"], face_b["offset"], face_b["size"])
                try:
                    row = audit.compare_pair(label, data_a, data_b)
                finally:
                    del data_a, data_b
                row["resource"] = list(key)
                row["slot"] = face_a["slot"]
                font_rows.append(row)
                audit.report["resources"]["fonts"][label] = {
                    "python_sha256": row["python"]["sha256"], "go_sha256": row["go"]["sha256"],
                    "codepoints": row["geometry"]["codepoints_checked"] + row["cmap"]["missing_in_go"] + row["cmap"]["extra_in_go"],
                }
                audit.progress("face_end", face=face_number, total_faces=total_faces, label=label,
                               seconds=round(time.perf_counter() - face_started, 3),
                               reused=row["pair_reuse"]["reused"],
                               codepoints=row["geometry"]["codepoints_checked"],
                               semantic_differences=len(row["semantic_differences"]),
                               live_font_cache=len(audit.snapshots))
            except Exception as exc:
                face_error = f"{type(exc).__name__}: {exc}"
                audit.issue("semantic", "font_audit_failed", font=label, error=f"{type(exc).__name__}: {exc}")
                audit.progress("face_error", face=face_number, total_faces=total_faces, label=label,
                               seconds=round(time.perf_counter() - face_started, 3), error=str(exc))
            audit.save_face(event="face_result" if face_error is None else "face_error",
                            face=face_number, total_faces=total_faces, label=label,
                            resource=list(key), slot=face_a["slot"],
                            seconds=round(time.perf_counter() - face_started, 3),
                            runtime_metrics={"python": face_a["metrics"], "go": face_b["metrics"]},
                            result=row, error=face_error, issues=audit.issues[face_issue_start:])
    audit.report["fonts"] = font_rows
    audit.report["bounded_cache"] = dict(audit.pair_stats,
        distinct_completed_sha_pairs=len(audit.pair_results), geometry_lru_limit=128,
        reuse_key="Ordered SHA-256 pair of exact SFNT extents; alignment-only padding independently validated",
        lifecycle="Decoded fonts and all geometry discarded after every fresh pair; compact comparison rows only retained")
    audit.pair_results.clear()
    audit.progress("font_phase_end", faces=len(font_rows), **audit.pair_stats)

    # Atlas pairs: use report associations only to identify which resource is
    # paired, then independently parse both resources.  Known game bindings are
    # a fallback if a report omits the association.
    def atlas_map(package):
        result = {}
        for row in package.report.get("atlases", []):
            try:
                fid, tid = int(row["font_id"], 16), int(row["texture_id"], 16)
            except Exception:
                continue
            result[(row.get("archive"), fid, tid)] = row
        return result
    amap_a, amap_b = atlas_map(python_pkg), atlas_map(go_pkg)
    for package, target in ((python_pkg, amap_a), (go_pkg, amap_b)):
        for archive, patch in package.patches.items():
            for entry in patch["entries"]:
                kind = KINDS.get(entry[1])
                if kind == "font" and entry[0] in KNOWN_BINDINGS:
                    target[(archive, entry[0], KNOWN_BINDINGS[entry[0]])] = {
                        "archive": archive, "font_id": f"{entry[0]:016x}",
                        "texture_id": f"{KNOWN_BINDINGS[entry[0]]:016x}",
                        "group": None, "source": "known_game_binding",
                    }
    atlas_rows = []
    for pair_key in sorted(set(amap_a) & set(amap_b)):
        archive, fid, tid = pair_key
        fa, fb = find_resource(python_pkg, archive, fid, "font"), find_resource(go_pkg, archive, fid, "font")
        ta, tb = find_resource(python_pkg, archive, tid, "texture"), find_resource(go_pkg, archive, tid, "texture")
        label = f"{archive}/font-{fid:016x}/texture-{tid:016x}"
        if fa is None or fb is None or ta is None or tb is None:
            audit.issue("semantic", "atlas_resource_missing", atlas=label,
                        python=[fa is not None, ta is not None], go=[fb is not None, tb is not None])
            continue
        try:
            row = {"label": label, "group": amap_a[pair_key].get("group"),
                   "association": {"python": amap_a[pair_key], "go": amap_b[pair_key]}}
            row["font_table"] = compare_glyph_tables(audit, label, glyph_table(python_pkg.payload(fa)),
                                                       glyph_table(go_pkg.payload(fb)))
            row["texture"] = compare_textures(audit, label, texture_info(python_pkg, ta),
                                               texture_info(go_pkg, tb))
            atlas_rows.append(row)
        except Exception as exc:
            audit.issue("semantic", "atlas_audit_failed", atlas=label, error=f"{type(exc).__name__}: {exc}")
    audit.report["atlases"] = atlas_rows
    if set(amap_a) != set(amap_b):
        audit.issue("semantic", "atlas_association_scope",
                    missing_in_go=[list(key) for key in sorted(set(amap_a) - set(amap_b))],
                    extra_in_go=[list(key) for key in sorted(set(amap_b) - set(amap_a))])

    # Manifest and report associations are metadata, but record exact text.
    try:
        ma = json.loads((python_pkg.root / "manifest.json").read_text("utf-8-sig"))
        mb = json.loads((go_pkg.root / "manifest.json").read_text("utf-8-sig"))
        manifest_diff = diff_values(ma, mb, tuple(ma))
        if manifest_diff:
            audit.issue("metadata", "manifest", differences=manifest_diff)
    except Exception as exc:
        audit.issue("semantic", "manifest_missing_or_invalid", error=f"{type(exc).__name__}: {exc}")

    semantic_count = sum(1 for item in audit.issues if item["severity"] == "semantic")
    metadata_count = sum(1 for item in audit.issues if item["severity"] == "metadata")
    audit.report["summary"] = {
        "semantic_pass": semantic_count == 0,
        "semantic_issue_count": semantic_count,
        "metadata_attention_count": metadata_count,
        "fonts_compared": len(font_rows),
        "atlas_pairs_compared": len(atlas_rows),
        "required_groups": sorted(required),
        "geometry_checked_codepoints": sum(row["geometry"]["codepoints_checked"] for row in font_rows),
        "geometry_mismatch_codepoints": sum(row["geometry"]["mismatches"] for row in font_rows),
        "advance_mismatch_codepoints": sum(row["advances"]["mismatches"] for row in font_rows),
        "cmap_missing_in_go": sum(row["cmap"]["missing_in_go"] for row in font_rows),
        "cmap_extra_in_go": sum(row["cmap"]["extra_in_go"] for row in font_rows),
        "exit_code": 0 if semantic_count == 0 and not (args.fail_on_metadata and metadata_count) else 1,
        "metadata_policy": "numberOfHMetrics, bbox, checksums, table bytes and offsets are separately recorded metadata; use --fail-on-metadata to make them nonzero",
    }
    audit.report["complete"] = True
    if args.keep_work:
        audit.report["work_directory"] = str(work)
    else:
        shutil.rmtree(work, ignore_errors=True)
    return audit.report


def main() -> int:
    parser = argparse.ArgumentParser(description="Independent 1.3 Python vs new-Go package oracle")
    parser.add_argument("--python", type=Path, required=True, help="1.3 Python reference ZIP")
    parser.add_argument("--go", type=Path, required=True, help="new Go output ZIP")
    parser.add_argument("--out", type=Path, required=True, help="independent difference JSON")
    parser.add_argument("--require-groups", default="latin,sc,tc,jp,ko")
    parser.add_argument("--area-tolerance", type=float, default=0.01)
    parser.add_argument("--contour-area-tolerance", type=float, default=0.01,
                        help="signed contour-area multiset tolerance; direction counts are exact")
    parser.add_argument("--max-samples", type=int, default=20)
    parser.add_argument("--work", type=Path, help="extraction work directory (default: system TEMP)")
    parser.add_argument("--keep-work", action="store_true")
    parser.add_argument("--fail-on-metadata", action="store_true")
    parser.add_argument("--force", action="store_true")
    args = parser.parse_args()
    for label, path in (("python", args.python), ("go", args.go)):
        if not path.is_file():
            parser.error(f"{label} ZIP not found: {path}")
    if args.out.exists() and not args.force:
        parser.error(f"refusing to overwrite report: {args.out}")
    if args.work:
        args.work = args.work.resolve()
        args.work.mkdir(parents=True, exist_ok=True)
    try:
        report = compare_packages(args.python, args.go, args)
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        summary = report["summary"]
        print(json.dumps({
            "semantic_pass": summary["semantic_pass"],
            "semantic_issue_count": summary["semantic_issue_count"],
            "metadata_attention_count": summary["metadata_attention_count"],
            "fonts_compared": summary["fonts_compared"],
            "atlas_pairs_compared": summary["atlas_pairs_compared"],
            "geometry_checked_codepoints": summary["geometry_checked_codepoints"],
            "geometry_mismatch_codepoints": summary["geometry_mismatch_codepoints"],
            "advance_mismatch_codepoints": summary["advance_mismatch_codepoints"],
            "cmap_missing_in_go": summary["cmap_missing_in_go"],
            "cmap_extra_in_go": summary["cmap_extra_in_go"],
        }, ensure_ascii=False), flush=True)
        return summary["exit_code"]
    except Exception as exc:
        report = {"schema": SCHEMA, "complete": False, "fatal": f"{type(exc).__name__}: {exc}",
                  "created_at_utc": datetime.now(timezone.utc).isoformat()}
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(json.dumps({"fatal": report["fatal"]}, ensure_ascii=False), file=sys.stderr, flush=True)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
