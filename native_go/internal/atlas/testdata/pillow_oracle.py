"""Optional test-only oracle. No fonts or game assets are read or written."""
import hashlib
import json
from PIL import Image, __version__

cases = []
for width, height in [(1, 1), (2, 2), (4, 8), (1, 9), (9, 1), (5, 3), (7, 5), (257, 129), (512, 256)]:
    raw = bytes((i * 73 + (i // 7) * 19 + 11) % 256 for i in range(width * height * 4))
    current = Image.frombytes("RGBA", (width, height), raw)
    pixels = bytearray()
    while True:
        pixels.extend(current.tobytes())
        if current.size == (1, 1):
            break
        size = tuple(max(1, n // 2) for n in current.size)
        current = Image.merge("RGBA", tuple(c.resize(size, Image.Resampling.BOX) for c in current.split()))
    cases.append({"width": width, "height": height, "bytes": len(pixels), "sha256": hashlib.sha256(pixels).hexdigest()})
print(json.dumps({"pillow": __version__, "cases": cases}, indent=2))
