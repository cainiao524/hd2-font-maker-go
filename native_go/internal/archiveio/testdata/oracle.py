"""Test-only oracle: run selected original functions on synthetic byte fixtures.

This does not import the application, fontTools, lz4, or access installed fonts.
Run from any directory; stdout is the checked-in python_golden.json fixture.
"""
import ast
import json
from pathlib import Path
import struct


ROOT = Path(__file__).resolve().parents[4]


def original(filename, names):
    tree = ast.parse((ROOT / filename).read_text(encoding="utf-8"))
    selected = []
    for node in tree.body:
        if isinstance(node, ast.FunctionDef) and node.name in names:
            selected.append(node)
        elif isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id in names
            for target in node.targets
        ):
            selected.append(node)
    scope = {"struct": struct}
    exec(compile(ast.Module(body=selected, type_ignores=[]), filename, "exec"), scope)
    return scope


archive = original("archives.py", {
    "HEADER", "TYPE", "ENTRY", "MAGIC", "resource_hash", "make_patch", "make_patch_parts"
})
font = original("fonts.py", {"sfnt_size", "runtime_parts", "make_runtime"})


def resource(name, kind, main, stream=b"", gpu=b"", alignment=0, gpu_alignment=0):
    entry = (name, kind, 31, 47, 79, 0xFEDCBA9876543210, 0x123456789ABCDEF0,
             3, len(stream), len(gpu), alignment, gpu_alignment, 0xAABBCCDD)
    typ = (0x12345678, 0x89ABCDEF, kind, 99, 17, 19, 23)
    return entry, typ, main, stream, gpu


rows = [
    resource(100-i, 55+i % 2, b"abc"*i, b"x"*(i % 4), b"gpu"*i,
             (0, 24, 128)[i % 3], (0, 64, 17)[i % 3])
    for i in range(9)
]
rows += [resource(0xFEDCBA9876543210, 0xF234567890ABCDEF, b"high", b"s", b"g", 32, 64)]
# An empty final member still pads companions to its alignment.
rows += [resource(0xFFFFFFFFFFFFFFFF, 0xF234567890ABCDEF, b"", b"", b"", 128, 64)]


def case(name, records, main_only=False):
    if main_only:
        result = [archive["make_patch"]([(e, t, m) for e, t, m, _, _ in records]), b"", b""]
    else:
        result = archive["make_patch_parts"](records)
    return {
        "name": name,
        "main_only": main_only,
        "resources": [
            {"entry": e, "type": t, "main": m.hex(), "stream": s.hex(), "gpu": g.hex()}
            for e, t, m, s, g in records
        ],
        "expected": [part.hex() for part in result],
    }


def sfnt(magic, data):
    return (struct.pack(">4sHHHH", magic, 1, 0, 0, 0)
            + struct.pack(">4sIII", b"test", 0, 32, len(data))
            + bytes(4) + data)


parts = [((800.25, -200.5, 1000.75), sfnt(b"\0\1\0\0", b"abcde") + b"tail"),
         ((-0.0, 1.5, -2.75), sfnt(b"OTTO", b"xyz"))]
template = bytes(range(64))
runtime = font["make_runtime"](template, parts)
result = {
    "hashes": [{"text": text, "value": archive["resource_hash"](text)}
               for text in ["", "ttf", "otf", "runtime_font", "font", "texture",
                            "\u4e2d\u6587/\u5b57\u9ad4", "a\0b"]
               + ["a"*n for n in range(1, 18)]],
    "patches": [case("mixed_channels", rows), case("empty_archive", []),
                case("main_only", [resource(9, 5, b"main", alignment=24),
                                   resource(3, 5, b"", alignment=128)], True)],
    "runtime": {"template": template.hex(),
                "parts": [{"metrics": metrics, "data": data.hex()} for metrics, data in parts],
                "expected": runtime.hex(),
                "decoded": [data.hex() for _, data in font["runtime_parts"](runtime)]},
}
print(json.dumps(result, indent=2, ensure_ascii=True))
