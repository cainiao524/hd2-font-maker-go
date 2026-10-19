"""Read-only, bounded HarfBuzz shaping parity for already extracted packages.

Usage: python -B compare_shaping.py --work EXISTING_ROOT --out REPORT.json
No font files are written, extracted, modified or installed. Only --out is
written. Each unique full-SFNT SHA pair runs in its own time-limited subprocess.
Exit 0: tested semantics agree; 1: differences; 2: incomplete/error.
"""
from __future__ import annotations

import argparse
from collections import Counter
import ctypes as ct
import io
import json
from pathlib import Path
import subprocess
import sys
import time

sys.dont_write_bytecode = True
import compare_packages_bounded as oracle
from fontTools.ttLib import TTFont

HERE = Path(__file__).resolve().parent
NATIVE = HERE.parent.parent
DLL = NATIVE / "bin" / "hd2harfbuzz.dll"
FEATURES = ("kern=1,liga=1", "kern=0,liga=1", "kern=1,liga=0", "kern=0,liga=0")
CASES = [
    {"text": "AVATAR", "script": "Latn", "language": "en"},
    {"text": "To To", "script": "Latn", "language": "en"},
    {"text": "office", "script": "Latn", "language": "en"},
    {"text": "fi ffi fl ffl", "script": "Latn", "language": "en"},
    {"text": "A V AV  To", "script": "Latn", "language": "en"},
    {"text": "Player_01 [AVATAR]", "script": "Latn", "language": "en"},
    {"text": "设置选项：据点与玩家名", "script": "Hani", "language": "zh-cn"},
    {"text": "設定選項：據點與玩家名", "script": "Hani", "language": "zh-tw"},
    {"text": "简繁混排：龙龍，国國；彌弥。", "script": "Hani", "language": "zh-cn"},
    {"text": "中文，。！？「玩家」【設定】", "script": "Hani", "language": "zh-tw"},
    {"text": "Player_小明-繁體01", "script": "Hani", "language": "zh-cn"},
    {"text": "据 AV 國 To 設定 fi", "script": "Hani", "language": "zh-cn"},
    {"text": "かなカナ がぎぐげご", "script": "Hira", "language": "ja"},
    {"text": "設定と漢字：日本語プレイヤー", "script": "Hani", "language": "ja"},
    {"text": "か\u3099 カ\u3099 は\u309a", "script": "Hira", "language": "ja"},
    {"text": "한국어 설정 플레이어", "script": "Hang", "language": "ko"},
    {"text": "한글 玩家01", "script": "Hang", "language": "ko"},
    {"text": "café Ångström naïve", "script": "Latn", "language": "en"},
    {"text": "Cafe\u0301 A\u030a n\u0303 a\u0308\u0301", "script": "Latn", "language": "en"},
    {"text": "Άλφα ᾀ ɡ ǽ ǿ", "script": "Grek", "language": "el"},
]


# ABI copied from native/harfbuzz/bridge.h, with native x64 alignment.
class FaceResult(ct.Structure):
    _fields_ = [("handle", ct.c_void_p), ("error", ct.c_char * 256)]


class ShapeInput(ct.Structure):
    _fields_ = [("text", ct.c_char_p), ("text_length", ct.c_uint64),
                ("script", ct.c_char_p), ("language", ct.c_char_p),
                ("direction", ct.c_char_p), ("features", ct.c_char_p),
                ("upm", ct.c_uint32), ("reserved", ct.c_uint32)]


class Glyph(ct.Structure):
    _fields_ = [("gid", ct.c_uint32), ("cluster", ct.c_uint32),
                ("x_advance", ct.c_int32), ("y_advance", ct.c_int32),
                ("x_offset", ct.c_int32), ("y_offset", ct.c_int32)]


class ShapeResult(ct.Structure):
    _fields_ = [("glyphs", ct.POINTER(Glyph)), ("count", ct.c_uint64),
                ("error", ct.c_char * 256)]


class HarfBuzz:
    def __init__(self, dll=DLL):
        if ct.sizeof(ct.c_void_p) != 8 or ct.sizeof(ShapeInput) != 56 or ct.sizeof(Glyph) != 24:
            raise RuntimeError("requires the x64 bridge.h ABI")
        self.dll = ct.CDLL(str(Path(dll).resolve()))
        for name in ("hd_hb_version", "hd_hb_source_version"):
            getattr(self.dll, name).argtypes = []
            getattr(self.dll, name).restype = ct.c_uint32
        self.dll.hd_hb_face_create.argtypes = [ct.c_void_p, ct.c_uint64, ct.c_uint32, ct.POINTER(FaceResult)]
        self.dll.hd_hb_face_create.restype = ct.c_int
        self.dll.hd_hb_face_destroy.argtypes = [ct.c_void_p]
        self.dll.hd_hb_face_destroy.restype = None
        self.dll.hd_hb_shape.argtypes = [ct.c_void_p, ct.POINTER(ShapeInput), ct.POINTER(ShapeResult)]
        self.dll.hd_hb_shape.restype = ct.c_int
        self.dll.hd_hb_shape_free.argtypes = [ct.POINTER(ShapeResult)]
        self.dll.hd_hb_shape_free.restype = None

    def face(self, data):
        result = FaceResult()
        raw = ct.create_string_buffer(data)
        code = self.dll.hd_hb_face_create(raw, len(data), 0, ct.byref(result))
        if code or not result.handle:
            raise RuntimeError(bytes(result.error).decode("utf-8", "replace"))
        # make_face duplicates data (HB_MEMORY_MODE_DUPLICATE).
        return result.handle

    def shape(self, face, case, features):
        raw = case["text"].encode("utf-8")
        spec = ShapeInput(raw, len(raw), case["script"].encode("ascii"),
                          case["language"].encode("ascii"), b"ltr", features.encode("ascii"), 0, 0)
        result = ShapeResult()
        try:
            code = self.dll.hd_hb_shape(face, ct.byref(spec), ct.byref(result))
            if code:
                raise RuntimeError(bytes(result.error).decode("utf-8", "replace"))
            if result.count > len(raw) * 32 + 32:
                raise ValueError("unexpected shaping expansion")
            return [tuple(getattr(result.glyphs[i], name) for name, _ in Glyph._fields_)
                    for i in range(result.count)]
        finally:
            self.dll.hd_hb_shape_free(ct.byref(result))


def geometry_pair(cache, fonts, hashes, names):
    a, b = [cache.glyph(font, digest, name) for font, digest, name in zip(fonts, hashes, names)]
    directions = a[4] == b[4]
    areas_a, areas_b = sorted(a[3]), sorted(b[3])
    signed_areas = len(areas_a) == len(areas_b) and all(abs(x-y) <= .01 for x, y in zip(areas_a, areas_b))
    exact = a[0] == b[0]
    delta = 0.0 if exact else cache.xor_area(fonts[0], hashes[0], names[0], fonts[1], hashes[1], names[1])
    if delta is None:
        return {"status": "incomplete", "reason": "pathops comparison unavailable/failed"}
    passed = directions and signed_areas and abs(delta) <= .01
    row = {"status": "pass" if passed else "fail", "exact_commands": exact, "xor_area": delta,
           "directions_equal": directions, "signed_areas_equal": signed_areas}
    if not passed:
        row.update(python_directions=a[4], go_directions=b[4],
                   python_signed_areas=areas_a[:12], go_signed_areas=areas_b[:12],
                   python_bounds=a[1], go_bounds=b[1])
    return row


def compare_pair(data_a, data_b, hb, *, geometry_seconds=5.0):
    start = time.perf_counter()
    hashes = [oracle.sha(data_a), oracle.sha(data_b)]
    fonts, faces = [], []
    try:
        for data in (data_a, data_b):
            fonts.append(TTFont(io.BytesIO(data), lazy=True, recalcBBoxes=False, recalcTimestamp=False))
            faces.append(hb.face(data))
        orders = [font.getGlyphOrder() for font in fonts]
        cmaps = [font.getBestCmap() or {} for font in fonts]
        mapped_names = [set(cmap.values()) for cmap in cmaps]
        upms = [font["head"].unitsPerEm for font in fonts]
        cache, checked, runs = oracle.GeometryCache(), {}, []
        geometry_started = None
        for case_id, case in enumerate(CASES):
            for feature_id, features in enumerate(FEATURES):
                shaped = [hb.shape(face, case, features) for face in faces]
                a, b = shaped
                clusters = [row[1] for row in a] == [row[1] for row in b]
                metrics = len(a) == len(b) and all(x[2:] == y[2:] for x, y in zip(a, b))
                missing_equal = len(a) == len(b) and all((x[0] == 0) == (y[0] == 0) for x, y in zip(a, b))
                row = {"case": case_id, "features": feature_id, "status": "pass",
                       "glyph_counts": [len(a), len(b)], "clusters_equal": clusters,
                       "advances_offsets_equal": metrics, "notdef_counts": [sum(x[0] == 0 for x in a), sum(x[0] == 0 for x in b)],
                       "total_advance": [[sum(x[2] for x in side), sum(x[3] for x in side)] for side in shaped],
                       "geometry": {"pass": 0, "fail": 0, "incomplete": 0},
                       "emitted_not_in_cmap": [0, 0]}
                if not clusters or not metrics or not missing_equal or upms[0] != upms[1]:
                    row["status"] = "fail"
                    row["examples"] = [{"index": i, "python": a[i] if i < len(a) else None,
                                        "go": b[i] if i < len(b) else None}
                                       for i in range(max(len(a), len(b)))
                                       if i >= len(a) or i >= len(b) or a[i][1:] != b[i][1:] or (a[i][0] == 0) != (b[i][0] == 0)][:4]
                # GIDs are only local references, never equality evidence.
                # Compare only aligned emitted glyphs; any unaligned sequence
                # already fails above. This includes GSUB-only ligature GIDs.
                for glyph_index, (x, y) in enumerate(zip(a, b)):
                    ids = x[0], y[0]
                    names = [order[gid] for order, gid in zip(orders, ids)]
                    for side in range(2):
                        row["emitted_not_in_cmap"][side] += int(ids[side] != 0 and names[side] not in mapped_names[side])
                    if ids not in checked:
                        if geometry_started is None:
                            geometry_started = time.perf_counter()
                        if time.perf_counter() - geometry_started > geometry_seconds:
                            checked[ids] = {"status": "incomplete", "reason": "per-pair geometry time budget"}
                        else:
                            try:
                                checked[ids] = geometry_pair(cache, fonts, hashes, names)
                            except Exception as exc:
                                checked[ids] = {"status": "incomplete", "reason": f"{type(exc).__name__}: {exc}"}
                        if checked[ids]["status"] != "pass":
                            checked[ids].update(names=names, first_occurrence={"case": case_id,
                                "features": feature_id, "glyph_index": glyph_index,
                                "clusters": [x[1], y[1]], "emitted_not_in_cmap": [
                                    ids[side] != 0 and names[side] not in mapped_names[side] for side in range(2)]})
                    state = checked[ids]["status"]
                    row["geometry"][state] += 1
                    if state == "fail":
                        row["status"] = "fail"
                    elif state == "incomplete" and row["status"] == "pass":
                        row["status"] = "incomplete"
                runs.append(row)
        counts = dict(Counter(row["status"] for row in runs))
        status = "incomplete" if counts.get("incomplete") or any(x["status"] == "incomplete" for x in checked.values()) else "fail" if counts.get("fail") else "pass"
        missing = [{"case": i, "python": sorted(set(ord(ch) for ch in case["text"]) - cmaps[0].keys()),
                    "go": sorted(set(ord(ch) for ch in case["text"]) - cmaps[1].keys())} for i, case in enumerate(CASES)]
        return {"status": status, "sha256": hashes, "upm": upms, "seconds": round(time.perf_counter()-start, 3),
                "runs_summary": counts, "runs": runs, "missing_input_codepoints": [x for x in missing if x["python"] or x["go"]],
                "unique_emitted_glyph_pairs": len(checked),
                "geometry_summary": dict(Counter(x["status"] for x in checked.values())),
                "geometry_examples": [{"gids": ids, **value} for ids, value in checked.items() if value["status"] != "pass"][:8],
                "geometry_cache": {"fresh": cache.fresh, "reused": cache.reused, "peak_rows": cache.peak_rows}}
    finally:
        for face in faces:
            hb.dll.hd_hb_face_destroy(face)
        for font in fonts:
            font.close()


def open_extracted(root):
    # Bypass Package.__init__: it extracts ZIPs and writes files. This entry
    # point only reads existing patch indexes and validated SFNT ranges.
    package = oracle.Package.__new__(oracle.Package)
    package.root, package.resources, package.patches = root, {}, {}
    package._parse_patches()
    if not package.resources:
        raise ValueError(f"no existing patch resources in {root}")
    return package


def descriptors(work):
    children = list(work.iterdir())
    roots = []
    for prefix in ("python-", ("parity-go-", "v13-go-")):
        candidates = [p for p in children if p.is_dir() and p.name.startswith(prefix)]
        if len(candidates) != 1:
            raise ValueError(f"expected one {prefix} subdirectory, got {len(candidates)}")
        roots.append(candidates[0])
    packages = [open_extracted(p) for p in roots]
    keys = [{k for k, r in p.resources.items() if r["kind"] in ("ttf", "otf", "runtime_font")} for p in packages]
    mismatches = [{"missing_font_resources": [list(k) for k in sorted(keys[0]-keys[1])],
                   "extra_font_resources": [list(k) for k in sorted(keys[1]-keys[0])]}] if keys[0] != keys[1] else []
    pairs = {}
    instances = 0
    for key in sorted(keys[0] & keys[1]):
        sides = [oracle.font_descriptors(p, p.resources[key]) for p in packages]
        if len(sides[0]) != len(sides[1]):
            mismatches.append({"resource": list(key), "slot_counts": list(map(len, sides))})
        for a, b in zip(*sides):
            hashes = tuple(oracle.hash_range(d["path"], d["offset"], d["size"]) for d in (a, b))
            label = f"{key[0]}/{key[1]:016x}/{packages[0].resources[key]['kind']}[{a['slot']}]"
            instances += 1
            if hashes not in pairs:
                pairs[hashes] = {"sha256": hashes, "aliases": [], "descriptors": [
                    {"path": str(d["path"]), "offset": d["offset"], "size": d["size"]} for d in (a, b)]}
            pairs[hashes]["aliases"].append(label)
    return roots, list(pairs.values()), instances, mismatches


def worker():
    request = json.load(sys.stdin)
    data = [oracle.read_range(Path(d["path"]), d["offset"], d["size"]) for d in request["descriptors"]]
    if [oracle.sha(d) for d in data] != request["sha256"]:
        raise ValueError("input SFNT changed since pair discovery")
    hb = HarfBuzz()
    result = compare_pair(*data, hb, geometry_seconds=request["geometry_seconds"])
    result["harfbuzz_abi"] = hb.dll.hd_hb_version()
    result["harfbuzz_source_version"] = hb.dll.hd_hb_source_version()
    print(json.dumps(result, ensure_ascii=True, separators=(",", ":")))


def manifest(work):
    return {str(p.relative_to(work)): (p.stat().st_size, p.stat().st_mtime_ns)
            for p in work.rglob("*") if p.is_file()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--pair-seconds", type=float, default=12)
    parser.add_argument("--geometry-seconds", type=float, default=5)
    args = parser.parse_args()
    work, out = args.work.resolve(strict=True), args.out.resolve()
    if out.is_relative_to(work):
        parser.error("--out must be outside the read-only extracted --work tree")
    if args.pair_seconds <= 0 or args.geometry_seconds <= 0:
        parser.error("time limits must be positive")
    started = time.perf_counter()
    before = manifest(work)
    roots, pairs, instances, mismatches = descriptors(work)
    if not pairs:
        raise ValueError("no matching SFNT pairs")
    report = {"schema": "hd2-shaping-parity-v1", "status": "running", "work": str(work),
              "inputs": list(map(str, roots)), "read_only_inputs": True,
              "script_sha256": oracle.file_sha(Path(__file__)),
              "comparator_sha256": oracle.file_sha(HERE / "compare_packages_bounded.py"),
              "dll_sha256": oracle.file_sha(DLL), "cases": CASES, "features": FEATURES,
              "direction": "ltr", "cluster_units": "UTF-8 byte offsets", "metrics_units": "native UPM, required equal",
              "bounds": {"pair_seconds": args.pair_seconds, "geometry_seconds": args.geometry_seconds},
              "font_instances": instances, "unique_sha_pairs": len(pairs), "deduplicated_instances": instances-len(pairs),
              "scope_mismatches": mismatches, "pairs": [],
              "policy": "All emitted clusters/advances/offsets compared exactly. GIDs may differ. Emitted outlines use comparator cache/recording/XOR with direction counts and signed contour areas required (0.01 tolerances). Missing characters are reported, not treated as coverage proof. No whole-cmap geometry audit; no game rendering claim."}
    out.parent.mkdir(parents=True, exist_ok=True)
    for index, pair in enumerate(pairs):
        request = {**pair, "geometry_seconds": args.geometry_seconds}
        tick = time.perf_counter()
        try:
            run = subprocess.run([sys.executable, "-B", "-X", "utf8", str(Path(__file__)), "--worker"],
                                 input=json.dumps(request), capture_output=True, text=True, encoding="utf-8",
                                 timeout=args.pair_seconds)
            if run.returncode:
                result = {"status": "incomplete", "error": run.stderr[-1400:], "returncode": run.returncode}
            else:
                result = json.loads(run.stdout)
        except subprocess.TimeoutExpired:
            result = {"status": "incomplete", "error": "pair subprocess hard timeout"}
        result.update(aliases=pair["aliases"], sha256=pair["sha256"], wall_seconds=round(time.perf_counter()-tick, 3))
        report["pairs"].append(result)
        out.write_text(json.dumps(report, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
        print(f"{index+1}/{len(pairs)} {result['status']} {result['wall_seconds']:.2f}s {pair['aliases'][0]}", flush=True)
    report["input_file_metadata_unchanged"] = before == manifest(work)
    summary = Counter(row["status"] for row in report["pairs"])
    run_counts = Counter()
    geometry_counts = Counter()
    for pair in report["pairs"]:
        run_counts.update(pair.get("runs_summary", {}))
        geometry_counts.update(pair.get("geometry_summary", {}))
    report["summary"] = {"pairs": dict(summary), "runs": dict(run_counts), "emitted_geometry_pairs": dict(geometry_counts)}
    status = "incomplete" if summary.get("incomplete") or not report["input_file_metadata_unchanged"] else "fail" if summary.get("fail") or mismatches else "pass"
    report.update(status=status, seconds=round(time.perf_counter()-started, 3))
    out.write_text(json.dumps(report, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
    print(json.dumps({"status": status, "seconds": report["seconds"], "summary": report["summary"]}))
    return {"pass": 0, "fail": 1, "incomplete": 2}[status]


if __name__ == "__main__":
    if sys.argv[1:] == ["--worker"]:
        worker()
    else:
        raise SystemExit(main())
